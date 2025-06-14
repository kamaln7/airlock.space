package apod

import (
	"context"
	"log/slog"
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

type apod struct {
	lastAPOD    *nasa.Image
	lastAPODDay time.Time
}

func (n *apod) getAPOD(_ context.Context) (*nasa.Image, error) {
	if n.lastAPODDay == today() {
		time.Sleep(time.Second * 2)
		return n.lastAPOD, nil
	}

	slog.Info("fetching APOD", "day", today())
	apod, err := nasa.APODToday()
	if err != nil {
		return nil, err
	}
	n.lastAPOD = apod
	n.lastAPODDay = today()
	return n.lastAPOD, nil
}

func today() time.Time {
	return time.Now().Truncate(time.Hour * 24)
}
