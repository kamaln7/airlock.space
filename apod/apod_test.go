package apod

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/peteretelej/nasa"
)

func TestRedactAPIKey(t *testing.T) {
	err := errors.New(`Get "https://api.nasa.gov/planetary/apod?api_key=HZ33hRbsecret123": context deadline exceeded`)
	got := redactAPIKey(err).Error()
	if strings.Contains(got, "secret123") {
		t.Fatalf("api key leaked: %s", got)
	}
	if !strings.Contains(got, "api_key=REDACTED") {
		t.Fatalf("expected redaction marker: %s", got)
	}
}

// a video day is an absence, not a failure: it must resolve to nil bytes so it
// caches, instead of sending every new session back through the fetch
func TestVideoDayResolvesToNilBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("not an image"))
	}))
	defer srv.Close()

	a := newAPOD(&nasa.Image{URL: srv.URL + "/apod.mp4"})
	for range 2 {
		byt, err := a.ImageBytes()
		if err != nil || byt != nil {
			t.Fatalf("ImageBytes() = %v, %v; want nil, nil", byt, err)
		}
	}
}

func TestImageCacheSurvivesANewAPODInstance(t *testing.T) {
	cacheDir = t.TempDir()
	defer func() { cacheDir = imageCacheDir() }()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write(onePixelPNG(t))
	}))
	defer srv.Close()

	img := &nasa.Image{Date: "2026-08-30", URL: srv.URL + "/apod.png"}
	first, err := newAPOD(img).ImageBytes()
	if err != nil || len(first) == 0 {
		t.Fatalf("ImageBytes() = %d bytes, %v", len(first), err)
	}

	// a fresh instance stands in for the day being evicted from memory, or the
	// process restarting: the bytes must come off disk, not the network
	again := newAPOD(img)
	second, err := again.ImageBytes()
	if err != nil || !bytes.Equal(second, first) {
		t.Fatalf("cached ImageBytes() = %d bytes, %v; want the same %d", len(second), err, len(first))
	}
	if hits != 1 {
		t.Errorf("fetched %d times; want 1", hits)
	}
	if again.ImageSize.X != 1 || again.ImageSize.Y != 1 {
		t.Errorf("ImageSize = %v; want 1x1 from the cached bytes", again.ImageSize)
	}
}

func TestVideoDayIsRecordedWithoutAnImageFile(t *testing.T) {
	cacheDir = t.TempDir()
	defer func() { cacheDir = imageCacheDir() }()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "video/mp4")
	}))
	defer srv.Close()

	img := &nasa.Image{Date: "2026-08-31", URL: srv.URL + "/apod.mp4"}
	for range 2 {
		if byt, err := newAPOD(img).ImageBytes(); err != nil || byt != nil {
			t.Fatalf("ImageBytes() = %v, %v; want nil, nil", byt, err)
		}
	}
	if hits != 1 {
		t.Errorf("fetched %d times; want 1", hits)
	}
}

func TestCacheFileRejectsADateThatIsNotOne(t *testing.T) {
	cacheDir = t.TempDir()
	defer func() { cacheDir = imageCacheDir() }()

	if got := cacheFile("../../etc/passwd", "json"); got != "" {
		t.Errorf("cacheFile() = %q; want empty", got)
	}
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
