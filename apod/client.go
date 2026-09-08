package apod

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/peteretelej/nasa"
)

// NewClientFromEnv builds a NASA client from NASA_API_KEY or NASA_API_KEY_PATH.
// Empty both: DEMO_KEY.
func NewClientFromEnv() (*nasa.Client, error) {
	key, err := apiKeyFromEnv()
	if err != nil {
		return nil, err
	}
	if key == "" {
		key = "DEMO_KEY"
	}
	return nasa.NewClient(nasa.WithAPIKey(key)), nil
}

func apiKeyFromEnv() (string, error) {
	if k := os.Getenv("NASA_API_KEY"); k != "" {
		return k, nil
	}
	path := os.Getenv("NASA_API_KEY_PATH")
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading NASA_API_KEY_PATH: %w", err)
	}
	if key := strings.TrimSpace(string(b)); key != "" {
		return key, nil
	}
	return "", nil
}

func (n *apod) client() (*nasa.Client, error) {
	if n.nasa != nil {
		return n.nasa, nil
	}
	c, err := NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	n.nasa = c
	return c, nil
}

func toImage(a *nasa.APODImage) *nasa.Image {
	return &nasa.Image{
		Date:        a.Date.Format(time.DateOnly),
		Title:       a.Title,
		URL:         a.URL,
		HDURL:       a.HDURL,
		Explanation: a.Explanation,
		ApodDate:    a.Date,
	}
}

func (n *apod) fetchToday(ctx context.Context) (*nasa.Image, error) {
	c, err := n.client()
	if err != nil {
		return nil, err
	}
	img, err := c.APOD.Today(ctx)
	if err != nil {
		return nil, redactAPIKey(err)
	}
	return toImage(img), nil
}

func (n *apod) fetchDate(ctx context.Context, t time.Time) (*nasa.Image, error) {
	c, err := n.client()
	if err != nil {
		return nil, err
	}
	img, err := c.APOD.Get(ctx, t)
	if err != nil {
		return nil, redactAPIKey(err)
	}
	return toImage(img), nil
}
