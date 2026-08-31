package airlockspace

import (
	"image"
	"image/color"
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
