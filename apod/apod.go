package apod

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // decoders for whatever NASA posts: registering them here is
	_ "image/jpeg" // this package's business, not a dependency's to do by accident
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kamaln7/resolvable/v2"
	"github.com/peteretelej/nasa"
)

var Today = resolvable.New(
	(&apod{}).getAPOD,
	resolvable.Retry(3, nil),
	resolvable.StaleOnError(),
	resolvable.CacheFor(time.Minute),
).WithBackgroundContext()

type APOD struct {
	*nasa.Image

	// ImageBytes is the compressed source image (jpeg/png), or nil on the days
	// NASA posts a video instead. That absence is a value, not an error, so it
	// caches: an error would send every new session through the fetch again.
	// Decode on demand and discard: a decoded APOD is tens of MB, the box has 512.
	ImageBytes resolvable.V[[]byte]
	// PNGBytes is a PNG transcode for the kitty graphics protocol, only
	// materialized when a kitty-capable client connects.
	PNGBytes resolvable.V[[]byte]

	// ImageSize is set once ImageBytes has resolved.
	ImageSize image.Point
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

	img, err := nasa.ApodImage(t) // ponytail: concurrent misses fetch twice, then agree
	if err != nil {
		return nil, redactAPIKey(err)
	}

	days.Lock()
	defer days.Unlock()
	if existing, ok := days.byDate[key]; ok {
		return existing, nil // lost the race; one instance per day
	}
	a = newAPOD(img)
	days.byDate[key] = a
	days.order = append(days.order, key)
	for len(days.order) > daysInMemory {
		delete(days.byDate, days.order[0])
		days.order = days.order[1:]
	}
	return a, nil
}

type apod struct {
	last *APOD
}

func (n *apod) getAPOD(_ context.Context) (*APOD, error) {
	// nasa.APODToday caches by day itself; we only wrap the result
	var img *nasa.Image
	var err error
	if d := os.Getenv("APOD_DATE"); d != "" { // e.g. APOD_DATE=2026-08-29, for testing
		if n.last != nil && n.last.Date == d {
			// nasa.ApodImage has no cache (unlike APODToday); skip the refetch
			return n.last, nil
		}
		var t time.Time
		if t, err = time.Parse(time.DateOnly, d); err != nil {
			return nil, fmt.Errorf("invalid APOD_DATE: %w", err)
		}
		img, err = nasa.ApodImage(t)
	} else {
		img, err = nasa.APODToday()
	}
	if err != nil {
		return nil, redactAPIKey(err)
	}
	if n.last == nil || n.last.Date != img.Date {
		slog.Info("new APOD", "date", img.Date)
		n.last = newAPOD(img)
	}
	return n.last, nil
}

// api keys must never end up in logs
var apiKeyRegexp = regexp.MustCompile(`api_key=[^&"'\s]+`)

func redactAPIKey(err error) error {
	return errors.New(apiKeyRegexp.ReplaceAllString(err.Error(), "api_key=REDACTED"))
}

func newAPOD(img *nasa.Image) *APOD {
	a := &APOD{Image: img}
	a.ImageBytes = resolvable.New(a.getImageBytes,
		resolvable.Retry(3, nil),
		resolvable.CacheForever(),
	).WithBackgroundContext()
	a.PNGBytes = resolvable.New(a.getPNGBytes,
		resolvable.CacheForever(),
	).WithBackgroundContext()
	return a
}

var youtubeIDRegexp = regexp.MustCompile(`(?:youtube\.com/(?:watch\?v=|embed/)|youtu\.be/)([\w-]{11})`)

// imageURLs returns candidate URLs to try in order. Video days on youtube get
// their thumbnail; directly-hosted videos (mp4) have no image and will fail.
func (a *APOD) imageURLs() []string {
	if m := youtubeIDRegexp.FindStringSubmatch(a.URL + " " + a.HDURL); m != nil {
		return []string{
			fmt.Sprintf("https://img.youtube.com/vi/%s/maxresdefault.jpg", m[1]),
			fmt.Sprintf("https://img.youtube.com/vi/%s/hqdefault.jpg", m[1]),
		}
	}
	var urls []string
	if a.URL != "" {
		urls = append(urls, a.URL)
	}
	if a.HDURL != "" {
		urls = append(urls, a.HDURL)
	}
	return urls
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

// cachePath is the on-disk image for this APOD, or "" when it cannot be
// cached. The date is NASA's, so it is checked before it becomes a path.
func (a *APOD) cachePath() string {
	if cacheDir == "" || !dateRegexp.MatchString(a.Date) {
		return ""
	}
	return filepath.Join(cacheDir, a.Date+".img")
}

// cachedImage returns the stored image, if any. A zero-length file records a
// day that has no image at all, which is worth remembering too.
func (a *APOD) cachedImage() ([]byte, bool) {
	path := a.cachePath()
	if path == "" {
		return nil, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if len(body) == 0 {
		return nil, true
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		os.Remove(path) // truncated or corrupt; fetch it again
		return nil, false
	}
	a.ImageSize = image.Point{X: cfg.Width, Y: cfg.Height}
	return body, true
}

func (a *APOD) storeImage(body []byte) {
	path := a.cachePath()
	if path == "" {
		return
	}
	// write then rename, so a crash cannot leave a half image behind
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		slog.Warn("could not cache image", "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Warn("could not cache image", "error", err)
		os.Remove(tmp)
	}
}

func (a *APOD) getImageBytes(ctx context.Context) ([]byte, error) {
	if body, ok := a.cachedImage(); ok {
		return body, nil
	}

	urls := a.imageURLs()
	if len(urls) == 0 {
		a.storeImage(nil)
		return nil, nil
	}

	var lastErr error
	for _, url := range urls {
		body, err := fetchImage(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil {
			lastErr = fmt.Errorf("decoding image config: %w", err)
			continue
		}
		a.ImageSize = image.Point{X: cfg.Width, Y: cfg.Height}
		a.storeImage(body)
		return body, nil
	}
	if errors.Is(lastErr, errNotAnImage) {
		a.storeImage(nil)
		return nil, nil
	}
	return nil, lastErr
}

func fetchImage(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading image: status %d", resp.StatusCode)
	}
	// video days serve an mp4/youtube page; don't download megabytes just to fail decoding
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return nil, fmt.Errorf("%w: content-type %q", errNotAnImage, ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading image body: %w", err)
	}
	return body, nil
}

func (a *APOD) getPNGBytes(_ context.Context) ([]byte, error) {
	byt, err := a.ImageBytes()
	if err != nil {
		return nil, err
	}
	if len(byt) == 0 {
		return nil, errNotAnImage
	}
	if http.DetectContentType(byt) == "image/png" {
		return byt, nil
	}
	img, _, err := image.Decode(bytes.NewReader(byt))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding png: %w", err)
	}
	return buf.Bytes(), nil
}

// Link is the APOD page for this image on nasa.gov.
func (a *APOD) Link() string {
	return fmt.Sprintf("https://apod.nasa.gov/apod/ap%s.html", a.ApodDate.Format("060102"))
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
