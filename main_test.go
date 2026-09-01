package airlockspace

import (
	"fmt"
	"image"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	m := &Model{apod: &apod.APOD{Image: &nasa.Image{}}}
	m.Init()
	m.Update(imageMsg{ok: false})
	before := m.imgOrExplanation
	m.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
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

// a long explanation used to run off the bottom of the frame with no way to
// reach the rest of it. It scrolls now, and the keys it scrolls with must be
// only the ones this app has no use for.
func TestLongExplanationScrolls(t *testing.T) {
	m := testModel(t, day(2026, 8, 20))
	m.apod.Explanation = strings.Repeat("some words about space. ", 60)
	m.Height = 30

	if n := countLines(m.baseView()); n > m.Height {
		t.Errorf("frame is %d lines on a %d-row terminal", n, m.Height)
	}
	if !m.scrolls() {
		t.Fatalf("%d lines in a %d-line box does not scroll",
			m.vp.TotalLineCount(), m.vp.Height())
	}
	if m.scrollbar() == "" {
		t.Error("no scrollbar on a scrolling explanation")
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.vp.YOffset() == 0 {
		t.Fatal("down did not scroll the explanation")
	}

	// the viewport's stock keymap claims left/right, h/l and f, which are this
	// app's day arrows and fullscreen. They must reach the app, not the text.
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !m.date.Equal(day(2026, 8, 19)) {
		t.Errorf("left scrolled instead of stepping a day back; date = %v", m.date)
	}
	at := m.vp.YOffset()
	m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if m.vp.YOffset() != at {
		t.Error("f or h paged the explanation; they are fullscreen and prev-day")
	}
}

// a frame taller than the terminal shows its tail, so hit-testing has to
// follow the rows actually on screen. The explanation scrolls now, so this
// only arises on a terminal too short for the header furniture itself.
func TestFrameLineFollowsTheVisibleRows(t *testing.T) {
	m := testModel(t, day(2026, 8, 20))
	m.Height = 4

	lines := strings.Split(m.baseView(), "\n")
	if len(lines) <= m.Height {
		t.Fatalf("fixture no longer overflows: %d lines in %d rows", len(lines), m.Height)
	}
	off := len(lines) - m.Height
	for y := range m.Height {
		if got, want := m.frameLine(y), ansi.Strip(lines[y+off]); got != want {
			t.Fatalf("screen row %d = %q; want frame row %d, %q", y, got, y+off, want)
		}
	}
}

// lipgloss styles rune by rune under Underline, which shreds an escape
// sequence embedded in the styled text: the hyperlink must wrap the styled
// string, not the other way round. Only reproduces at a real color profile.
func TestLinkHyperlinkSurvivesStyling(t *testing.T) {
	m := &Model{Width: 100, Height: 40, hoverLink: true,
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
	m := &Model{Width: 100, Height: 40,
		date: date, latest: day(2026, 8, 31),
		apod: &apod.APOD{Image: &nasa.Image{Title: "A Test Nebula",
			Explanation: "some words about space.", ApodDate: date}}}
	m.State = StateAPOD
	return m
}

func press(m *Model, code rune) {
	m.Update(tea.KeyPressMsg{Code: code})
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

func TestDayNavigationInHeader(t *testing.T) {
	nav := func(m *Model) string { return ansi.Strip(m.viewNav(100)) }

	latest := testModel(t, day(2026, 8, 31))
	if got := nav(latest); strings.Contains(got, "\u2192") {
		t.Errorf("next-day offered on the latest day: %q", got)
	}
	if got := nav(latest); !strings.Contains(got, "\u2190 Aug 30") {
		t.Errorf("prev-day missing or mislabelled: %q", got)
	}
	if got := nav(testModel(t, day(2026, 8, 20))); !strings.Contains(got, "Aug 21 \u2192") {
		t.Errorf("next-day missing on an older day: %q", got)
	}
	if got := nav(testModel(t, apod.First)); strings.Contains(got, "\u2190") {
		t.Errorf("prev-day offered on the first APOD: %q", got)
	}
	// the day on screen keeps its year; browsing reaches back to 1995
	if got := nav(latest); !strings.Contains(got, "Aug 31, 2026") {
		t.Errorf("the day on screen is not dated in full: %q", got)
	}
	// and the footer no longer carries the arrows
	for _, k := range testModel(t, day(2026, 8, 20)).helpKeys() {
		if h := k.Help(); h.Key == "\u2190" || h.Key == "\u2192" {
			t.Errorf("footer still offers %q", h.Key)
		}
	}
}

// wide: one row, arrows pinned to the edges of a navMaxWidth container so they
// do not drift apart on a big terminal. tight: the three blocks wrap.
func TestDayNavigationWrapsWhenTight(t *testing.T) {
	m := testModel(t, day(2026, 8, 20)) // a day with both arrows

	wide := ansi.Strip(m.viewNav(200))
	if strings.Contains(wide, "\n") {
		t.Errorf("wrapped with room to spare:\n%s", wide)
	}
	if w := lipgloss.Width(wide); w != navMaxWidth {
		t.Errorf("day line is %d wide; want it capped at %d", w, navMaxWidth)
	}
	if !strings.HasPrefix(wide, "\u2190") || !strings.HasSuffix(wide, "\u2192") {
		t.Errorf("arrows are not on the container's edges: %q", wide)
	}

	rows := strings.Split(ansi.Strip(m.viewNav(30)), "\n")
	if len(rows) != 3 {
		t.Fatalf("tight day line is %d rows, want 3:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	// each block keeps its date once it has a row to itself
	if !strings.Contains(rows[0], "\u2190 Aug 19") || !strings.Contains(rows[2], "Aug 21 \u2192") {
		t.Errorf("wrapped rows lost their dates: %q", rows)
	}
	// and the arrows are still clickable where they wrapped to
	if got := m.navHit(rows[2], 2); got != "\u2192" {
		t.Errorf("navHit on the wrapped next-day row = %q", got)
	}
}

// the arrows only moved; clicking one on the header row must still navigate
func TestHeaderArrowClickNavigates(t *testing.T) {
	m := testModel(t, day(2026, 8, 31))
	label := "\u2190 Aug 30"

	lines := strings.Split(m.baseView(), "\n")
	row := slices.IndexFunc(lines, func(l string) bool {
		return strings.Contains(ansi.Strip(l), label)
	})
	if row < 0 {
		t.Fatal("no day arrow on the frame")
	}
	start, end, _ := columnSpan(ansi.Strip(lines[row]), label)

	if got, _ := m.hitTest((start+end)/2, row); got != "\u2190" {
		t.Fatalf("hitTest on the arrow = %q; want the prev-day key", got)
	}
	// the whole label is the target, not just the glyph
	if got, _ := m.hitTest(end-1, row); got != "\u2190" {
		t.Errorf("hitTest on the arrow's date = %q; want the prev-day key", got)
	}

	m.Update(tea.MouseClickMsg{X: (start + end) / 2, Y: row, Button: tea.MouseLeft})
	if !m.date.Equal(day(2026, 8, 30)) {
		t.Errorf("clicking the prev-day arrow moved to %v; want 2026-08-30", m.date)
	}
}

// the title has a row of its own now; dragging over it must still copy it,
// and the calendar row above it must stay unselectable
func TestTitleIsSelectableOnItsOwnRow(t *testing.T) {
	m := testModel(t, day(2026, 8, 20))
	lines := strings.Split(m.baseView(), "\n")
	row := slices.IndexFunc(lines, func(l string) bool {
		return strings.Contains(ansi.Strip(l), m.apod.Title)
	})
	if row < 1 {
		t.Fatal("no title row under the calendar")
	}
	start, end, ok := columnSpan(ansi.Strip(lines[row]), m.apod.Title)
	if !ok {
		t.Fatal("title not found on its row")
	}

	m.selAnchor, m.selEnd, m.selActive = point{start, row}, point{end - 1, row}, true
	if got := m.selectionText(); got != m.apod.Title {
		t.Errorf("selectionText() = %q; want %q", got, m.apod.Title)
	}
	if _, _, ok := m.copyableSpan(ansi.Strip(lines[row-1])); ok {
		t.Error("the calendar row is selectable; only the title and explanation are")
	}
}

// kitty stretches an image to fill whatever c x r it is handed once both are
// named, so the placement box has to carry the image's aspect ratio. Handing
// it the whole area distorts every image that is not the shape of the area.
func TestKittyPlacementCarriesTheAspect(t *testing.T) {
	for _, tc := range []struct {
		name               string
		imgW, imgH         int
		wantCols, wantRows int
	}{
		{"tall", 1000, 2000, 40, 40}, // 1000 x 1000 in cells -> square
		{"wide", 2000, 500, 60, 8},   // 2000 x 250 in cells, width-bound
		{"square", 1200, 1200, 60, 30},
	} {
		var out strings.Builder
		m := testModel(t, day(2026, 8, 20))
		m.Width, m.Height = 100, 50
		m.WidthPixels, m.HeightPixels = 1000, 1000 // 10x20 px cells: aspect 2
		m.Session, m.KittyGraphics = &out, true
		m.imageOK, m.kittyReady = true, true
		m.apod.ImageSize = image.Point{X: tc.imgW, Y: tc.imgH}

		m.imageArea(60, 40) // a box that is not the image's shape

		want := fmt.Sprintf("c=%d,r=%d", tc.wantCols, tc.wantRows)
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s: placement %q has no %q", tc.name, out.String(), want)
		}
		if strings.Contains(out.String(), "c=60,r=40") && tc.name != "square" {
			t.Errorf("%s: handed kitty the whole box; the image will be stretched", tc.name)
		}
	}
}

// the reported pixel size gives the real cell shape; without it, assume 2:1
func TestCellAspect(t *testing.T) {
	m := &Model{Width: 100, Height: 50}
	if got := m.cellAspect(); got != defaultCellAspect {
		t.Errorf("cellAspect() = %v with no pixel size; want %v", got, defaultCellAspect)
	}
	m.WidthPixels, m.HeightPixels = 1000, 1000 // 10x20 px cells
	if got := m.cellAspect(); got != 2 {
		t.Errorf("cellAspect() = %v; want 2", got)
	}
	m.WidthPixels, m.HeightPixels = 800, 1200 // 8x24 px cells, a taller cell
	if got := m.cellAspect(); got != 3 {
		t.Errorf("cellAspect() = %v; want 3", got)
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

// the photo is megabytes over ssh; art mode must not pay for it
func TestPhotoIsOnlySentWhenItWillBeSeen(t *testing.T) {
	m := testModel(t, day(2026, 8, 31))
	m.KittyGraphics, m.Session, m.imageOK = true, io.Discard, true

	m.preferArt = true
	if cmd := m.sendKitty(); cmd != nil {
		t.Error("uploaded the photo while showing sextant art")
	}
	m.preferArt = false
	if cmd := m.sendKitty(); cmd == nil {
		t.Error("did not upload the photo when the photo is what is shown")
	}
	m.kittyReady = true
	if cmd := m.sendKitty(); cmd != nil {
		t.Error("uploaded the same photo twice")
	}
}

// the footer offers p on capability, not on having already paid for the upload
func TestPhotoToggleOfferedBeforeTheUpload(t *testing.T) {
	m := testModel(t, day(2026, 8, 31))
	m.KittyGraphics, m.Session, m.imageOK = true, io.Discard, true
	m.imgOrExplanation, m.preferArt, m.kittyReady = true, true, false

	var keys string
	for _, k := range m.helpKeys() {
		keys += k.Help().Key
	}
	if !strings.Contains(keys, "p") {
		t.Errorf("footer %q offers no p toggle", keys)
	}
}
