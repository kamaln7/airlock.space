package apod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // decoders for whatever NASA posts: registering them here is
	_ "image/jpeg" // this package's business, not a dependency's to do by accident
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	apodclient "github.com/kamaln7/airlock.space/pkg/apod"
	"github.com/kamaln7/resolvable/v2"
	"golang.org/x/sync/singleflight"
)

// live is the process-wide APOD fetcher the TUI calls through Today/ByDate.
var live = &apod{client: apodclient.NewClient(
	apodclient.WithHTTPClient(&http.Client{Transport: http.DefaultTransport, Timeout: 15 * time.Second}),
)}

var Today = resolvable.New(
	live.getAPOD,
	resolvable.Retry(3, nil),
	resolvable.StaleOnError(),
	resolvable.CacheFor(time.Minute),
).WithBackgroundContext()

type APOD struct {
	apodclient.Picture
	Explanation string

	// ImageBytes is the compressed featured image, or nil when NASA provides
	// none. Video entries may have a poster image. Absence is a cached value.
	// Decode on demand and discard: a decoded APOD is tens of MB, the box has 512.
	ImageBytes resolvable.V[[]byte]

	// ImageSize is the picture's size in pixels. It is known from the first
	// few kilobytes of the download; WaitImageSize says when.
	ImageSize image.Point
	sized     chan struct{} // closed once ImageSize is set
	sizeOnce  sync.Once
}

// First is the day APOD began; there is nothing to browse before it.
var First = time.Date(1995, 6, 16, 0, 0, 0, 0, time.UTC)

// daysInMemory bounds the browsable history kept in RAM: each APOD holds its
// compressed image, and the box has 512MB. Evicted days come back from the
// disk cache without another download.
const daysInMemory = 4

// ponytail: FIFO eviction, not LRU. Swap if browsing patterns make it matter.
var days = struct {
	sync.Mutex
	byDate map[string]*APOD
	order  []string
}{byDate: map[string]*APOD{}}

// dateFlight coalesces concurrent ByDate misses for the same day.
// resolvable already does this for Today and per-day ImageBytes; ByDate is a map.
var dateFlight singleflight.Group

// ByDate resolves the APOD posted on a given day. Past days never change, so
// each is fetched once and reused, which is what makes stepping back and forth
// through history instant.
func ByDate(t time.Time) (*APOD, error) {
	key := t.Format(time.DateOnly)

	days.Lock()
	a, ok := days.byDate[key]
	days.Unlock()
	if ok {
		return a, nil
	}

	v, err, _ := dateFlight.Do(key, func() (any, error) {
		var entry *apodclient.Picture
		if rec := loadDay(key); rec != nil {
			entry = rec.APOD
		} else {
			var err error
			entry, err = live.client.ByDate(context.Background(), t)
			if err != nil {
				return nil, err
			}
		}
		a, err := newAPOD(entry)
		if err != nil {
			return nil, err
		}

		days.Lock()
		defer days.Unlock()
		if existing, ok := days.byDate[key]; ok {
			return existing, nil
		}
		days.byDate[key] = a
		days.order = append(days.order, key)
		for len(days.order) > daysInMemory {
			delete(days.byDate, days.order[0])
			days.order = days.order[1:]
		}
		return a, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*APOD), nil
}

type apod struct {
	client *apodclient.Client
	last   *APOD
}

func (n *apod) getAPOD(ctx context.Context) (*APOD, error) {
	var entry *apodclient.Picture
	var err error
	if d := os.Getenv("APOD_DATE"); d != "" { // e.g. APOD_DATE=2026-08-29, for testing
		if n.last != nil && n.last.Date.Format(time.DateOnly) == d {
			return n.last, nil
		}
		var t time.Time
		if t, err = time.Parse(time.DateOnly, d); err != nil {
			return nil, fmt.Errorf("invalid APOD_DATE: %w", err)
		}
		entry, err = n.client.ByDate(ctx, t)
	} else {
		entry, err = n.client.Latest(ctx)
	}
	if err != nil {
		return nil, err
	}
	if n.last == nil || !n.last.Date.Equal(entry.Date) {
		a, err := newAPOD(entry)
		if err != nil {
			return nil, err
		}
		slog.Info("new APOD", "date", entry.Date.Format(time.DateOnly))
		n.last = a
	}
	return n.last, nil
}

// dropExplanationLabel removes NASA's leading "Explanation:" label, with the
// whitespace or break after it, from converted Markdown. The converter wraps
// strong/em in symmetric "**", "*" or "***". When that emphasis also covers
// body words, it stays and only the label goes. Anything else is returned as is.
func dropExplanationLabel(md string) string {
	s := strings.TrimLeftFunc(md, unicode.IsSpace)
	var delim string
	for _, d := range []string{"***", "**", "*"} {
		if strings.HasPrefix(s, d) {
			delim = d
			s = s[len(d):]
			break
		}
	}
	s, ok := strings.CutPrefix(s, "Explanation:")
	if !ok {
		return md
	}
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	if delim == "" {
		return s
	}
	if s, ok := strings.CutPrefix(s, delim); ok {
		return strings.TrimLeftFunc(s, unicode.IsSpace)
	}
	return delim + s
}

// newAPOD derives the Markdown explanation once, without its leading
// "Explanation:" label; a conversion failure is an error, not a lossy
// substitute. Picture.ExplanationHTML stays as published.
func newAPOD(entry *apodclient.Picture) (*APOD, error) {
	explanation, err := htmltomarkdown.ConvertString(entry.ExplanationHTML, converter.WithDomain(entry.ArticleURL))
	if err != nil {
		return nil, fmt.Errorf("converting explanation: %w", err)
	}
	a := &APOD{Picture: *entry, Explanation: dropExplanationLabel(explanation), sized: make(chan struct{})}
	a.ImageBytes = resolvable.New(a.getImageBytes,
		resolvable.Retry(3, nil),
		resolvable.CacheForever(),
	).WithBackgroundContext()
	return a, nil
}

// errNotAnImage means the URL served something that is not an image (an mp4 on
// video days), so there is nothing here to retry.
var errNotAnImage = errors.New("not an image")

// getImageBytes resolves nil bytes for an APOD that has no image, and an error
// only when the fetch itself failed and is worth another try.
// cacheDir holds compressed source images between restarts. systemd hands us
// StateDirectory; otherwise the user cache dir. Empty disables the cache, and
// every use is best-effort: a cache miss is only ever slower, never wrong.
// ponytail: never pruned. One image per day browsed, so add a sweep if the
// disk ever complains.
var cacheDir = imageCacheDir()

func imageCacheDir() string {
	// Browsers use HTTP caching and the in-memory day cache, not a filesystem.
	if runtime.GOOS == "js" {
		return ""
	}
	dir, _, _ := strings.Cut(os.Getenv("STATE_DIRECTORY"), ":") // systemd may list several
	if dir == "" {
		d, err := os.UserCacheDir()
		if err != nil {
			slog.Warn("no image cache on disk", "error", err)
			return ""
		}
		dir = filepath.Join(d, "airlock.space")
	}
	dir = filepath.Join(dir, "images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Warn("no image cache on disk", "error", err)
		return ""
	}
	return dir
}

var dateRegexp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// dayVersion is the on-disk format. Bump it and every stored day is ignored.
const dayVersion = 2

// day stores entry metadata and image bookkeeping. Version 2 invalidates
// legacy API records, including days that cached NASA's logo as the image.
type day struct {
	Version   int                 `json:"version"`
	CachedAt  time.Time           `json:"cached_at"`
	HasImage  bool                `json:"has_image"`
	ImageSize image.Point         `json:"image_size,omitzero"`
	APOD      *apodclient.Picture `json:"apod"`
}

// cacheFile names one of a day's files. The date is NASA's, so it is checked
// before it becomes a path.
func cacheFile(date, ext string) string {
	if cacheDir == "" || !dateRegexp.MatchString(date) {
		return ""
	}
	return filepath.Join(cacheDir, date+"."+ext)
}

// loadDay reads a stored day. Anything wrong with it - missing, truncated,
// written by an older format - is simply a miss.
func loadDay(date string) *day {
	path := cacheFile(date, "json")
	if path == "" {
		return nil
	}
	byt, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var d day
	if err := json.Unmarshal(byt, &d); err != nil || d.Version != dayVersion || d.APOD == nil || d.APOD.Date.Format(time.DateOnly) != date {
		return nil
	}
	return &d
}

// store records the day, and the image bytes when it has any. The image is
// written first: the record is the index, so it must never promise a file
// that is not there yet.
func (a *APOD) store(body []byte) {
	path := cacheFile(a.Date.Format(time.DateOnly), "json")
	if path == "" {
		return
	}
	if len(body) > 0 && !writeFile(cacheFile(a.Date.Format(time.DateOnly), "img"), body) {
		return
	}
	rec, err := json.Marshal(day{
		Version:   dayVersion,
		CachedAt:  time.Now().UTC(),
		HasImage:  len(body) > 0,
		ImageSize: a.ImageSize,
		APOD:      &a.Picture,
	})
	if err != nil {
		slog.Warn("could not encode cached day", "error", err)
		return
	}
	writeFile(path, rec)
}

// writeFile replaces path atomically, so a crash cannot leave a half-written
// file for the next run to trust.
func writeFile(path string, byt []byte) bool {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, byt, 0o600); err != nil {
		slog.Warn("could not write cache file", "path", path, "error", err)
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Warn("could not write cache file", "path", path, "error", err)
		os.Remove(tmp)
		return false
	}
	return true
}

func (a *APOD) getImageBytes(ctx context.Context) ([]byte, error) {
	if rec := loadDay(a.Date.Format(time.DateOnly)); rec != nil {
		if !rec.HasImage {
			return nil, nil
		}
		if body, err := os.ReadFile(cacheFile(a.Date.Format(time.DateOnly), "img")); err == nil && rec.ImageSize != (image.Point{}) {
			a.setSize(rec.ImageSize)
			return body, nil
		}
	}

	if a.ImageURL == "" {
		a.store(nil)
		return nil, nil
	}

	body, err := fetchImage(ctx, a.ImageURL, a.setSize)
	if errors.Is(err, errNotAnImage) {
		a.store(nil)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.ImageSize == (image.Point{}) {
		return nil, errors.New("decoding image config: unrecognised image header")
	}
	a.store(body)
	return body, nil
}

// imageClient is isolated from http.DefaultClient so other packages cannot
// change our pool.
var imageClient = newImageClient()

// ponytail: 32 in-flight images; reduce this if the image host or box complains.
var imageSlots = make(chan struct{}, 32)

func newImageClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 4
	return &http.Client{Transport: t}
}

// fetchImage downloads an image, calling onSize with its dimensions as soon as
// the header has arrived, so a layout can be fitted to it while the rest of
// the bytes are still on their way.
func fetchImage(ctx context.Context, url string, onSize func(image.Point)) ([]byte, error) {
	select {
	case imageSlots <- struct{}{}:
		defer func() { <-imageSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := imageClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer resp.Body.Close()
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading image: status %d", resp.StatusCode)
	}
	// video days serve an mp4/youtube page; don't download megabytes just to fail decoding
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return nil, fmt.Errorf("%w: content-type %q", errNotAnImage, ct)
	}

	// DecodeConfig reads only as far as the dimensions; the tee keeps those
	// bytes for the body. A failure here is a header this decoder does not
	// recognise, or a read error the rest of the copy will report.
	var body bytes.Buffer
	if cfg, _, err := image.DecodeConfig(io.TeeReader(resp.Body, &body)); err == nil {
		onSize(image.Point{X: cfg.Width, Y: cfg.Height})
	}
	if _, err := body.ReadFrom(resp.Body); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("reading image body: %w", err)
	}
	return body.Bytes(), nil
}

// setSize records the picture's size once; a retried download is the same
// picture, and readers rely on the value not moving after sized is closed.
func (a *APOD) setSize(p image.Point) {
	a.sizeOnce.Do(func() {
		a.ImageSize = p
		if a.sized != nil {
			close(a.sized)
		}
	})
}

// WaitImageSize blocks until the picture's size is known, which is usually
// long before ImageBytes resolves, and returns it. It returns the zero size
// when the day turns out to have no image or the download fails.
func (a *APOD) WaitImageSize() image.Point {
	done := make(chan struct{})
	go func() {
		a.ImageBytes() // coalesces with the caller already fetching it
		close(done)
	}()
	select {
	case <-a.sized:
	case <-done:
	}
	return a.ImageSize
}

// Decode decodes the compressed image. The result is intentionally not cached;
// hold it only as long as needed.
func (a *APOD) Decode() (image.Image, error) {
	byt, err := a.ImageBytes()
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(byt))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}
	return img, nil
}
