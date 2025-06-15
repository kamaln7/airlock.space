package apod

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kamaln7/resolvable"
	"github.com/peteretelej/nasa"
)

var Today = resolvable.New(
	(&apod{}).getAPOD,
	resolvable.WithRetry(),
	resolvable.WithGraceful(),
	resolvable.WithCacheTTL(time.Minute),
).WithBackgroundContext()

type APOD struct {
	*nasa.Image

	ImageBytes   resolvable.V[[]byte]
	ImageDecoded resolvable.V[image.Image]
}

type apod struct {
	lastAPOD    *APOD
	lastAPODDay time.Time
}

func (n *apod) getAPOD(_ context.Context) (*APOD, error) {
	if n.lastAPODDay == today() {
		time.Sleep(time.Second * 2)
		return n.lastAPOD, nil
	}

	slog.Info("fetching APOD", "day", today())
	apod, err := nasa.APODToday()
	if err != nil {
		return nil, err
	}
	n.lastAPOD = newAPOD(apod)
	n.lastAPODDay = today()
	return n.lastAPOD, nil
}

func newAPOD(apod *nasa.Image) *APOD {
	a := &APOD{
		Image: apod,
	}
	a.ImageBytes = resolvable.New(a.getImageBytes,
		resolvable.WithRetry(),
		resolvable.WithGraceful(),
	).WithBackgroundContext()
	a.ImageDecoded = resolvable.New(a.getImageDecoded,
		resolvable.WithRetry(),
		resolvable.WithGraceful(),
	).WithBackgroundContext()
	return a
}

func (a *APOD) getImageBytes(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	var url string
	if a.URL != "" {
		url = a.URL
	} else if a.HDURL != "" {
		url = a.HDURL
	} else {
		return nil, fmt.Errorf("no image URL found")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading image body: %w", err)
	}

	return body, nil
}

func (a *APOD) getImageDecoded(ctx context.Context) (image.Image, error) {
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

func today() time.Time {
	return time.Now().Truncate(time.Hour * 24)
}
