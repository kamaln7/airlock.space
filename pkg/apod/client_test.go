package apod

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const entryJSON = `{
	"date": "2014-09-18",
	"title": "Cocoon Nebula Wide Field",
	"explanation": "<strong>Explanation:</strong> <a href=\"http://example.com/\">A nebula</a> & more.",
	"url": "https://science.nasa.gov/image-article/apod-2014-september-18-cocoon-nebula-wide-field/",
	"hdurl": "https://assets.science.nasa.gov/cocoon.jpg?w=3000",
	"basic_html": "<html>discarded</html>"
}`

func wantEntry(t *testing.T, got *Picture) {
	t.Helper()
	if got == nil {
		t.Fatal("successful fetch returned nil entry")
	}
	want := Picture{
		Date:            time.Date(2014, time.September, 18, 0, 0, 0, 0, time.UTC),
		Title:           "Cocoon Nebula Wide Field",
		ArticleURL:      "https://science.nasa.gov/image-article/apod-2014-september-18-cocoon-nebula-wide-field/",
		ImageURL:        "https://assets.science.nasa.gov/cocoon.jpg?w=3000",
		ExplanationHTML: `<strong>Explanation:</strong> <a href="http://example.com/">A nebula</a> & more.`,
	}
	if *got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

func serve(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(WithHTTPClient(srv.Client()), WithBaseURL(srv.URL+"/wp-json/wp/v2/"))
}

func TestLatest(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wp-json/wp/v2/apod-basic" || r.URL.RawQuery != "per_page=1" {
			t.Errorf("request = %s, want /wp-json/wp/v2/apod-basic?per_page=1", r.URL)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Write([]byte("[" + entryJSON + "]"))
	})
	got, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantEntry(t, got)
}

func TestByDate(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wp-json/wp/v2/apod-basic/140918" || r.URL.RawQuery != "" {
			t.Errorf("request = %s, want /wp-json/wp/v2/apod-basic/140918", r.URL)
		}
		w.Write([]byte(entryJSON))
	})
	// A non-UTC location still selects its own calendar day.
	loc := time.FixedZone("UTC-10", -10*3600)
	got, err := c.ByDate(context.Background(), time.Date(2014, time.September, 18, 23, 0, 0, 0, loc))
	if err != nil {
		t.Fatal(err)
	}
	wantEntry(t, got)
}

func TestByDateInvalidDates(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	})
	for name, date := range map[string]time.Time{
		"zero":              {},
		"before first APOD": time.Date(1995, time.June, 15, 0, 0, 0, 0, time.UTC),
	} {
		if got, err := c.ByDate(context.Background(), date); err == nil || got != nil {
			t.Errorf("%s: got (%+v, %v); want nil entry and error", name, got, err)
		}
	}
}

func TestByDateMismatch(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(entryJSON))
	})
	if got, err := c.ByDate(context.Background(), time.Date(2014, time.September, 19, 0, 0, 0, 0, time.UTC)); err == nil || got != nil {
		t.Errorf("mismatched date: got (%+v, %v); want nil entry and error", got, err)
	}
}

func TestBadResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		list bool
	}{
		{"latest empty list", `[]`, true},
		{"latest not a list", `{"date":"2014-09-18"}`, true},
		{"latest missing date", `[{"title":"t"}]`, true},
		{"latest invalid date", `[{"date":"18/09/2014"}]`, true},
		{"latest malformed JSON", `[{"date":`, true},
		{"by date not an object", `[]`, false},
		{"by date missing date", `{"title":"t"}`, false},
		{"by date invalid date", `{"date":"2014-13-40"}`, false},
		{"by date malformed JSON", `{`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tt.body)) })
			var err error
			var got *Picture
			if tt.list {
				got, err = c.Latest(context.Background())
			} else {
				got, err = c.ByDate(context.Background(), time.Date(2014, time.September, 18, 0, 0, 0, 0, time.UTC))
			}
			if err == nil || got != nil {
				t.Errorf("bad response: got (%+v, %v); want nil entry and error", got, err)
			}
		})
	}
}

func TestHTTPStatusError(t *testing.T) {
	huge := strings.Repeat("x", 100<<10)
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":"rest_invalid_param"}` + huge))
	})
	got, err := c.ByDate(context.Background(), time.Date(2014, time.September, 18, 0, 0, 0, 0, time.UTC))
	if err == nil || got != nil {
		t.Fatalf("HTTP failure: got (%+v, %v); want nil entry and error", got, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "rest_invalid_param") {
		t.Errorf("err = %q, want status and body", msg)
	}
	if len(msg) > 2<<10 {
		t.Errorf("error is %d bytes, want bounded", len(msg))
	}
}

func TestOversizedResponse(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"date":"2014-09-18","basic_html":"`))
		w.Write([]byte(strings.Repeat("a", defaultMaxResponseBytes)))
		w.Write([]byte(`"}`))
	})
	if got, err := c.ByDate(context.Background(), time.Date(2014, time.September, 18, 0, 0, 0, 0, time.UTC)); err == nil || got != nil {
		t.Errorf("oversized response: got (%+v, %v); want nil entry and error", got, err)
	}
}

func TestResponseLimit(t *testing.T) {
	largeBody := strings.ReplaceAll(entryJSON, "discarded", strings.Repeat("x", defaultMaxResponseBytes))
	for _, tt := range []struct {
		name    string
		body    string
		limit   int64
		wantErr bool
	}{
		{"over limit", entryJSON, int64(len(entryJSON)) - 1, true},
		{"exact limit", entryJSON, int64(len(entryJSON)), false},
		{"exact limit with trailing whitespace", entryJSON + " \r\n\t", int64(len(entryJSON)) + 4, false},
		{"raised limit", largeBody, int64(len(largeBody)), false},
		{"unlimited", largeBody, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := NewClient(
				WithHTTPClient(srv.Client()),
				WithBaseURL(srv.URL),
				WithMaxResponseBytes(tt.limit),
			)
			got, err := c.ByDate(context.Background(), time.Date(2014, 9, 18, 0, 0, 0, 0, time.UTC))
			if tt.wantErr {
				if err == nil || got != nil {
					t.Fatalf("got (%+v, %v); want nil picture and error", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantEntry(t, got)
		})
	}
}

func TestCancellation(t *testing.T) {
	release := make(chan struct{})
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	got, err := c.Latest(ctx)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Errorf("got (%+v, %v); want nil entry and context.Canceled", got, err)
	}
}

func TestAlreadyCancelledContext(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[" + entryJSON + "]")) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := c.Latest(ctx); !errors.Is(err, context.Canceled) || got != nil {
		t.Errorf("got (%+v, %v); want nil entry and context.Canceled", got, err)
	}
}

func TestHTTPClientTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	httpClient := srv.Client()
	httpClient.Timeout = 20 * time.Millisecond
	c := NewClient(WithHTTPClient(httpClient), WithBaseURL(srv.URL))
	picture, err := c.Latest(context.Background())
	if picture != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got (%+v, %v); want nil picture and timeout", picture, err)
	}
}

type streamingTransport struct{ body io.ReadCloser }

func (s streamingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: s.body, Header: make(http.Header)}, nil
}

func TestCancellationWhileReadingBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	client := NewClient(WithHTTPClient(&http.Client{Transport: streamingTransport{reader}}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Latest(ctx)
		done <- err
	}()
	// A partial JSON value ensures cancellation happens during a body read.
	if _, err := writer.Write([]byte("[")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request stayed blocked reading the body after cancellation")
	}
}

func TestOptionOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(entryJSON))
	}))
	defer srv.Close()
	for _, tt := range []struct {
		name    string
		limits  []int64
		wantErr bool
	}{
		{"raise limit", []int64{128, int64(len(entryJSON))}, false},
		{"lower limit", []int64{int64(len(entryJSON)), 128}, true},
		{"disable limit", []int64{128, 0}, false},
		{"re-enable limit", []int64{0, 128}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := []Opts{WithHTTPClient(srv.Client()), WithBaseURL(srv.URL)}
			for _, limit := range tt.limits {
				opts = append(opts, WithMaxResponseBytes(limit))
			}
			c := NewClient(opts...)
			picture, err := c.ByDate(context.Background(), time.Date(2014, 9, 18, 0, 0, 0, 0, time.UTC))
			if tt.wantErr {
				if picture != nil || err == nil {
					t.Fatalf("got (%+v, %v); want nil picture and error", picture, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantEntry(t, picture)
		})
	}
}
