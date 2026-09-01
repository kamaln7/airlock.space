package airlockspace

import (
	"bytes"

	"github.com/charmbracelet/x/ansi"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"

	chafa "github.com/ploMP4/chafa-go"
)

func TestRenderSextant(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 2), G: uint8(y * 4), B: 128, A: 255})
		}
	}

	out := renderSextant(img, 20, 10, chafa.CHAFA_CANVAS_MODE_TRUECOLOR)
	if out == "" {
		t.Fatal("empty render")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("got %d lines, want 10", len(lines))
	}
	if !strings.Contains(out, "\x1b[38;2;") {
		t.Fatal("expected truecolor escape sequences in output")
	}

	// a picture far larger than the art it becomes is cut down before chafa
	// sees it; the geometry it produces must be unchanged by that
	big := image.NewNRGBA(image.Rect(0, 0, 2000, 3000))
	for y := 0; y < 3000; y += 7 {
		for x := 0; x < 2000; x += 7 {
			big.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	out = renderSextant(big, 40, 25, chafa.CHAFA_CANVAS_MODE_TRUECOLOR)
	lines = strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 25 {
		t.Fatalf("large source rendered %d rows, want 25", len(lines))
	}
	if w := ansi.StringWidth(lines[0]); w != 40 {
		t.Errorf("large source rendered %d columns, want 40", w)
	}
}

func TestFitCells(t *testing.T) {
	// 1024x512 image into an 80x24 cell box: cell aspect halves the height
	cols, rows := fitCells(1024, 512, 80, 24, 2)
	if cols != 80 || rows != 20 {
		t.Fatalf("got %dx%d, want 80x20", cols, rows)
	}
}

// the shapes APOD actually posts: panoramas, portraits and squares all have to
// come out of the box with their proportions intact, never stretched to it
func TestFitCellsKeepsAspect(t *testing.T) {
	for _, tc := range []struct {
		name             string
		imgW, imgH       int
		boxCols, boxRows int
	}{
		{"very wide", 3000, 800, 80, 40},
		{"wide", 1600, 900, 80, 40},
		{"square", 1280, 1280, 80, 40},
		{"tall", 1059, 1641, 80, 40},
		{"very tall", 800, 2400, 80, 40},
	} {
		cols, rows := fitCells(tc.imgW, tc.imgH, tc.boxCols, tc.boxRows, 2)
		if cols < 1 || rows < 1 {
			t.Errorf("%s: got an empty %dx%d box", tc.name, cols, rows)
			continue
		}
		if cols > tc.boxCols || rows > tc.boxRows {
			t.Errorf("%s: %dx%d overflows the %dx%d box", tc.name, cols, rows, tc.boxCols, tc.boxRows)
		}
		// a cell is twice as tall as it is wide, so the drawn image is
		// cols x 2*rows in image proportions
		want := float64(tc.imgW) / float64(tc.imgH)
		got := float64(cols) / float64(2*rows)
		if math.Abs(got-want)/want > 0.08 {
			t.Errorf("%s: %dx%d cells is aspect %.2f; want %.2f", tc.name, cols, rows, got, want)
		}
	}
}

func TestKittyPNGFitsTheBox(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 1024))
	for y := range 1024 {
		for x := range 800 {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}

	byt, err := kittyPNG(src, 700, 500)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(byt))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width > 700 || cfg.Height > 500 {
		t.Errorf("encoded %dx%d; want it inside 700x500", cfg.Width, cfg.Height)
	}
	if cfg.Width != 391 || cfg.Height != 500 {
		t.Errorf("encoded %dx%d; want 391x500, the aspect-preserving fit", cfg.Width, cfg.Height)
	}

	// a box bigger than the source must not upscale: that is pure bytes
	byt, err = kittyPNG(src, 4000, 4000)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = png.DecodeConfig(bytes.NewReader(byt))
	if cfg.Width != 800 || cfg.Height != 1024 {
		t.Errorf("encoded %dx%d; want the source size untouched", cfg.Width, cfg.Height)
	}
}

// the photo is encoded for the cells it will be drawn into, not the screen:
// on a wide terminal the picture is a column beside the text, and pixels are
// the only real lever on what it weighs
func TestPhotoBoxIsThePaneNotTheScreen(t *testing.T) {
	m := &Model{Width: 200, Height: 50, cellW: 8, cellH: 20}

	w, h := m.photoBox()
	if w != imageMaxWidth*8 {
		t.Errorf("main view photo is %d px wide; want the %d-cell column", w, imageMaxWidth)
	}
	if full := 200 * 8; w >= full {
		t.Errorf("main view photo is %d px wide, the whole screen is %d", w, full)
	}
	if h != (50-photoChrome)*20 {
		t.Errorf("main view photo is %d px tall; want the rows left after the page", h)
	}

	// fullscreen asks for the lot, and gets it
	m.State = StateFullscreen
	fw, fh := m.photoBox()
	if fw != 200*8 || fh != 50*20 {
		t.Errorf("fullscreen photo box is %dx%d; want the whole screen", fw, fh)
	}
	if fw*fh <= w*h {
		t.Error("fullscreen asks for no more pixels than the column does")
	}
}

// NASA's picture is whatever NASA posted, and a hidpi terminal has a box big
// enough to ask for all of it. One day's photo does not get to decide how many
// megabytes go down every connection.
func TestKittyPNGIsCapped(t *testing.T) {
	huge := image.NewRGBA(image.Rect(0, 0, 4000, 6000))
	byt, err := kittyPNG(huge, 4000, 6000) // a box with room for all of it
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(byt))
	if err != nil {
		t.Fatal(err)
	}
	if px := cfg.Width * cfg.Height; px > maxPhotoPixels {
		t.Errorf("encoded %dx%d = %d pixels; cap is %d", cfg.Width, cfg.Height, px, maxPhotoPixels)
	}
	// the picture keeps its shape through the capping
	if want, got := 4000.0/6000.0, float64(cfg.Width)/float64(cfg.Height); got < want*0.98 || got > want*1.02 {
		t.Errorf("capping changed the aspect: %.3f, want %.3f", got, want)
	}

	// an ordinary picture in an ordinary box is left alone
	ord := image.NewRGBA(image.Rect(0, 0, 1059, 1641))
	byt, _ = kittyPNG(ord, 576, 760)
	cfg, _ = png.DecodeConfig(bytes.NewReader(byt))
	if cfg.Width != 490 || cfg.Height != 760 {
		t.Errorf("ordinary encode came out %dx%d; want the plain fit 490x760", cfg.Width, cfg.Height)
	}
}

// without the terminal's own answer, fall back to the ssh window, then to a
// plain guess at the cell
func TestCellPixels(t *testing.T) {
	m := &Model{Width: 100, Height: 40}
	if w, h := m.cellPixels(); w != 10 || h != 20 {
		t.Errorf("cellPixels() = %d,%d with nothing to go on; want the 10x20 guess", w, h)
	}
	m.WidthPixels, m.HeightPixels = 800, 800
	if w, h := m.cellPixels(); w != 8 || h != 20 {
		t.Errorf("cellPixels() = %d,%d from the ssh window; want 8,20", w, h)
	}
	m.cellW, m.cellH = 9, 26
	if w, h := m.cellPixels(); w != 9 || h != 26 {
		t.Errorf("cellPixels() = %d,%d; want the terminal's own 9,26", w, h)
	}
}
