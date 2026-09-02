package airlockspace

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"strings"
	"sync"

	"github.com/charmbracelet/colorprofile"
	"github.com/kamaln7/airlock.space/internal/chafa"
	xdraw "golang.org/x/image/draw"
)

// fitCells fits an image of imgW x imgH pixels into a box of cells, preserving
// aspect ratio. cellAspect is how many times taller a cell is than it is wide,
// the number that turns pixels into cells.
func fitCells(imgW, imgH, boxCols, boxRows int, cellAspect float64) (cols, rows int) {
	if cellAspect <= 0 {
		cellAspect = defaultCellAspect
	}
	return fitImage(imgW, int(math.Round(float64(imgH)/cellAspect)), boxCols, boxRows)
}

func canvasMode(p colorprofile.Profile) chafa.Mode {
	switch p {
	case colorprofile.TrueColor:
		return chafa.TrueColor
	case colorprofile.ANSI256:
		return chafa.Indexed240
	case colorprofile.ANSI:
		return chafa.Indexed16
	default:
		return chafa.FgBg
	}
}

// renderSextant converts img to sextant-glyph art of exactly cols x rows cells.
//
// The picture goes in whole. We used to shrink it first, to about the detail a
// sextant cell can hold, because handing the C library a photograph meant
// copying it at full size and then watching it throw nearly all of it away -
// which cost more than the shrinking did. Our own renderer scales the way
// chafa does, pulling source rows on demand, so there is nothing left to save
// by shrinking first, and the picture it draws is now the one chafa would
// draw rather than an approximation of it.
func renderSextant(img image.Image, cols, rows int, mode chafa.Mode) (string, error) {
	return chafa.Render(img, chafa.Options{Cols: cols, Rows: rows, Mode: mode})
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

func sextantKey(date string, cols, rows int, mode chafa.Mode) string {
	return fmt.Sprintf("%s|%dx%d|%d", date, cols, rows, mode)
}

// sextantCached returns a render already in hand, and says whether there was
// one. Drawing art costs a jpeg decode and a pass through chafa, so while the
// window is still moving the answer to a miss is to wait, not to render.
func sextantCached(date string, cols, rows int, mode chafa.Mode) (string, bool) {
	renderCache.Lock()
	defer renderCache.Unlock()
	s, ok := renderCache.entries[sextantKey(date, cols, rows, mode)]
	return s, ok
}

// decodeFn matches apod.(*APOD).Decode: decode fresh, drop after render.
func cachedSextant(date string, decode func() (image.Image, error), cols, rows int, mode chafa.Mode) (string, error) {
	key := sextantKey(date, cols, rows, mode)

	if s, ok := sextantCached(date, cols, rows, mode); ok {
		return s, nil
	}

	img, err := decode()
	if err != nil {
		return "", err
	}
	s, err := renderSextant(img, cols, rows, mode)
	if err != nil {
		return "", err
	}

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
	w, h := fitImage(b.Dx(), b.Dy(), maxW, maxH)
	// only ever down: upscaling here would just cost bytes, and the terminal
	// scales the placement to the cell box anyway
	if w > b.Dx() || h > b.Dy() {
		w, h = b.Dx(), b.Dy()
	}
	// The session can only ask for so much - the picture is a column 72 cells
	// wide and the rows the page leaves it. What is unbounded is the other
	// side: NASA's image is whatever NASA posted, and a hidpi terminal has a
	// box big enough to ask for all of it. Every picture so far fits under
	// this, so it changes nothing today; it is here so that one enormous day
	// cannot decide by itself how many megabytes go down every connection, and
	// how much of a 512MB box is spent resampling it.
	if px := w * h; px > maxPhotoPixels {
		s := math.Sqrt(float64(maxPhotoPixels) / float64(px))
		w, h = max(1, int(float64(w)*s)), max(1, int(float64(h)*s))
	}
	if w >= 1 && h >= 1 && (w < b.Dx() || h < b.Dy()) {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

// maxPhotoPixels bounds what one photo may be encoded to, whatever was posted
// and whatever the terminal has room for.
const maxPhotoPixels = 2 << 20 // ~2 megapixels

// kitty graphics protocol: https://sw.kovidgoyal.net/kitty/graphics-protocol/
const kittyImageID = 42

// kittyChunks splits PNG data into the escape-wrapped pieces that upload it
// under kittyImageID without displaying it. Returned as pieces rather than one
// string so the caller can write them one at a time and let a frame out
// between: a megabyte written in a single call holds the connection for
// seconds, and nothing else can reach the terminal while it does.
//
// The protocol's rule is that no other *graphics* escape may arrive between
// the chunks of one image. Nothing emits one while an upload is in flight -
// the placement is only written once the upload has landed, and until then the
// view is drawing sextant art.
func kittyChunks(pngData []byte) []string {
	b64 := base64.StdEncoding.EncodeToString(pngData)
	var chunks []string
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
		chunks = append(chunks, "\x1b_G"+ctrl+";"+chunk+"\x1b\\")
	}
	return chunks
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
