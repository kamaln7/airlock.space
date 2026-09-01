package apod

import (
	"errors"
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
