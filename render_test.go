package airlockspace

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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
}

func TestFitCells(t *testing.T) {
	// 1024x512 image into an 80x24 cell box: cell aspect halves the height
	cols, rows := fitCells(1024, 512, 80, 24)
	if cols != 80 || rows != 20 {
		t.Fatalf("got %dx%d, want 80x20", cols, rows)
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

func TestPixelBoxFallsBackToCellGuess(t *testing.T) {
	m := &Model{Width: 100, Height: 40}
	if w, h := m.pixelBox(); w != 1000 || h != 800 {
		t.Errorf("pixelBox() = %d,%d; want the 10x20 per cell guess", w, h)
	}
	m.WidthPixels, m.HeightPixels = 2400, 1600
	if w, h := m.pixelBox(); w != 2400 || h != 1600 {
		t.Errorf("pixelBox() = %d,%d; want what ssh reported", w, h)
	}
}
