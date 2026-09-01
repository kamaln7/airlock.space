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

func day(y int, mo time.Month, d int) time.Time {
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
}

func testModel(t *testing.T, date time.Time) *Model {
	t.Helper()
	m := &Model{Width: 100, Height: 40, Style: lipgloss.NewStyle(),
		date: date, latest: day(2026, 8, 31),
		apod: &apod.APOD{Image: &nasa.Image{Title: "A Test Nebula",
			Explanation: "some words about space.", ApodDate: date}}}
	m.State = StateAPOD
	return m
}

func press(m *Model, k tea.KeyType) {
	m.Update(tea.KeyMsg{Type: k})
}

func TestDayNavigationStaysInRange(t *testing.T) {
	m := testModel(t, day(2026, 8, 31)) // the latest day
	press(m, tea.KeyRight)
	if !m.date.Equal(day(2026, 8, 31)) {
		t.Errorf("right past the latest day moved to %v", m.date)
	}
	press(m, tea.KeyLeft)
	if !m.date.Equal(day(2026, 8, 30)) {
		t.Errorf("left = %v; want 2026-08-30", m.date)
	}
	if m.State != StateLoading {
		t.Errorf("State = %v; want the spinner while the day loads", m.State)
	}

	m = testModel(t, apod.First)
	press(m, tea.KeyLeft)
	if !m.date.Equal(apod.First) {
		t.Errorf("left before the first APOD moved to %v", m.date)
	}
}

func TestDayNavigationFooter(t *testing.T) {
	shown := func(m *Model) string {
		var s string
		for _, k := range m.helpKeys() {
			s += k.Help().Key
		}
		return s
	}
	if got := shown(testModel(t, day(2026, 8, 31))); strings.Contains(got, "→") {
		t.Errorf("next-day offered on the latest day: %q", got)
	}
	if got := shown(testModel(t, day(2026, 8, 20))); !strings.Contains(got, "→") {
		t.Errorf("next-day missing on an older day: %q", got)
	}
	if got := shown(testModel(t, apod.First)); strings.Contains(got, "←") {
		t.Errorf("prev-day offered on the first APOD: %q", got)
	}
}

// a slow day that the user has already navigated away from must not land
func TestStaleDayResponseIsDropped(t *testing.T) {
	m := testModel(t, day(2026, 8, 31))
	press(m, tea.KeyLeft) // now waiting on the 30th
	overtaken := &apod.APOD{Image: &nasa.Image{Title: "Overtaken", ApodDate: day(2026, 8, 25)}}
	m.Update(apodMsg{date: day(2026, 8, 25), apod: overtaken})
	if m.apod.Title == "Overtaken" {
		t.Error("a response for a day we left applied anyway")
	}

	wanted := &apod.APOD{Image: &nasa.Image{Title: "Wanted", ApodDate: day(2026, 8, 30)}}
	m.Update(apodMsg{date: day(2026, 8, 30), apod: wanted})
	if m.apod.Title != "Wanted" || m.State != StateAPOD {
		t.Errorf("apod = %q, state = %v; want the day we asked for", m.apod.Title, m.State)
	}
}

// a failed day leaves the reader on the day already on screen
func TestFailedDayKeepsTheCurrentOne(t *testing.T) {
	m := testModel(t, day(2026, 8, 31))
	press(m, tea.KeyLeft)
	m.Update(apodMsg{date: day(2026, 8, 30), apod: nil})
	if !m.date.Equal(day(2026, 8, 31)) || m.State != StateAPOD {
		t.Errorf("date = %v, state = %v; want to stay on 2026-08-31", m.date, m.State)
	}
}
