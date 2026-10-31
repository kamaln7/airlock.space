// Package apod fetches pictures from NASA's APOD Basic JSON API,
// as documented in
// https://api.nasa.gov/assets/files/apod/APOD-APIs-and-RSS-info.pdf.
//
// The API needs no credentials.
package apod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://science.nasa.gov/wp-json/wp/v2"

	// defaultMaxResponseBytes is the default cap on JSON metadata reads.
	defaultMaxResponseBytes = 2 << 20
	// maxErrorBytes bounds how much of a non-200 response is read for the
	// error message.
	maxErrorBytes = 1 << 10
)

// firstAPOD is the date APOD began.
var firstAPOD = time.Date(1995, time.June, 16, 0, 0, 0, 0, time.UTC)

// Picture is one Astronomy Picture of the Day.
type Picture struct {
	// Date is the APOD date, at midnight UTC.
	Date time.Time
	// Title is the plain-text title.
	Title string
	// ArticleURL is the APOD post on science.nasa.gov.
	ArticleURL string
	// ImageURL is the full-size featured image URL. It is empty when the
	// picture has no featured image. Videos may still have one, as a poster.
	ImageURL string
	// ImageSize is the featured image's size in pixels, as NASA's image
	// service states it in ImageURL's w and h parameters; the zero value
	// when the URL does not say.
	ImageSize image.Point
	// ExplanationHTML is the explanation exactly as published, including
	// its HTML markup.
	ExplanationHTML string
}

// Opts configures a Client.
type Opts func(*Client)

// WithHTTPClient sets the HTTP client. Nil leaves the current client unchanged.
// Request timeouts belong to this client or the caller's context.
func WithHTTPClient(httpClient *http.Client) Opts {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithBaseURL sets the API root, ignoring trailing slashes.
// An empty root leaves the current root unchanged.
func WithBaseURL(baseURL string) Opts {
	baseURL = strings.TrimRight(baseURL, "/")
	return func(c *Client) {
		if baseURL != "" {
			c.baseURL = baseURL
		}
	}
}

// WithMaxResponseBytes limits bytes read from the JSON response body.
// Zero disables limiting; negative values leave the current limit unchanged.
// The default is 2 MiB.
// This does not limit image downloads.
func WithMaxResponseBytes(maxResponseBytes int64) Opts {
	return func(c *Client) {
		if maxResponseBytes >= 0 {
			c.maxResponseBytes = maxResponseBytes
		}
	}
}

// Client fetches APOD pictures. Create one with NewClient.
type Client struct {
	httpClient       *http.Client
	baseURL          string
	maxResponseBytes int64
}

// NewClient creates a Client with defaults, then applies opts in order.
// It does not impose request timeouts.
func NewClient(opts ...Opts) *Client {
	c := &Client{
		httpClient:       http.DefaultClient,
		baseURL:          defaultBaseURL,
		maxResponseBytes: defaultMaxResponseBytes,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// wirePicture holds the APOD Basic JSON fields this package uses.
type wirePicture struct {
	Date        string `json:"date"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	HDURL       string `json:"hdurl"`
	Explanation string `json:"explanation"`
}

// Latest returns the most recent published APOD.
func (c *Client) Latest(ctx context.Context) (*Picture, error) {
	var list []wirePicture
	if err := c.getJSON(ctx, "/apod-basic?per_page=1", &list); err != nil {
		return nil, err
	}

	if len(list) == 0 {
		return nil, errors.New("latest APOD response is empty")
	}

	return list[0].entry()
}

// ByDate returns the APOD published on the calendar day of date, taken in
// date's own location. Dates before the first APOD on 1995-06-16 are rejected.
func (c *Client) ByDate(ctx context.Context, date time.Time) (*Picture, error) {
	year, month, day := date.Date()
	want := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if want.Before(firstAPOD) {
		return nil, fmt.Errorf("date %s is before the first APOD on %s", want.Format(time.DateOnly), firstAPOD.Format(time.DateOnly))
	}

	var wire wirePicture
	if err := c.getJSON(ctx, "/apod-basic/"+want.Format("060102"), &wire); err != nil {
		return nil, err
	}

	entry, err := wire.entry()
	if err != nil {
		return nil, err
	}
	if !entry.Date.Equal(want) {
		return nil, fmt.Errorf("requested APOD for %s but response is for %s", want.Format(time.DateOnly), entry.Date.Format(time.DateOnly))
	}
	return entry, nil
}

func (w *wirePicture) entry() (*Picture, error) {
	date, err := time.Parse(time.DateOnly, w.Date)
	if err != nil {
		return nil, err
	}
	return &Picture{
		Date:            date,
		Title:           w.Title,
		ArticleURL:      w.URL,
		ImageURL:        w.HDURL,
		ImageSize:       urlImageSize(w.HDURL),
		ExplanationHTML: w.Explanation,
	}, nil
}

// urlImageSize reads the size NASA's image service puts in its URLs
// (".../x_2752.jpg?w=2752&h=2208&fit=clip"). Undocumented, so anything
// missing or malformed is simply not a size.
func urlImageSize(raw string) image.Point {
	u, err := url.Parse(raw)
	if err != nil {
		return image.Point{}
	}
	q := u.Query()
	w, errW := strconv.Atoi(q.Get("w"))
	h, errH := strconv.Atoi(q.Get("h"))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return image.Point{}
	}
	return image.Point{X: w, Y: h}
}

// getJSON decodes one JSON response directly from the HTTP body.
func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	// Fetch-backed clients need the deadline on the request context.
	if timeout := c.httpClient.Timeout; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
		defer stop()
	}

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if msg := strings.TrimSpace(string(snippet)); msg != "" {
			return fmt.Errorf("unexpected HTTP status %s: %s", resp.Status, msg)
		}
		return fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	var body io.Reader = resp.Body
	if lim := c.maxResponseBytes; lim > 0 {
		body = io.LimitReader(resp.Body, lim)
	}
	err = json.NewDecoder(body).Decode(dst)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
