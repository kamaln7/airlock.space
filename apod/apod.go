package apod

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
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
func (a *APOD) getImageBytes(ctx context.Context) ([]byte, error) {
	urls := a.imageURLs()
	if len(urls) == 0 {
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
		return body, nil
	}
	if errors.Is(lastErr, errNotAnImage) {
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
