package airlockspace

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/muesli/termenv"
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

// an explanation taller than the viewport scrolls off the top; hit-testing
// must follow the rows actually on screen, or hover/click land on the wrong row
func TestHitTestWhenFrameOverflows(t *testing.T) {
	m := &Model{Width: 80, Height: 24, Style: lipgloss.NewStyle(),
		apod: &apod.APOD{Image: &nasa.Image{Title: "A Test Nebula",
			Explanation: strings.Repeat("some words about space. ", 60),
			ApodDate:    time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)}}}
	m.State = StateAPOD

	lines := strings.Split(m.baseView(), "\n")
	if len(lines) <= m.Height {
		t.Fatal("fixture no longer overflows the viewport")
	}
	link := m.apod.Link()
	frameRow := slices.IndexFunc(lines, func(l string) bool {
		return strings.Contains(ansi.Strip(l), link)
	})
	screenRow := frameRow - (len(lines) - m.Height)
	if _, ok := m.hitTest(40, screenRow); !ok {
		t.Errorf("no link hit at screen row %d (frame row %d)", screenRow, frameRow)
	}
	if _, ok := m.hitTest(40, frameRow); ok {
		t.Error("link hit at the unscrolled frame row, which is off screen")
	}
}

// lipgloss styles rune by rune under Underline, which shreds an escape
// sequence embedded in the styled text: the hyperlink must wrap the styled
// string, not the other way round. Only reproduces at a real color profile.
func TestLinkHyperlinkSurvivesStyling(t *testing.T) {
	re := lipgloss.NewRenderer(nil)
	re.SetColorProfile(termenv.TrueColor)
	m := &Model{Width: 100, Height: 40, Style: re.NewStyle(), hoverLink: true,
		apod: &apod.APOD{Image: &nasa.Image{Title: "A Test Nebula",
			Explanation: "some words about space.",
			ApodDate:    time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)}}}
	m.State = StateAPOD

	link := m.apod.Link()
	for y, raw := range strings.Split(m.baseView(), "\n") {
		line := ansi.Strip(raw)
		if !strings.Contains(line, "apod.nasa.gov") {
			continue
		}
		// the escape must be gone once stripped, leaving only the visible URL
		if strings.Contains(line, "]8;;") {
			t.Fatalf("row %d: hyperlink escape rendered as text: %q", y, line)
		}
		start, end, ok := columnSpan(line, link)
		if !ok {
			t.Fatalf("row %d: link not found in %q", y, line)
		}
		if _, hit := m.hitTest((start+end)/2, y); !hit {
			t.Errorf("row %d: no hover hit at the link's own columns", y)
		}
		return
	}
	t.Fatal("no link row in the frame")
}
