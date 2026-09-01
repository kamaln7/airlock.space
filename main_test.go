package airlockspace

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
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

// a video day has no picture, so nothing may offer to enlarge one
func TestVideoDayOffersNoImageKeys(t *testing.T) {
	m := &Model{Width: 100, Height: 40, apod: &apod.APOD{Image: &nasa.Image{}}}
	m.Init()
	m.Update(imageMsg{ok: false})

	for _, k := range m.helpKeys() {
		if h := k.Help(); h.Key == "f" || h.Key == "p" {
			t.Errorf("footer offers %q with no image", h.Key)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if m.State == StateFullscreen {
		t.Error("went fullscreen with no image to show")
	}
	// the explanation still gets the whole reading box
	if got := countLines(m.baseView()); got > m.Height {
		t.Errorf("frame is %d lines on a %d-row terminal", got, m.Height)
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

// besideImage is a model showing a tall picture next to the explanation, with
// the kitty path standing in so no decoding is needed.
func besideImage(t *testing.T, w, h int) *Model {
	t.Helper()
	m := testModel(t, day(2026, 8, 20))
	m.Width, m.Height = w, h
	// long enough that the reading box, not the text, decides the height
	m.apod.Explanation = strings.Repeat("Some words about deep space and distant galaxies. ", 60)
	m.apod.ImageSize = image.Point{X: 1059, Y: 1641}
	m.Session, m.KittyGraphics = io.Discard, true
	m.imageOK, m.kittyReady = true, true
	m.apod.ImageBytes = func() ([]byte, error) { return tinyPNG(), nil }
	return m
}

// tinyPNG is a real image, so the sextant path has something to decode when a
// test takes it rather than the kitty one.
func tinyPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 48))
	for y := range 48 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 5), 160, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// the picture and the text share rows now, so selection can no longer assume
// the text starts at the left edge: it has to know which columns are the
// explanation's, or dragging over it copies the picture beside it.
func TestExplanationSelectableBesideTheImage(t *testing.T) {
	t.Run("beside", func(t *testing.T) { explSelectable(t, besideImage(t, 150, 34)) })
	t.Run("video day", func(t *testing.T) {
		m := besideImage(t, 130, 30)
		m.imageOK, m.kittyReady = false, false
		m.apod.ImageSize = image.Point{}
		explSelectable(t, m)
	})
	t.Run("stacked under a wide picture", func(t *testing.T) {
		m := besideImage(t, 150, 34)
		m.apod.ImageSize = image.Point{X: 3000, Y: 900}
		explSelectable(t, m)
	})
}

func explSelectable(t *testing.T, m *Model) {
	t.Helper()
	lines := strings.Split(m.baseView(), "\n")
	if m.explWidth == 0 {
		t.Fatal("no explanation block recorded")
	}

	// the recorded column has to be where the text actually is, not merely
	// consistent with itself: a block that forgets to indent agrees with a
	// column of zero and still renders in the wrong place
	for _, l := range lines {
		stripped := ansi.Strip(l)
		i := strings.Index(stripped, "Some words about")
		if i < 0 {
			continue
		}
		if got := ansi.StringWidth(stripped[:i]); got != m.explCol {
			t.Fatalf("explanation text starts at column %d; explCol says %d", got, m.explCol)
		}
		break
	}

	var found int
	for _, l := range lines {
		stripped := ansi.Strip(l)
		x1, x2, ok := m.copyableSpan(stripped)
		if !ok {
			continue
		}
		if strings.Contains(stripped, m.apod.Title) {
			continue // the title row is selectable on its own terms
		}
		found++
		if x1 < m.explCol || x2 > m.explCol+m.explWidth {
			t.Errorf("span %d-%d strays outside the explanation columns %d-%d",
				x1, x2, m.explCol, m.explWidth+m.explCol)
		}
		text := strings.TrimSpace(ansi.Cut(stripped, x1, x2))
		if !strings.Contains(m.explanationText(), text) {
			t.Errorf("selected %q, which is not explanation text", text)
		}
	}
	if found == 0 {
		t.Error("no explanation row is selectable beside the picture")
	}
}

// whatever the terminal and whatever the picture's shape, the page has to fit
func TestMergedLayoutFits(t *testing.T) {
	for _, sz := range []image.Point{{X: 1059, Y: 1641}, {X: 1280, Y: 1280}, {X: 3000, Y: 900}} {
		for _, w := range []int{60, 80, 100, 130, 200} {
			for _, h := range []int{20, 24, 34, 60} {
				m := besideImage(t, w, h)
				m.apod.ImageSize = sz
				if got := countLines(m.baseView()); got > h {
					t.Errorf("%v at %dx%d: frame is %d rows", sz, w, h, got)
				}
			}
		}
	}
}

// every art has to be reachable, or the shuffle always lands on the same one
func TestGoodbyeArtIsActuallyVaried(t *testing.T) {
	seen := map[string]bool{}
	for range 60 {
		seen[randomArt("varied", 78, 40, colorMuted.dark)] = true
	}
	if len(seen) < len(ASCIIAll) {
		t.Errorf("only %d of %d arts ever drawn at 78 columns", len(seen), len(ASCIIAll))
	}
	// a narrow terminal still gets something, and it fits
	art := randomArt("narrow", 34, 40, colorMuted.dark)
	if art == "" {
		t.Fatal("no art fits 34 columns")
	}
	if w := lipgloss.Width(art); w > 34 {
		t.Errorf("art is %d wide in a 34 column box", w)
	}
}

// the goodbye is the art's home now; it has to actually be in there
func TestGoodbyeCarriesArt(t *testing.T) {
	out := ansi.Strip(Goodbye(100, "carries"))
	if !strings.Contains(out, "thanks for visiting") {
		t.Fatal("no farewell in the goodbye")
	}
	art := randomArt("carries", 98, 40, colorMuted.dark)
	if art == "" {
		t.Fatal("no art fits, so the check below proves nothing")
	}
	// every art is a block of drawing glyphs; the goodbye's other lines are not
	rows := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.ContainsAny(l, "\u2800\u28ff\u2580\u2584\u2588\u259f\U0001fb00") {
			rows++
		}
	}
	if rows == 0 {
		t.Errorf("no art rows in the goodbye:\n%s", out)
	}
}

// the picture's box is its own size, so its top edge meets the text's beside
// it and no rows are left blank above or below it
func TestPictureTopAlignsWithText(t *testing.T) {
	m := besideImage(t, 150, 40)
	firstText, firstImg, lastImg := -1, -1, -1
	for i, l := range strings.Split(m.baseView(), "\n") {
		s := ansi.Strip(l)
		if firstText < 0 && strings.Contains(s, "Some words") {
			firstText = i
		}
		if strings.ContainsRune(s, 0x10EEEE) { // a kitty placeholder cell
			if firstImg < 0 {
				firstImg = i
			}
			lastImg = i
		}
	}
	if firstImg < 0 || firstText < 0 {
		t.Fatalf("picture row %d, text row %d: expected both", firstImg, firstText)
	}
	if firstImg != firstText {
		t.Errorf("picture starts on row %d, text on row %d; their tops must meet", firstImg, firstText)
	}
	// and the placement is exactly as tall as the picture, not the box
	_, imgH := fitCells(m.apod.ImageSize.X, m.apod.ImageSize.Y, imageMaxWidth,
		lastImg-firstImg+1, m.cellAspect())
	if got := lastImg - firstImg + 1; got != imgH {
		t.Errorf("picture occupies %d rows, fits %d: it is being padded", got, imgH)
	}
}

// a video day has no picture, but the page keeps its shape: the explanation
// stays top-aligned where it would be beside one, and the box is sized for
// the text alone rather than for a picture that is not there
func TestVideoDayKeepsThePageShape(t *testing.T) {
	rowOf := func(m *Model, needle string) int {
		for i, l := range strings.Split(m.baseView(), "\n") {
			if strings.Contains(ansi.Strip(l), needle) {
				return i
			}
		}
		return -1
	}
	photo := besideImage(t, 130, 30)
	video := besideImage(t, 130, 30)
	video.imageOK, video.kittyReady = false, false
	video.apod.ImageSize = image.Point{}

	for _, needle := range []string{"Some words", "apod.nasa.gov", "q quit"} {
		a, b := rowOf(photo, needle), rowOf(video, needle)
		if a < 0 || a != b {
			t.Errorf("%q is on row %d with a picture and row %d without", needle, a, b)
		}
	}

	// measured, not read from explCol: a block that forgets to indent agrees
	// with the column it recorded and still renders in the wrong place
	col := -1
	for _, l := range strings.Split(video.baseView(), "\n") {
		s := ansi.Strip(l)
		if i := strings.Index(s, "Some words"); i >= 0 {
			col = ansi.StringWidth(s[:i])
			break
		}
	}
	left := col
	right := video.Width - (col + video.explWidth + scrollWidth)
	if left-right > 1 || right-left > 1 {
		t.Errorf("video day is off centre: %d columns to the left, %d to the right", left, right)
	}
}

// fullscreen is still a place you can act from: the picture toggle and the way
// out belong on its help bar too
func TestFullscreenHelpKeys(t *testing.T) {
	m := besideImage(t, 130, 30)
	m.State = StateFullscreen

	var keys []string
	for _, k := range m.helpKeys() {
		keys = append(keys, k.Help().Key)
	}
	for _, want := range []string{"f", "p", "q"} {
		if !slices.Contains(keys, want) {
			t.Errorf("fullscreen help %v has no %q", keys, want)
		}
	}
	// no picture, no picture toggle
	m.KittyGraphics = false
	for _, k := range m.helpKeys() {
		if k.Help().Key == "p" {
			t.Error("offered the photo toggle to a client that cannot draw one")
		}
	}
}

// the terminal's own answer about its cell size outranks what ssh reports.
// They disagree whenever the terminal adjusts its cell without adjusting the
// window it reports - ghostty's adjust-cell-height does exactly that - and a
// wrong ratio fits the picture to the wrong shape.
func TestCellAspectPrefersTheTerminalsAnswer(t *testing.T) {
	m := &Model{Width: 100, Height: 40}
	if got := m.cellAspect(); got != defaultCellAspect {
		t.Errorf("cellAspect() = %v with nothing to go on; want %v", got, defaultCellAspect)
	}

	// ssh says the window is 800x800, so cells look like 8x20
	m.WidthPixels, m.HeightPixels = 800, 800
	if got := m.cellAspect(); got != 2.5 {
		t.Errorf("cellAspect() = %v from the ssh window; want 2.5", got)
	}

	// but the terminal itself draws 8x26 cells
	m.Update(uv.CellSizeEvent{Width: 8, Height: 26})
	if got := m.cellAspect(); got != 26.0/8.0 {
		t.Errorf("cellAspect() = %v; want the terminal's own %v", got, 26.0/8.0)
	}
}

// learning the real cell size mid-session has to redraw the placement: the one
// already on screen was built to the wrong shape
func TestCellSizeAnswerRedrawsThePlacement(t *testing.T) {
	var out strings.Builder
	m := besideImage(t, 150, 40)
	m.Session = &out
	m.baseView()
	first := out.String()
	if !strings.Contains(first, "a=p") {
		t.Fatal("no placement on the first draw")
	}
	out.Reset()

	m.baseView() // unchanged: the placement is not re-sent for nothing
	if strings.Contains(out.String(), "a=p") {
		t.Error("re-sent an unchanged placement")
	}

	m.Update(uv.CellSizeEvent{Width: 8, Height: 26})
	out.Reset()
	m.baseView()
	if !strings.Contains(out.String(), "a=p") {
		t.Error("kept a placement built for the wrong cell size")
	}
}

// a tall picture in fullscreen leaves a margin either side; the keys go down
// it, one to a row, and must still be recognised and clickable there
func TestFullscreenStacksHelpBesideATallPicture(t *testing.T) {
	m := besideImage(t, 120, 28)
	m.State = StateFullscreen

	lines := strings.Split(m.baseView(), "\n")
	rows := map[string]int{}
	for i, l := range lines {
		s := ansi.Strip(l)
		for _, k := range m.helpKeys() {
			h := k.Help()
			if strings.Contains(s, h.Key+" "+h.Desc) {
				rows[h.Key] = i
			}
		}
	}
	if len(rows) < 3 {
		t.Fatalf("only %d help items on screen: %v", len(rows), rows)
	}
	seen := map[int]bool{}
	for k, r := range rows {
		if seen[r] {
			t.Errorf("%q shares row %d; stacked help is one item to a row", k, r)
		}
		seen[r] = true
	}

	// the keys line up with each other down the margin, not each centered on
	// its own row
	col := -1
	for _, r := range slices.Sorted(maps.Values(rows)) {
		s := ansi.Strip(lines[r])
		at := ansi.StringWidth(s) - ansi.StringWidth(strings.TrimLeft(s, " "))
		if col < 0 {
			col = at
		} else if at != col {
			t.Errorf("stacked help starts at column %d on row %d, %d elsewhere", at, r, col)
		}
	}
	if col < 1 {
		t.Errorf("stacked help is jammed against the screen edge at column %d", col)
	}

	// and vertically centered against the picture
	first, last := slices.Min(slices.Collect(maps.Values(rows))), slices.Max(slices.Collect(maps.Values(rows)))
	if mid := (first + last) / 2; mid < m.Height/2-2 || mid > m.Height/2+2 {
		t.Errorf("stacked help centers on row %d of %d", mid, m.Height)
	}

	// the two-item rule cannot see a lone item, so hit-testing has to know
	row := rows["q"]
	line := ansi.Strip(lines[row])
	if !m.isHelpLine(line) {
		t.Fatalf("row %d is not recognised as help: %q", row, strings.TrimSpace(line))
	}
	start, end, _ := columnSpan(line, "q quit")
	if got, _ := m.hitTest((start+end)/2, row); got != "q" {
		t.Errorf("hitTest on the stacked quit item = %q", got)
	}
}

// the scroll hint has to be right on the frame it appears, not one behind
func TestScrollHintIsNotAFrameLate(t *testing.T) {
	m := besideImage(t, 130, 24) // long explanation, short terminal
	first := ansi.Strip(m.baseView())
	if !m.scrolls() {
		t.Fatal("fixture does not scroll")
	}
	if !strings.Contains(first, "scroll") {
		t.Error("the first frame offers no scroll hint for a scrolling explanation")
	}
}

// a returning visitor should get a drawing they have not just seen, until
// they have seen them all and the rotation starts over
func TestGoodbyeArtRotatesPerClient(t *testing.T) {
	fits := []int{}
	for i, a := range ASCIIAll {
		if countLines(a) <= 40 && lipgloss.Width(a) <= 98 {
			fits = append(fits, i)
		}
	}
	if len(fits) < 3 {
		t.Fatalf("only %d arts fit; this proves little", len(fits))
	}

	var run []int
	for range len(fits) {
		run = append(run, pickArt("alice", fits))
	}
	if len(slices.Compact(slices.Sorted(slices.Values(run)))) != len(fits) {
		t.Errorf("a full rotation repeated itself: %v", run)
	}
	// the next one starts a fresh rotation rather than running dry
	if got := pickArt("alice", fits); !slices.Contains(fits, got) {
		t.Errorf("pickArt returned %d, not one of %v", got, fits)
	}

	// one visitor's rotation is not another's
	artLogReset()
	a, b := pickArt("alice", fits), pickArt("bob", fits)
	if got := pickArt("alice", fits); got == a {
		t.Error("alice was shown the same art twice running")
	}
	_ = b
}

// the log is a nicety on a small box: it must not grow without bound
func TestGoodbyeArtLogIsBounded(t *testing.T) {
	artLogReset()
	fits := []int{0}
	for i := range artMemory * 2 {
		pickArt(fmt.Sprintf("client-%d", i), fits)
	}
	artLog.Lock()
	defer artLog.Unlock()
	if len(artLog.seen) > artMemory || len(artLog.order) > artMemory {
		t.Errorf("log holds %d clients (order %d); cap is %d",
			len(artLog.seen), len(artLog.order), artMemory)
	}
	// the oldest client is the one forgotten
	if _, ok := artLog.seen["client-0"]; ok {
		t.Error("kept the least recently seen client and evicted someone newer")
	}
	if _, ok := artLog.seen[fmt.Sprintf("client-%d", artMemory*2-1)]; !ok {
		t.Error("forgot the most recent client")
	}
}

func artLogReset() {
	artLog.Lock()
	defer artLog.Unlock()
	artLog.seen, artLog.order = map[string][]int{}, nil
}

// the picture is uploaded in pieces so frames can get onto the wire between
// them; each piece has to be a complete, correctly keyed escape
func TestKittyChunksAreWellFormed(t *testing.T) {
	png := make([]byte, 20000) // enough for several chunks
	chunks := kittyChunks(png)
	if len(chunks) < 3 {
		t.Fatalf("got %d chunks, want several", len(chunks))
	}
	for i, c := range chunks {
		if !strings.HasPrefix(c, "\x1b_G") || !strings.HasSuffix(c, "\x1b\\") {
			t.Errorf("chunk %d is not a complete escape: %q", i, c[:min(30, len(c))])
		}
	}
	if !strings.HasPrefix(chunks[0], "\x1b_Ga=t,f=100,i=42,q=2,m=1;") {
		t.Errorf("first chunk does not open the transmission: %q", chunks[0][:40])
	}
	for i, c := range chunks[1 : len(chunks)-1] {
		if !strings.HasPrefix(c, "\x1b_Gm=1;") {
			t.Errorf("middle chunk %d is not a continuation: %q", i+1, c[:20])
		}
	}
	if !strings.HasPrefix(chunks[len(chunks)-1], "\x1b_Gm=0;") {
		t.Error("the last chunk does not close the transmission")
	}
}

// the notice sits over the middle of the picture, on top of the art rather
// than in place of it, and only once the send is slow enough to be worth
// mentioning
func TestSendingNoticeSitsOverThePicture(t *testing.T) {
	m := besideImage(t, 130, 34)
	m.preferArt, m.kittyReady = false, false
	if cmd := m.sendKitty(); cmd == nil {
		t.Fatal("nothing to send")
	}
	if m.photoSince.IsZero() {
		t.Fatal("sending the photo did not start the clock")
	}
	// up at once, unlike a spinner: what decides how long it shows is the
	// picture travelling, which is never nothing, and it is written ahead of
	// those bytes so it is on screen for all of it
	busy := m.baseView()
	if !strings.Contains(ansi.Strip(busy), "loading photo") {
		t.Fatal("no notice while the photo is on its way")
	}
	rows := strings.Split(ansi.Strip(busy), "\n")
	var at int
	for i, r := range rows {
		if strings.Contains(r, "loading photo") {
			at = i
		}
	}
	if at < 4 || at > len(rows)-4 {
		t.Errorf("notice is on row %d of %d; it should be in the middle", at, len(rows))
	}

	m.Update(kittyMsg{apod: m.apod, ok: true})
	quiet := m.baseView()
	if strings.Contains(ansi.Strip(quiet), "loading photo") {
		t.Error("notice still up after the photo arrived")
	}
	// laid over the picture, not squeezed in beside it: the frame is the same
	// size either way
	if a, b := countLines(quiet), countLines(busy); a != b {
		t.Errorf("frame is %d rows while sending, %d otherwise", b, a)
	}
}

// the notice goes over the picture, not instead of it: everything around it
// has to survive, which is what the cell canvas would not do
func TestOverlayCenterKeepsWhatIsAround(t *testing.T) {
	base := strings.TrimSuffix(strings.Repeat("..........\n", 7), "\n")
	got := strings.Split(overlayCenter(base, "[XX]"), "\n")

	if len(got) != 7 {
		t.Fatalf("overlay changed the height: %d rows, want 7", len(got))
	}
	if got[3] != "...[XX]..." {
		t.Errorf("middle row is %q; want the overlay centered in what was there", got[3])
	}
	for i, l := range got {
		if i != 3 && l != ".........." {
			t.Errorf("row %d became %q; only the middle should change", i, l)
		}
	}

	// taller than what it would cover: the notice is the more useful of the two
	if got := overlayCenter("one row", "a\nb\nc"); got != "a\nb\nc" {
		t.Errorf("overlay too big for its base returned %q", got)
	}
}

// going fullscreen re-cuts the photo only when that is worth a second
// transfer. On an ordinary terminal it is not: the box widens a lot but the
// picture is bound by the rows, so it gains a twentieth and nobody sees it.
func TestFullscreenOnlyResendsWhenItIsWorthIt(t *testing.T) {
	ordinary := besideImage(t, 200, 50)
	ordinary.cellW, ordinary.cellH = 8, 20
	ordinary.preferArt, ordinary.kittyReady = false, false
	if cmd := ordinary.sendKitty(); cmd == nil {
		t.Fatal("no first send")
	}
	was := ordinary.sentW
	ordinary.kittyReady = true

	ordinary.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if ordinary.State != StateFullscreen {
		t.Fatal("f did not go fullscreen")
	}
	if ordinary.sentW != was {
		t.Errorf("re-cut the photo from %d to %d wide, a %.2fx gain nobody can see",
			was, ordinary.sentW, float64(ordinary.sentW)/float64(was))
	}

	// on a small terminal fullscreen really is a different picture, and it goes
	small := besideImage(t, 80, 24)
	small.cellW, small.cellH = 8, 16
	small.preferArt, small.kittyReady = false, false
	if cmd := small.sendKitty(); cmd == nil {
		t.Fatal("no first send on the small terminal")
	}
	wasSmall := small.sentW
	small.kittyReady = true

	small.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if gain := float64(small.sentW) / float64(wasSmall); gain < resendGain {
		t.Errorf("fullscreen kept a photo cut for a twelve-row column: %d -> %d (%.2fx)",
			wasSmall, small.sentW, gain)
	}

	// and coming back never sends a smaller one
	back := small.sentW
	small.kittyReady = true
	small.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if small.sentW != back {
		t.Errorf("leaving fullscreen sent a smaller photo: %d -> %d", back, small.sentW)
	}
}

// art costs a jpeg decode and a pass through chafa. A drag asks for a dozen
// sizes nobody will look at, so the picture holds still until it stops - while
// the page around it reflows at once.
func TestResizeHoldsTheArtStill(t *testing.T) {
	m := besideImage(t, 130, 30)
	m.KittyGraphics, m.kittyReady, m.preferArt = false, false, true

	m.baseView()
	drawn := m.lastArt
	if drawn == "" {
		t.Fatal("no art drawn to begin with")
	}

	m.Update(tea.WindowSizeMsg{Width: 142, Height: 34})
	if !m.resizing {
		t.Fatal("a resize did not start")
	}
	if m.Width != 130 {
		t.Error("the page reflowed while the window was still moving")
	}

	// the page catches up first, and the art is still the old one, cut to fit
	m.Update(msgResized{gen: m.resizeGen, kind: settleLayout})
	if m.Width != 142 {
		t.Fatalf("the page did not reflow on its settle: %d", m.Width)
	}
	frame := m.baseView()
	if m.lastArt != drawn {
		t.Error("art was drawn again before its own settle")
	}
	if strings.TrimSpace(ansi.Strip(frame)) == "" {
		t.Error("the frame went blank while the art was held")
	}
	if !strings.Contains(ansi.Strip(frame), m.apod.Title) {
		t.Error("the page did not reflow with the rest")
	}

	m.Update(msgResized{gen: m.resizeGen, kind: settleArt})
	if m.resizing {
		t.Fatal("the art settle did not release the picture")
	}
	m.baseView()
	if m.lastArt == drawn {
		t.Error("art was not drawn again once the window stopped")
	}
}

// a page laid out larger than the terminal is centered, so cutting it to fit
// slides it sideways and drops the bottom off. Shrinking reflows at once
// rather than showing that and jumping back.
func TestShrinkingReflowsAtOnce(t *testing.T) {
	m := besideImage(t, 150, 40)

	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.Width != 100 || m.Height != 30 {
		t.Errorf("page is still %dx%d after the terminal shrank to 100x30", m.Width, m.Height)
	}
	// the expensive work still waits for the dragging to stop
	if !m.resizing {
		t.Error("shrinking drew the art immediately")
	}
	if m.sentW != 0 {
		t.Error("shrinking sent a photo immediately")
	}

	// growing is what waits
	m.Update(msgResized{gen: m.resizeGen, kind: settleArt})
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 44})
	if m.Width != 100 {
		t.Errorf("growing reflowed at once, to %d", m.Width)
	}
}

// dragging a window edge fires resizes by the dozen, and each kind of work
// waits for the dragging to pause for as long as it costs
func TestResizeWaitsForTheDraggingToStop(t *testing.T) {
	m := besideImage(t, 150, 40)
	m.preferArt, m.photoToggled, m.kittyReady = false, true, false

	was := m.Width
	for i := range 5 {
		m.Update(tea.WindowSizeMsg{Width: 150 + i, Height: 40})
	}
	if m.sentW != 0 {
		t.Fatal("a photo went out mid-drag")
	}
	if m.Width != was {
		t.Errorf("the page reflowed mid-drag, to %d", m.Width)
	}
	latest := m.resizeGen
	if latest != 5 {
		t.Fatalf("five resizes counted as %d", latest)
	}

	m.Update(msgResized{gen: latest - 3, kind: settlePhoto}) // long overtaken
	if m.sentW != 0 {
		t.Error("an overtaken resize sent a photo")
	}

	// each settle does its own share and no more
	m.Update(msgResized{gen: latest, kind: settleLayout})
	if m.Width != 154 {
		t.Errorf("the page is laid out to %d after settling; want the last size, 154", m.Width)
	}
	if !m.resizing || m.sentW != 0 {
		t.Error("the layout settle drew art or sent a photo; neither is its job")
	}

	m.Update(msgResized{gen: latest, kind: settleArt})
	if m.resizing {
		t.Error("the art settle did not release the picture")
	}
	if m.sentW != 0 {
		t.Error("the art settle sent a photo; that is the longest one's job")
	}

	m.Update(msgResized{gen: latest, kind: settlePhoto})
	if m.sentW == 0 {
		t.Error("the photo settled and nothing was sent")
	}
}

// most days are already in hand and arrive in a millisecond. A spinner
// between them is a flicker, so the day on screen holds until the new one is
// actually slow.
func TestFastDayDoesNotFlashASpinner(t *testing.T) {
	m := besideImage(t, 130, 30)
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft}) // step back a day
	if m.State != StateLoading {
		t.Fatal("stepping back did not start a load")
	}

	if frame := ansi.Strip(m.baseView()); strings.Contains(frame, "loading") {
		t.Error("a spinner appeared before the day had even been slow")
	} else if !strings.Contains(frame, m.apod.Title) {
		t.Error("the day on screen was dropped for a blank frame")
	}

	// once it really is slow, say so
	m.daySince = time.Now().Add(-loadingDelay)
	if !strings.Contains(ansi.Strip(m.baseView()), "loading") {
		t.Error("no spinner for a day that is genuinely taking its time")
	}
}

// the photo is megabytes; asking for it on one day is not asking for it on
// every day after
func TestNewDayGoesBackToArt(t *testing.T) {
	m := besideImage(t, 130, 30)
	m.preferArt = true                              // as a fresh day starts
	m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"}) // ask for the photo
	if m.preferArt {
		t.Fatal("p did not switch to the photo")
	}
	if !m.photoToggled {
		t.Fatal("p did not record that the reader chose")
	}

	next := &apod.APOD{Image: &nasa.Image{Title: "Another Day",
		Explanation: "more words", ApodDate: day(2026, 8, 19)}}
	next.ImageBytes = func() ([]byte, error) { return tinyPNG(), nil }
	m.date = day(2026, 8, 19)
	m.Update(apodMsg{date: day(2026, 8, 19), apod: next})

	if !m.preferArt {
		t.Error("the photo followed the reader to the next day")
	}
	if m.photoToggled {
		t.Error("the next p should toggle from the default, not from a stale choice")
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
	m.preferArt, m.kittyReady = true, false

	var keys string
	for _, k := range m.helpKeys() {
		keys += k.Help().Key
	}
	if !strings.Contains(keys, "p") {
		t.Errorf("footer %q offers no p toggle", keys)
	}
}
