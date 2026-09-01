package airlockspace

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/peteretelej/nasa"
)

// the v2 renderer, the declarative View and the terminal negotiation only meet
// inside a running program: unit tests on View() would have passed all through
// the migration while the real thing never painted. This boots one.
func TestProgramPaintsAndQuits(t *testing.T) {
	in, keys := io.Pipe()
	var out strings.Builder
	m := &Model{KittyGraphics: false, Session: io.Discard}
	p := tea.NewProgram(m,
		tea.WithInput(in),
		tea.WithOutput(&out),
		tea.WithWindowSize(100, 40),
		tea.WithColorProfile(colorprofile.TrueColor),
		tea.WithoutSignalHandler(),
	)

	go func() {
		// a day of our own, so the test never waits on api.nasa.gov
		a := &apod.APOD{Image: &nasa.Image{
			Title:       "A Test Nebula",
			Explanation: "some words about space.",
			ApodDate:    time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		}}
		// a video day, resolved without leaving the process
		a.ImageBytes = func() ([]byte, error) { return nil, nil }
		p.Send(apodMsg{apod: a})
		time.Sleep(200 * time.Millisecond)
		keys.Write([]byte("q"))
	}()

	if _, err := p.Run(); err != nil {
		t.Fatalf("program exited with %v", err)
	}

	frame := out.String()
	for _, want := range []struct{ what, seq string }{
		{"alt screen", "\x1b[?1049h"},
		{"mouse all-motion tracking", "\x1b[?1003h"},
		{"the title", "A Test Nebula"},
		{"the explanation", "some words about space."},
		{"the help bar", "quit"},
		{"the link", "apod.nasa.gov"},
	} {
		if !strings.Contains(frame, want.seq) {
			t.Errorf("frame has no %s (%q)", want.what, want.seq)
		}
	}
	if !strings.Contains(frame, "\x1b[?1049l") {
		t.Error("never left the alt screen on quit")
	}
}
