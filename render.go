package airlockspace

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"strings"
	"sync"

	"github.com/charmbracelet/colorprofile"
	chafa "github.com/ploMP4/chafa-go"
	xdraw "golang.org/x/image/draw"
)

// fitCells fits an image of imgW x imgH pixels into a box of cells, preserving
// aspect ratio. A terminal cell is ~twice as tall as it is wide.
func fitCells(imgW, imgH, boxCols, boxRows int) (cols, rows int) {
	return fitImage(imgW, imgH/2, boxCols, boxRows)
}

func canvasMode(p colorprofile.Profile) chafa.CanvasMode {
	switch p {
	case colorprofile.TrueColor:
		return chafa.CHAFA_CANVAS_MODE_TRUECOLOR
	case colorprofile.ANSI256:
		return chafa.CHAFA_CANVAS_MODE_INDEXED_240
	case colorprofile.ANSI:
		return chafa.CHAFA_CANVAS_MODE_INDEXED_16
	default:
		return chafa.CHAFA_CANVAS_MODE_FGBG
	}
}

// renderSextant converts img to sextant-glyph art of exactly cols x rows cells.
func renderSextant(img image.Image, cols, rows int, mode chafa.CanvasMode) string {
	bounds := img.Bounds()
	rgba := image.NewNRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)

	symbolMap := chafa.SymbolMapNew()
	defer chafa.SymbolMapUnref(symbolMap)
	chafa.SymbolMapAddByTags(symbolMap, chafa.CHAFA_SYMBOL_TAG_SEXTANT)

	config := chafa.CanvasConfigNew()
	defer chafa.CanvasConfigUnref(config)
	chafa.CanvasConfigSetGeometry(config, int32(cols), int32(rows))
	chafa.CanvasConfigSetCanvasMode(config, mode)
	chafa.CanvasConfigSetSymbolMap(config, symbolMap)

	canvas := chafa.CanvasNew(config)
	defer chafa.CanvasUnRef(canvas)
	chafa.CanvasDrawAllPixels(canvas, chafa.CHAFA_PIXEL_RGBA8_UNASSOCIATED,
		rgba.Pix, int32(bounds.Dx()), int32(bounds.Dy()), int32(rgba.Stride))

	return chafa.CanvasPrint(canvas, nil).String()
}

// renderCache shares rendered frames across all sessions: everyone is looking
// at the same APOD and terminal sizes cluster around a few common ones.
// renders are keyed by day as well as size, so stepping back to a day already
// seen redraws from memory.
// ponytail: emptied wholesale at the cap rather than evicting an entry
const maxRenders = 64

var renderCache = struct {
	sync.Mutex
	entries map[string]string
}{entries: map[string]string{}}

// decodeFn matches apod.(*APOD).Decode: decode fresh, drop after render.
func cachedSextant(date string, decode func() (image.Image, error), cols, rows int, mode chafa.CanvasMode) (string, error) {
	key := fmt.Sprintf("%s|%dx%d|%d", date, cols, rows, mode)

	renderCache.Lock()
	if s, ok := renderCache.entries[key]; ok {
		renderCache.Unlock()
		return s, nil
	}
	renderCache.Unlock()

	img, err := decode()
	if err != nil {
		return "", err
	}
	s := renderSextant(img, cols, rows, mode)

	renderCache.Lock()
	if len(renderCache.entries) >= maxRenders {
		clear(renderCache.entries)
	}
	renderCache.entries[key] = s
	renderCache.Unlock()
	return s, nil
}

// kittyPNG encodes the photo for transmission, scaled to fit maxW x maxH
// device pixels - never more than the terminal can actually display.
//
// The resampling pays for itself twice. CatmullRom is the nicest of the
// practical kernels going down, and it also removes the source jpeg's noise,
// which png cannot compress: fitting an 800x1024 source into 1400x900 drops
// 23% of the pixels and 62% of the bytes.
func kittyPNG(img image.Image, maxW, maxH int) ([]byte, error) {
	b := img.Bounds()
	// only ever down: upscaling here would just cost bytes, and the terminal
	// scales the placement to the cell box anyway
	if w, h := fitImage(b.Dx(), b.Dy(), maxW, maxH); w >= 1 && h >= 1 && (w < b.Dx() || h < b.Dy()) {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

// kitty graphics protocol: https://sw.kovidgoyal.net/kitty/graphics-protocol/
const kittyImageID = 42

// kittyTransmit uploads PNG data under kittyImageID without displaying it.
// Safe to write at any time; it draws nothing.
func kittyTransmit(pngData []byte) string {
	b64 := base64.StdEncoding.EncodeToString(pngData)
	var s strings.Builder
	first := true
	for len(b64) > 0 {
		chunk := b64
		if len(chunk) > 4096 {
			chunk = b64[:4096]
		}
		b64 = b64[len(chunk):]
		ctrl := fmt.Sprintf("m=%d", boolToInt(len(b64) > 0))
		if first {
			ctrl = fmt.Sprintf("a=t,f=100,i=%d,q=2,", kittyImageID) + ctrl
			first = false
		}
		s.WriteString("\x1b_G" + ctrl + ";" + chunk + "\x1b\\")
	}
	return s.String()
}

// kittyVirtualPlacement creates or replaces the virtual placement that maps
// the image onto a cols x rows grid. It draws nothing by itself — cells appear
// wherever kittyPlaceholders text is printed — so it is safe to write to the
// session at any time.
func kittyVirtualPlacement(cols, rows int) string {
	return fmt.Sprintf("\x1b_Ga=p,U=1,i=%d,p=1,c=%d,r=%d,q=2\x1b\\", kittyImageID, cols, rows)
}

// kittyPlaceholders returns a cols x rows block of unicode placeholder cells
// referencing the virtual placement. It is plain text plus SGR color (the
// image id rides in the foreground color), so bubbletea's renderer can diff,
// truncate, and reposition it safely — unlike raw graphics escapes, which it
// mangles.
//
// The id survives only at a truecolor profile: the v2 renderer converts colors
// on the way out, and a downsampled foreground is a different image id. That
// holds because the only terminals we enable graphics for are truecolor ones.
func kittyPlaceholders(cols, rows int) string {
	cols = min(cols, len(kittyDiacritics))
	rows = min(rows, len(kittyDiacritics))
	var s strings.Builder
	for r := 0; r < rows; r++ {
		if r > 0 {
			s.WriteByte('\n')
		}
		fmt.Fprintf(&s, "\x1b[38;2;0;0;%dm", kittyImageID)
		for c := 0; c < cols; c++ {
			s.WriteRune(0x10EEEE)
			s.WriteRune(kittyDiacritics[r])
			s.WriteRune(kittyDiacritics[c])
		}
		s.WriteString("\x1b[39m")
	}
	return s.String()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
