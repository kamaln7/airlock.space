package apod

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/peteretelej/nasa"
)

// NewClientFromEnv builds a NASA client from NASA_API_KEY or NASA_API_KEY_PATH
// and installs it for Today/ByDate. Empty both: DEMO_KEY.
func NewClientFromEnv() error {
	key, err := apiKeyFromEnv()
	if err != nil {
		return err
	}
	if key == "" {
		key = "DEMO_KEY"
	}
	live.nasa = nasa.NewClient(nasa.WithAPIKey(key))
	return nil
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

// fetch loads one APOD. Zero t is today. Call NewClientFromEnv first.
func (n *apod) fetch(ctx context.Context, t time.Time) (*nasa.Image, error) {
	if n.nasa == nil {
		return nil, errors.New("nasa client not configured")
	}
	var (
		img *nasa.APODImage
		err error
	)
	if t.IsZero() {
		img, err = n.nasa.APOD.Today(ctx)
	} else {
		img, err = n.nasa.APOD.Get(ctx, t)
	}
	if err != nil {
		return nil, redactAPIKey(err)
	}
	return toImage(img), nil
}
