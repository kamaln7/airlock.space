package airlockspace

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/peteretelej/nasa"
)

func TestFitImage(t *testing.T) {
	for _, tc := range []struct {
		imgW, imgH, boxW, boxH, wantW, wantH int
	}{
		{100, 50, 50, 50, 50, 25},    // wide image, width-bound
		{50, 100, 50, 50, 25, 50},    // tall image, height-bound
		{10, 10, 100, 100, 100, 100}, // upscale
		{0, 10, 50, 50, 0, 0},        // degenerate input
	} {
		gotW, gotH := fitImage(tc.imgW, tc.imgH, tc.boxW, tc.boxH)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("fitImage(%d,%d,%d,%d) = %d,%d; want %d,%d",
				tc.imgW, tc.imgH, tc.boxW, tc.boxH, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}

// on a video day (no image) e must not toggle into a view that can't exist,
// and the footer must not offer it
func TestVideoDayNoImageToggle(t *testing.T) {
	m := &Model{Style: lipgloss.NewStyle(), apod: &apod.APOD{Image: &nasa.Image{}}}
	m.Init()
	m.Update(imageMsg{ok: false})
	before := m.imgOrExplanation
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.imgOrExplanation != before {
		t.Error("e toggled with no image to toggle to")
	}
	if m.showingImage() {
		t.Error("showingImage with no image")
	}
	for _, k := range m.helpKeys() {
		if h := k.Help(); h.Key == "e" || h.Key == "f" {
			t.Errorf("footer offers %q with no image", h.Key)
		}
	}
}
