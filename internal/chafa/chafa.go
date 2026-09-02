// Package chafa renders images as terminal text using Unicode sextant mosaics.
//
// It is a transliteration of the subset of chafa 1.19.0 reached by:
//
//	symbol_map = chafa_symbol_map_new ();
//	chafa_symbol_map_add_by_tags (symbol_map, CHAFA_SYMBOL_TAG_SEXTANT);
//	config = chafa_canvas_config_new ();
//	chafa_canvas_config_set_geometry (config, cols, rows);
//	chafa_canvas_config_set_canvas_mode (config, mode);
//	chafa_canvas_config_set_symbol_map (config, symbol_map);
//	canvas = chafa_canvas_new (config);
//	chafa_canvas_draw_all_pixels (canvas, CHAFA_PIXEL_RGBA8_UNASSOCIATED, ...);
//	out = chafa_canvas_print (canvas, NULL);
//
// Everything else keeps chafa_canvas_config_new()'s defaults: average colour
// extractor, RGB colour space, no dithering, work factor 0.5, preprocessing on,
// all optimizations on, white on black, alpha threshold 127.
package chafa

import (
	"errors"
	"image"
	imgcolor "image/color"
)

// Mode selects the terminal colour model.
type Mode int

const (
	// TrueColor is CHAFA_CANVAS_MODE_TRUECOLOR.
	TrueColor Mode = iota
	// Indexed240 is CHAFA_CANVAS_MODE_INDEXED_240.
	Indexed240
	// Indexed16 is CHAFA_CANVAS_MODE_INDEXED_16.
	Indexed16
	// FgBg is CHAFA_CANVAS_MODE_FGBG.
	FgBg
)

// Options configures a render.
type Options struct {
	Cols, Rows int
	Mode       Mode
}

// ErrBadGeometry is returned when the canvas geometry is not positive, matching
// chafa_canvas_new()'s refusal to build a canvas with a non-positive extent.
var ErrBadGeometry = errors.New("chafa: canvas width and height must be positive")

// ErrImageTooLarge is returned when a source dimension exceeds smolscale's
// SMOL_DIM_MAX (65535).
var ErrImageTooLarge = errors.New("chafa: source image dimension exceeds 65535")

const smolDimMax = 65535

// Render draws img into a cols x rows grid of sextant mosaics and returns the
// terminal escape sequence string. Rows are separated by newlines; the last row
// has no trailing newline.
func Render(img image.Image, opts Options) (string, error) {
	if opts.Cols <= 0 || opts.Rows <= 0 {
		return "", ErrBadGeometry
	}
	if opts.Mode < TrueColor || opts.Mode > FgBg {
		return "", errors.New("chafa: unknown mode")
	}

	cv := newCanvas(opts.Mode, opts.Cols, opts.Rows)

	rows, w, h := newRowSource(img)
	if w > smolDimMax || h > smolDimMax {
		return "", ErrImageTooLarge
	}

	if w == 0 || h == 0 {
		cv.maybeClear()
	} else {
		cv.draw(rows, w, h)
	}

	return cv.print(), nil
}

// rowFunc yields source row y as w*4 bytes of unassociated RGBA8.
//
// The C is handed one tightly packed RGBA8 buffer, so the port used to
// materialise the whole image up front. That copy is the single largest
// allocation in a render (94 MB for a 23 MP source) and a whole serial pass
// over memory. Producing rows on demand instead is byte-identical -- the
// scaler reads each source row through exactly this accessor and nothing else
// -- and it also gets the conversion parallelised for free, because the
// scaler's row batches already run on separate goroutines.
//
// The returned slice may alias the source image, so it is read-only and valid
// only until the next call. buf is caller-owned scratch of at least w*4 bytes;
// one buffer per goroutine keeps this race-free.
type rowFunc func(y int, buf []byte) []byte

// newRowSource returns a row accessor for img plus its dimensions.
func newRowSource(img image.Image) (rowFunc, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, 0, 0
	}

	switch src := img.(type) {
	case *image.NRGBA:
		// Already unassociated RGBA8: hand out the source rows directly.
		return func(y int, _ []byte) []byte {
			o := src.PixOffset(b.Min.X, b.Min.Y+y)
			return src.Pix[o : o+w*4 : o+w*4]
		}, w, h

	case *image.RGBA:
		return func(y int, buf []byte) []byte {
			o := src.PixOffset(b.Min.X, b.Min.Y+y)
			row := src.Pix[o : o+w*4]
			dst := buf[:w*4]
			for x := 0; x < w*4; x += 4 {
				a := row[x+3]
				switch a {
				case 0:
					dst[x], dst[x+1], dst[x+2], dst[x+3] = 0, 0, 0, 0
				case 0xff:
					dst[x], dst[x+1], dst[x+2], dst[x+3] = row[x], row[x+1], row[x+2], 0xff
				default:
					dst[x] = unpremul8(row[x], a)
					dst[x+1] = unpremul8(row[x+1], a)
					dst[x+2] = unpremul8(row[x+2], a)
					dst[x+3] = a
				}
			}
			return dst
		}, w, h

	case *image.YCbCr:
		// Chroma is shared by cstep horizontally adjacent pixels, so the three
		// chroma terms of image/color.YCbCrToRGB are computed once per chroma
		// sample instead of once per pixel, and COffset's division is replaced
		// by a walk. The per-pixel arithmetic is otherwise character for
		// character the stdlib function's, so the bytes are identical.
		cstep := 1
		switch src.SubsampleRatio {
		case image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420:
			cstep = 2
		case image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410:
			cstep = 4
		}
		return func(y int, buf []byte) []byte {
			dst := buf[:w*4]
			yo := src.YOffset(b.Min.X, b.Min.Y+y)
			yrow := src.Y[yo : yo+w : yo+w]
			co := src.COffset(b.Min.X, b.Min.Y+y)

			x := 0
			run := cstep - b.Min.X%cstep // pixels sharing the first sample
			for x < w {
				cb1 := int32(src.Cb[co]) - 128
				cr1 := int32(src.Cr[co]) - 128
				rc := 91881 * cr1
				gc := -22554*cb1 - 46802*cr1
				bc := 116130 * cb1
				co++

				end := x + run
				if end > w {
					end = w
				}
				run = cstep

				for ; x < end; x++ {
					yy1 := int32(yrow[x]) * 0x10101

					r := yy1 + rc
					if uint32(r)&0xff000000 == 0 {
						r >>= 16
					} else {
						r = ^(r >> 31)
					}
					g := yy1 + gc
					if uint32(g)&0xff000000 == 0 {
						g >>= 16
					} else {
						g = ^(g >> 31)
					}
					bl := yy1 + bc
					if uint32(bl)&0xff000000 == 0 {
						bl >>= 16
					} else {
						bl = ^(bl >> 31)
					}

					d := dst[x*4 : x*4+4 : x*4+4]
					d[0], d[1], d[2], d[3] = uint8(r), uint8(g), uint8(bl), 0xff
				}
			}
			return dst
		}, w, h

	default:
		return func(y int, buf []byte) []byte {
			dst := buf[:w*4]
			for x := 0; x < w; x++ {
				c := imgcolor.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(imgcolor.NRGBA)
				dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = c.R, c.G, c.B, c.A
			}
			return dst
		}, w, h
	}
}

// unpremul8 matches imgcolor.NRGBAModel's conversion of a premultiplied channel.
func unpremul8(v, a uint8) uint8 {
	// (v16 * 0xffff) / a16 >> 8, with v16 = v * 0x101 and a16 = a * 0x101.
	return uint8((uint32(v) * 0xffff / uint32(a)) >> 8)
}
