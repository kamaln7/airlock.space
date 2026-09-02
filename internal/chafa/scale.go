package chafa

import "sync"

// Port of the smolscale subset chafa 1.19.0 uses on the symbol path:
//
//	src  = SMOL_PIXEL_RGBA8_UNASSOCIATED
//	dest = SMOL_PIXEL_RGBA8_UNASSOCIATED
//	color = opaque background colour, SMOL_COMPOSITE_SRC_OVER_COLOR_SRC_ALPHA
//	flags = SMOL_CLEAR_DEST (work factor 0.5 -> no SMOL_INTERP_NEAREST)
//	placement covers the whole destination, offset 0, whole pixels
//
// Under that configuration smolscale picks:
//
//	storage      = SMOL_STORAGE_128BPP
//	internal a   = SMOL_ALPHA_PREMUL16
//	gamma        = SMOL_GAMMA_SRGB_LINEAR
//	src unpack   = repack_row_1234_32_UNASSOCIATED_COMPRESSED_to_2341_128_PREMUL16_LINEAR
//	pack         = repack_row_1234_128_PREMUL16_LINEAR_to_4123_32_UNASSOCIATED_COMPRESSED
//	composite    = composite_over_color_src_alpha_p16_128bpp
//
// Internal channel order is B, G, R, A (mid_order {3,2,1,4}), laid out as
// two uint64 limbs of two 32-bit lanes: limb0 = [B<<32 | G], limb1 = [R<<32 | A].

const (
	subpixelShift = 8
	subpixelMul   = 1 << subpixelShift

	smallMul = uint64(256)
	bigMul   = uint64(65536)

	boxesMultiplier = bigMul * smallMul
	bilinMultiplier = bigMul * bigMul

	opacityMax = 256

	bilinBoxCutoff = 14
)

type filterType int

const (
	filterCopy filterType = iota
	filterOne
	filterBilinear0H
	filterBilinear1H
	filterBilinear2H
	filterBilinear3H
	filterBox
	filterNearest
)

// smolDim mirrors SmolDim for the subset we need.
type smolDim struct {
	filter filterType

	srcSizePx, srcSizeSpx   uint32
	destSizePx, destSizeSpx uint32

	nHalvings uint32

	placementOfsPx, placementOfsSpx   int32
	placementSizePx, placementSizeSpx uint32
	placementSizePrehalvingPx         uint32
	placementSizePrehalvingSpx        uint32

	spanStep, spanMul uint32

	firstOpacity, lastOpacity uint16

	clearBeforePx, clearAfterPx int32
	clipBeforePx, clipAfterPx   int32

	// precalc storage; only one of these is populated
	precalc16 []uint16
	precalc32 []uint32
}

type scaleCtx struct {
	srcRow     rowFunc // unassociated RGBA8, one source row at a time
	hdim, vdim smolDim

	colorPixel [2]uint64
	clearPixel uint32

	skipHFilter bool
}

func spxToPx(spx int64) int64 { return (spx + subpixelMul - 1) / subpixelMul }

func subpixelMod(n int64) int64 {
	return ((n % subpixelMul) + subpixelMul) % subpixelMul
}

// pickFilterParams is pick_filter_params() with SMOL_INTERP_NEAREST and
// SMOL_DISABLE_SRGB_LINEARIZATION both off (storage is therefore always 128bpp).
//
// Not ported: get_implementations() falls back to SMOL_GAMMA_SRGB_COMPRESSED
// when src_size_spx > MAX (placement_size_spx, 256) * 8191. With whole-pixel
// placements and a cell height/width of 8, that needs a source dimension of
// 65528..65535 px against a one-cell canvas -- a sliver just under
// SMOL_DIM_MAX. Renders in that sliver will diverge.
func pickFilterParams(d *smolDim, srcDim uint32, srcDimSpx uint32, destOfsSpx int32,
	destDim uint32, destDimSpx uint32) {
	d.placementSizePrehalvingPx = destDim

	d.firstOpacity = uint16(subpixelMod(-int64(destOfsSpx)-1) + 1)
	d.lastOpacity = uint16(subpixelMod(int64(destOfsSpx)+int64(destDimSpx)-1) + 1)

	if destDim == 1 {
		d.firstOpacity = uint16(destDimSpx)
		d.lastOpacity = opacityMax
	}

	if destDimSpx == 0 {
		d.placementSizePrehalvingSpx = 0
		d.nHalvings = 0
		d.filter = filterOne
		return
	}

	dspx := uint64(destDimSpx)
	if dspx < subpixelMul {
		dspx = subpixelMul
	}

	switch {
	case uint64(srcDimSpx) > dspx*255:
		d.filter = filterBox
	case uint64(srcDimSpx) >= dspx*bilinBoxCutoff:
		d.filter = filterBox
	case srcDim <= 1:
		d.filter = filterOne
	case (destOfsSpx&0xff) == 0 && srcDimSpx == destDimSpx:
		d.filter = filterCopy
		d.firstOpacity = opacityMax
		d.lastOpacity = opacityMax
	default:
		nHalvings := uint32(0)
		dd := dspx
		for {
			dd *= 2
			if dd >= uint64(srcDimSpx) {
				break
			}
			nHalvings++
		}
		if nHalvings > 3 {
			nHalvings = 3
		}
		d.placementSizePrehalvingPx = destDim << nHalvings
		d.placementSizePrehalvingSpx = destDimSpx << nHalvings
		d.filter = filterBilinear0H + filterType(nHalvings)
		d.nHalvings = nHalvings
	}
}

// initDim is smolscale.c:init_dim().
func initDim(d *smolDim, srcSizeSpx, destSizeSpx uint32, placementOfsSpx, placementSizeSpx int32) {
	*d = smolDim{}
	d.srcSizeSpx = srcSizeSpx
	d.srcSizePx = uint32(spxToPx(int64(srcSizeSpx)))
	d.destSizeSpx = destSizeSpx
	d.destSizePx = uint32(spxToPx(int64(destSizeSpx)))
	d.placementOfsSpx = placementOfsSpx
	d.placementSizeSpx = uint32(placementSizeSpx)

	var placementOfsPx int64
	if placementOfsSpx < 0 {
		placementOfsPx = (int64(placementOfsSpx) - 255) / subpixelMul
	} else {
		placementOfsPx = int64(placementOfsSpx) / subpixelMul
	}
	placementSizePx := spxToPx(int64(placementSizeSpx) + subpixelMod(int64(placementOfsSpx)))

	pickFilterParams(d, d.srcSizePx, d.srcSizeSpx, d.placementOfsSpx,
		uint32(placementSizePx), d.placementSizeSpx)

	visibleFirst := placementOfsPx
	if visibleFirst < 0 {
		visibleFirst = 0
	}
	if visibleFirst > int64(d.destSizePx) {
		visibleFirst = int64(d.destSizePx)
	}
	visibleEnd := placementOfsPx + placementSizePx
	if visibleEnd > int64(d.destSizePx) {
		visibleEnd = int64(d.destSizePx)
	}
	if visibleEnd < visibleFirst {
		visibleEnd = visibleFirst
	}

	d.clearBeforePx = int32(visibleFirst)
	d.clearAfterPx = int32(int64(d.destSizePx) - visibleEnd)
	d.clipBeforePx = int32(visibleFirst - placementOfsPx)
	d.clipAfterPx = int32((placementOfsPx + placementSizePx) - visibleEnd)

	if d.clipBeforePx > 0 {
		d.firstOpacity = opacityMax
	}
	if d.clipAfterPx > 0 {
		d.lastOpacity = opacityMax
	}

	d.placementOfsPx = int32(visibleFirst)
	d.placementSizePx = uint32(visibleEnd - visibleFirst)
}

// ---------------------------------------------------------------- precalc

// precalcLinearRange is smolscale-generic.c:precalc_linear_range().
func precalcLinearRange(out []uint16, firstIndex, lastIndex int64,
	firstSampleOfs, sampleStep uint64, sampleOfsPxMax int,
	clipFirstIndex, clipLastIndex int64, arrayI *int) {
	if firstIndex < clipFirstIndex {
		firstSampleOfs += sampleStep * uint64(clipFirstIndex-firstIndex)
		firstIndex = clipFirstIndex
	}
	if lastIndex > clipLastIndex {
		lastIndex = clipLastIndex
	}

	sampleOfs := firstSampleOfs

	for i := firstIndex; i < lastIndex; i++ {
		sampleOfsPx := sampleOfs / bilinMultiplier

		if sampleOfsPx >= uint64(sampleOfsPxMax)-1 {
			out[(*arrayI)*2] = uint16(sampleOfsPxMax - 2)
			out[(*arrayI)*2+1] = 0
			*arrayI++
			continue
		}

		out[(*arrayI)*2] = uint16(sampleOfsPx)
		out[(*arrayI)*2+1] = uint16(smallMul - ((sampleOfs / (bilinMultiplier / smallMul)) % smallMul))
		*arrayI++

		sampleOfs += sampleStep
	}
}

// precalcBilinearArray is smolscale-generic.c:precalc_bilinear_array().
func precalcBilinearArray(array []uint16, srcDimSpx, destOfsSpx, destDimSpx uint64,
	destDimPrehalvingPx uint32, nHalvings uint32,
	destClipBeforePx, destVisiblePx int32) {
	srcDimPx := uint32(spxToPx(int64(srcDimSpx)))
	var firstSampleOfs [3]uint64
	var sampleStep uint64

	clipFirst := int64(destClipBeforePx) << nHalvings
	clipLast := clipFirst + (int64(destVisiblePx) << nHalvings)
	i := 0

	destOfsSpx %= subpixelMul

	if srcDimSpx > destDimSpx {
		sampleStep = (srcDimSpx * bilinMultiplier) / destDimSpx
		firstSampleOfs[0] = (sampleStep - bilinMultiplier) / 2
		firstSampleOfs[1] = ((sampleStep - bilinMultiplier) / 2) +
			((sampleStep * (subpixelMul - destOfsSpx) * (1 << nHalvings)) / subpixelMul)
	} else {
		den := uint64(1)
		if destDimSpx > subpixelMul {
			den = destDimSpx - subpixelMul
		}
		sampleStep = ((srcDimSpx - subpixelMul) * bilinMultiplier) / den
		firstSampleOfs[0] = 0
		firstSampleOfs[1] = (sampleStep * (subpixelMul - destOfsSpx) * (1 << nHalvings)) / subpixelMul
	}

	firstSampleOfs[2] = (((srcDimSpx*bilinMultiplier*2)/subpixelMul)+sampleStep-bilinMultiplier)/2 -
		sampleStep*(1<<nHalvings)

	precalcLinearRange(array, 0, 1<<nHalvings, firstSampleOfs[0], sampleStep,
		int(srcDimPx), clipFirst, clipLast, &i)

	if destDimPrehalvingPx > (1 << nHalvings) {
		precalcLinearRange(array, 1<<nHalvings, int64(destDimPrehalvingPx)-(1<<nHalvings),
			firstSampleOfs[1], sampleStep, int(srcDimPx), clipFirst, clipLast, &i)

		precalcLinearRange(array, int64(destDimPrehalvingPx)-(1<<nHalvings), int64(destDimPrehalvingPx),
			firstSampleOfs[2], sampleStep, int(srcDimPx), clipFirst, clipLast, &i)
	}
}

// precalcBoxesArray is smolscale-generic.c:precalc_boxes_array().
func precalcBoxesArray(array []uint32, spanStep, spanMul *uint32,
	srcDimSpx uint32, destDim uint32, destOfsSpx uint32, destDimSpx uint32,
	destClipBeforePx, destVisiblePx int32) {
	destOfsSpx %= subpixelMul

	if destDimSpx < 256 {
		destDimSpx = 256
	}

	fracStepF := (uint64(srcDimSpx) * bigMul) / uint64(destDimSpx)

	stride := fracStepF / bigMul
	f := (fracStepF / smallMul) % smallMul

	a := boxesMultiplier * 255
	b := (stride * 255) + ((f * 255) / 256)
	*spanStep = uint32(fracStepF / smallMul)
	*spanMul = uint32(a / (b + 1))

	lastOfs := (uint64(srcDimSpx)*smallMul - fracStepF) / smallMul

	clipFirst := int64(destClipBeforePx)
	clipLast := clipFirst + int64(destVisiblePx)
	i := 0

	if clipFirst == 0 && clipLast > 0 {
		array[i] = 0
		i++
	}

	mainFirst := clipFirst
	if mainFirst < 1 {
		mainFirst = 1
	}
	mainLast := clipLast
	if mainLast > int64(destDim)-1 {
		mainLast = int64(destDim) - 1
	}
	fracF := ((fracStepF * uint64(subpixelMul-destOfsSpx)) / subpixelMul) +
		uint64(mainFirst-1)*fracStepF
	for destI := mainFirst; destI < mainLast; destI++ {
		v := fracF / smallMul
		if v > lastOfs {
			v = lastOfs
		}
		array[i] = uint32(v)
		i++
		fracF += fracStepF
	}

	if destDim > 1 && int64(destDim)-1 >= clipFirst && int64(destDim)-1 < clipLast {
		array[i] = uint32(lastOfs)
		i++
	}
}

func precalcEntriesForDim(d *smolDim) int {
	return int(d.placementSizePx<<d.nHalvings) + 1
}

func initDimPrecalc(d *smolDim) {
	switch d.filter {
	case filterOne, filterCopy:
		// no precalc
	case filterBox:
		d.precalc32 = make([]uint32, precalcEntriesForDim(d))
		precalcBoxesArray(d.precalc32, &d.spanStep, &d.spanMul,
			d.srcSizeSpx, d.placementSizePrehalvingPx, uint32(d.placementOfsSpx),
			d.placementSizeSpx, d.clipBeforePx, int32(d.placementSizePx))
	default:
		d.precalc16 = make([]uint16, precalcEntriesForDim(d)*2)
		precalcBilinearArray(d.precalc16, uint64(d.srcSizeSpx), uint64(d.placementOfsSpx),
			uint64(d.placementSizePrehalvingSpx), d.placementSizePrehalvingPx,
			d.nHalvings, d.clipBeforePx, int32(d.placementSizePx))
	}
}

// ------------------------------------------------------------ repacking

const (
	lane24Mask = uint64(0x00ffffff00ffffff)
	lane11Mask = uint64(0x000007ff000007ff)
)

// unpackPixelA234UTo234AP16L is unpack_pixel_a234_u_to_234a_p16l_128bpp().
// p is the source pixel read as a host uint32 (little endian: R | G<<8 | B<<16 | A<<24).
func unpackPixelA234UTo234AP16L(p uint32, out *[2]uint64) {
	alpha := uint8(p >> 24)

	out[0] = uint64(fromSRGBLut[(p>>16)&0xff])<<32 | uint64(fromSRGBLut[(p>>8)&0xff])
	out[1] = uint64(fromSRGBLut[p&0xff])<<32 | 0xff

	if alpha == 0xff {
		out[0] <<= 8
		out[1] <<= 8
	} else {
		out[0] *= uint64(alpha) + 1
		out[1] *= uint64(alpha) + 1
	}

	out[1] = (out[1] & 0xffffffff00000000) | (uint64(alpha) << 8) | 0xff
}

func unpremulP16LToUL(in *[2]uint64, out *[2]uint64, alpha uint8) {
	m := uint64(invDivP16LLut[alpha])
	out[0] = ((in[0] * m) >> (30 - 11)) & lane11Mask
	out[1] = ((in[1] * m) >> (30 - 11)) & lane11Mask
}

func toSRGBPixelXXXA(in *[2]uint64, out *[2]uint64) {
	out[0] = uint64(toSRGBLut[in[0]>>32])<<32 | uint64(toSRGBLut[in[0]&0xffff])
	out[1] = uint64(toSRGBLut[in[1]>>32]) << 32
}

// packPixelP16LTo4123U is pack_pixel_p16l_to_4123_u_128bpp().
func packPixelP16LTo4123U(in *[2]uint64) uint32 {
	alpha := uint8(in[1] >> 8)
	var t [2]uint64

	if alpha == 0xff {
		t[0] = (in[0] >> 8) & lane11Mask
		t[1] = (in[1] >> 8) & lane11Mask
	} else {
		unpremulP16LToUL(in, &t, alpha)
	}

	toSRGBPixelXXXA(&t, &t)
	t[1] = (t[1] & 0xffffffff00000000) | uint64(alpha)

	// PACK_FROM_1234_128BPP(t, 4, 1, 2, 3):
	//   byte3 <- ch4 (t[1] low lane), byte2 <- ch1 (t[0] high lane),
	//   byte1 <- ch2 (t[0] low lane),  byte0 <- ch3 (t[1] high lane)
	return uint32((t[1]<<24)&0xff000000) |
		uint32((t[0]>>16)&0x00ff0000) |
		uint32((t[0]<<8)&0x0000ff00) |
		uint32((t[1]>>32)&0x000000ff)
}

// compositeOverColorSrcAlphaP16 is composite_over_color_src_alpha_p16_128bpp_span()
// with opacity == SMOL_OPACITY_MAX.
func compositeOverColorSrcAlphaP16(src, dst []uint64, color *[2]uint64, nPixels int) {
	for i := 0; i < nPixels*2; i += 2 {
		s0 := src[i]
		s1 := src[i+1]

		a := (s1 >> 8) & 0xff
		nz := (a + 0xff) >> 8
		w := 0x100 - a - nz

		s0 = s0*nz + (((color[0]*w + 0x0000008000000080) >> 8) & lane24Mask)
		s1 = s1*nz + (((color[1]*w + 0x0000008000000080) >> 8) & lane24Mask)

		s0 = ((s0 >> 8) & 0x0000ffff0000ffff) * (a + 1)
		s1 = ((s1 >> 8) & 0x0000ffff0000ffff) * (a + 1)

		dst[i] = s0
		dst[i+1] = (s1 & 0xffffffff00000000) | (a << 8) | 0xff
	}
}

// ---------------------------------------------------- horizontal filters

func (sc *scaleCtx) hfilter(srcParts, destParts []uint64) {
	h := &sc.hdim
	n := int(h.placementSizePx)

	switch h.filter {
	case filterCopy:
		copy(destParts[:n*2], srcParts[int(h.clipBeforePx)*2:int(h.clipBeforePx)*2+n*2])

	case filterOne:
		for i := 0; i < n; i++ {
			destParts[i*2] = srcParts[0]
			destParts[i*2+1] = srcParts[1]
		}

	case filterBox:
		pre := h.precalc32
		for i := 0; i < n; i++ {
			ofs0, _, f0, f1, cnt := unpackBoxPrecalc(pre[i], h.spanStep)
			pp := int(ofs0) * 2
			var accum [2]uint64
			accum[0] = ((srcParts[pp] * uint64(f0)) >> 8) & lane24Mask
			accum[1] = ((srcParts[pp+1] * uint64(f0)) >> 8) & lane24Mask
			pp += 2
			for k := uint32(0); k < cnt; k++ {
				accum[0] += srcParts[pp]
				accum[1] += srcParts[pp+1]
				pp += 2
			}
			accum[0] += ((srcParts[pp] * uint64(f1)) >> 8) & lane24Mask
			accum[1] += ((srcParts[pp+1] * uint64(f1)) >> 8) & lane24Mask

			destParts[i*2] = scale128bppHalf(accum[0], uint64(h.spanMul))
			destParts[i*2+1] = scale128bppHalf(accum[1], uint64(h.spanMul))
		}

	case filterNearest:
		pre := h.precalc16
		for i := 0; i < n; i++ {
			ofs := int(pre[i]) * 2
			destParts[i*2] = srcParts[ofs]
			destParts[i*2+1] = srcParts[ofs+1]
		}

	default: // bilinear
		nh := h.nHalvings
		taps := 1 << nh
		pre := h.precalc16
		pi := 0
		for i := 0; i < n; i++ {
			var a0, a1 uint64
			for k := 0; k < taps; k++ {
				// Slice with a fixed length so each tap costs one bounds
				// check rather than six.
				pr := pre[pi : pi+2 : pi+2]
				pi += 2
				ofs := int(pr[0]) * 2
				F := uint64(pr[1])
				sp := srcParts[ofs : ofs+4 : ofs+4]

				p, q := sp[0], sp[2]
				a0 += ((((p - q) * F) >> 8) + q) & lane24Mask

				p, q = sp[1], sp[3]
				a1 += ((((p - q) * F) >> 8) + q) & lane24Mask
			}
			d := destParts[i*2 : i*2+2 : i*2+2]
			d[0] = (a0 >> nh) & lane24Mask
			d[1] = (a1 >> nh) & lane24Mask
		}
	}

	sc.applyHorizEdgeOpacity(destParts)
}

func (sc *scaleCtx) applyHorizEdgeOpacity(destParts []uint64) {
	h := &sc.hdim
	applySubpixelOpacity128(destParts[0:2], uint64(h.firstOpacity))
	o := int(h.placementSizePx-1) * 2
	applySubpixelOpacity128(destParts[o:o+2], uint64(h.lastOpacity))
}

func applySubpixelOpacity128(p []uint64, opacity uint64) {
	p[0] = ((p[0] * opacity) >> subpixelShift) & lane24Mask
	p[1] = ((p[1] * opacity) >> subpixelShift) & lane24Mask
}

func unpackBoxPrecalc(precalc, step uint32) (ofs0, ofs1, f0, f1, n uint32) {
	ofs0 = precalc
	ofs1 = ofs0 + step
	f0 = 256 - (ofs0 % subpixelMul)
	f1 = ofs1 % subpixelMul
	ofs0 /= subpixelMul
	ofs1 /= subpixelMul
	n = ofs1 - ofs0 - 1
	return
}

func scale128bppHalf(accum, multiplier uint64) uint64 {
	a := accum & 0x00000000ffffffff
	a = (a*multiplier + boxesMultiplier/2) / boxesMultiplier

	b := (accum & 0xffffffff00000000) >> 32
	b = (b*multiplier + boxesMultiplier/2) / boxesMultiplier

	return a | (b << 32)
}

// ------------------------------------------------------ vertical scaling

type localCtx struct {
	srcOfs   uint32
	hasSrc   bool
	partsRow [4][]uint64
	destRow  []uint64
	rowBuf   []byte // scratch for srcRow; per-localCtx, so per-goroutine
}

// localCtxPool recycles the per-worker scratch across renders. It is by far
// the largest thing a render allocates -- tens of kilobytes per worker, and
// one worker per core -- and successive renders want the same shapes.
var localCtxPool sync.Pool

func (sc *scaleCtx) newLocalCtx() *localCtx {
	n := int(sc.hdim.srcSizePx) + 1
	if int(sc.hdim.placementSizePx) > n {
		n = int(sc.hdim.placementSizePx)
	}

	lc, _ := localCtxPool.Get().(*localCtx)
	if lc == nil {
		lc = &localCtx{}
	}
	lc.srcOfs, lc.hasSrc = 0, false

	for i := range lc.partsRow {
		lc.partsRow[i] = growU64(lc.partsRow[i], n*2)

		// unpackRow fills the first srcSizePx pixels of the row; the box
		// horizontal filter can read one pixel past that. Freshly allocated
		// memory made those two limbs zero, and a recycled buffer has to keep
		// them zero or the output would depend on the previous render.
		lc.partsRow[i][sc.hdim.srcSizePx*2] = 0
		lc.partsRow[i][sc.hdim.srcSizePx*2+1] = 0
	}
	lc.destRow = growU64(lc.destRow, maxInt(int(sc.hdim.placementSizePx), 1)*2)
	lc.rowBuf = growBytes(lc.rowBuf, int(sc.hdim.srcSizePx)*4)
	return lc
}

func (lc *localCtx) release() { localCtxPool.Put(lc) }

func growU64(s []uint64, n int) []uint64 {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]uint64, n)
}

func growBytes(s []byte, n int) []byte {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]byte, n)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// scaleHorizontal is smolscale-generic.c:scale_horizontal().
func (sc *scaleCtx) scaleHorizontal(lc *localCtx, srcRow int, destParts []uint64) {
	row := sc.srcRow(srcRow, lc.rowBuf)

	if sc.skipHFilter {
		unpackRow(row, destParts, int(sc.hdim.srcSizePx))
		sc.applyHorizEdgeOpacity(destParts)
		return
	}

	unpacked := lc.partsRow[3]
	unpackRow(row, unpacked, int(sc.hdim.srcSizePx))
	sc.hfilter(unpacked, destParts)
}

func unpackRow(src []byte, dest []uint64, n int) {
	src = src[:n*4]
	dest = dest[:n*2]
	for i := 0; i < n; i++ {
		s := src[i*4 : i*4+4 : i*4+4]
		d := dest[i*2 : i*2+2 : i*2+2]

		// Opaque pixels are the overwhelmingly common case (every pixel of a
		// jpeg or a solid png). unpackPixelA234UTo234AP16L's alpha == 0xff
		// branch collapses to two shifts once constant-folded, so it is spelt
		// out here; the values are identical to the general path below.
		if s[3] == 0xff {
			d[0] = uint64(fromSRGBLut[s[2]])<<40 | uint64(fromSRGBLut[s[1]])<<8
			d[1] = uint64(fromSRGBLut[s[0]])<<40 | 0xffff
			continue
		}

		alpha := uint64(s[3])
		d0 := uint64(fromSRGBLut[s[2]])<<32 | uint64(fromSRGBLut[s[1]])
		d1 := uint64(fromSRGBLut[s[0]])<<32 | 0xff
		d0 *= alpha + 1
		d1 *= alpha + 1
		d[0] = d0
		d[1] = (d1 & 0xffffffff00000000) | (alpha << 8) | 0xff
	}
}

func (sc *scaleCtx) updateLocalCtxBilinear(lc *localCtx, destRowIndex uint32) {
	precalcY := sc.vdim.precalc16
	newSrcOfs := uint32(precalcY[destRowIndex*2])

	if lc.hasSrc && newSrcOfs == lc.srcOfs {
		return
	}

	if lc.hasSrc && newSrcOfs == lc.srcOfs+1 {
		lc.partsRow[0], lc.partsRow[1] = lc.partsRow[1], lc.partsRow[0]
		sc.scaleHorizontal(lc, int(newSrcOfs+1), lc.partsRow[1])
	} else {
		sc.scaleHorizontal(lc, int(newSrcOfs), lc.partsRow[0])
		sc.scaleHorizontal(lc, int(newSrcOfs+1), lc.partsRow[1])
	}

	lc.srcOfs = newSrcOfs
	lc.hasSrc = true
}

func interpVerticalBilinearStore(F uint64, top, bottom, dest []uint64, width int) {
	for i := 0; i < width; i++ {
		p := top[i]
		q := bottom[i]
		dest[i] = ((((p - q) * F) >> 8) + q) & lane24Mask
	}
}

func interpVerticalBilinearAdd(F uint64, top, bottom, accum []uint64, width int) {
	for i := 0; i < width; i++ {
		p := top[i]
		q := bottom[i]
		accum[i] += ((((p - q) * F) >> 8) + q) & lane24Mask
	}
}

func interpVerticalBilinearFinal(F uint64, top, bottom, accum []uint64, width int, nHalvings uint32) {
	for i := 0; i < width; i++ {
		p := top[i]
		q := bottom[i]
		p = ((((p - q) * F) >> 8) + q) & lane24Mask
		p = ((p + accum[i]) >> nHalvings) & lane24Mask
		accum[i] = p
	}
}

func applySubpixelOpacityRow(row []uint64, width int, opacity uint64) {
	for i := 0; i < width; i++ {
		row[i] = ((row[i] * opacity) >> subpixelShift) & lane24Mask
	}
}

// scaleDestRow runs the vertical filter and returns the index of the parts row
// holding the result, plus whether that row is a cached row (i.e. must not be
// composited in place).
func (sc *scaleCtx) scaleDestRow(lc *localCtx, destRowIndex uint32) (int, bool) {
	v := &sc.vdim
	width := int(sc.hdim.placementSizePx) * 2

	switch v.filter {
	case filterCopy:
		sc.scaleHorizontal(lc, int(destRowIndex)+int(v.clipBeforePx), lc.partsRow[0])
		return 0, false

	case filterOne:
		if !lc.hasSrc || lc.srcOfs != 0 {
			sc.scaleHorizontal(lc, 0, lc.partsRow[0])
			lc.srcOfs = 0
			lc.hasSrc = true
		}
		if destRowIndex == 0 && v.firstOpacity < opacityMax {
			copy(lc.partsRow[1][:width], lc.partsRow[0][:width])
			applySubpixelOpacityRow(lc.partsRow[1], width, uint64(v.firstOpacity))
			return 1, true
		}
		if destRowIndex == v.placementSizePx-1 && v.lastOpacity < opacityMax {
			copy(lc.partsRow[1][:width], lc.partsRow[0][:width])
			applySubpixelOpacityRow(lc.partsRow[1], width, uint64(v.lastOpacity))
			return 1, true
		}
		return 0, true

	case filterNearest:
		srcRowOfs := uint32(v.precalc16[destRowIndex])
		if !lc.hasSrc || srcRowOfs != lc.srcOfs {
			sc.scaleHorizontal(lc, int(srcRowOfs), lc.partsRow[0])
			lc.srcOfs = srcRowOfs
			lc.hasSrc = true
		}
		return 0, true

	case filterBox:
		sc.scaleDestRowBox(lc, destRowIndex)
		return 2, false

	default: // bilinear
		nh := v.nHalvings
		precalcY := v.precalc16
		bilinIndex := destRowIndex << nh

		sc.updateLocalCtxBilinear(lc, bilinIndex)
		if nh == 0 {
			F := uint64(precalcY[bilinIndex*2+1])
			interpVerticalBilinearStore(F, lc.partsRow[0], lc.partsRow[1], lc.partsRow[2], width)
			if destRowIndex == 0 && v.firstOpacity < opacityMax {
				applySubpixelOpacityRow(lc.partsRow[2], width, uint64(v.firstOpacity))
			} else if destRowIndex == v.placementSizePx-1 && v.lastOpacity < opacityMax {
				applySubpixelOpacityRow(lc.partsRow[2], width, uint64(v.lastOpacity))
			}
			return 2, false
		}

		interpVerticalBilinearStore(uint64(precalcY[bilinIndex*2+1]),
			lc.partsRow[0], lc.partsRow[1], lc.partsRow[2], width)
		bilinIndex++

		for i := 0; i < (1<<nh)-2; i++ {
			sc.updateLocalCtxBilinear(lc, bilinIndex)
			interpVerticalBilinearAdd(uint64(precalcY[bilinIndex*2+1]),
				lc.partsRow[0], lc.partsRow[1], lc.partsRow[2], width)
			bilinIndex++
		}

		sc.updateLocalCtxBilinear(lc, bilinIndex)
		interpVerticalBilinearFinal(uint64(precalcY[bilinIndex*2+1]),
			lc.partsRow[0], lc.partsRow[1], lc.partsRow[2], width, nh)

		if destRowIndex == 0 && v.firstOpacity < opacityMax {
			applySubpixelOpacityRow(lc.partsRow[2], width, uint64(v.firstOpacity))
		} else if destRowIndex == v.placementSizePx-1 && v.lastOpacity < opacityMax {
			applySubpixelOpacityRow(lc.partsRow[2], width, uint64(v.lastOpacity))
		}
		return 2, false
	}
}

func (sc *scaleCtx) scaleDestRowBox(lc *localCtx, destRowIndex uint32) {
	v := &sc.vdim
	nPx := int(sc.hdim.placementSizePx)

	ofsY, _, w1, w2, n := unpackBoxPrecalc(v.precalc32[destRowIndex], v.spanStep)

	if !lc.hasSrc || ofsY != lc.srcOfs {
		sc.scaleHorizontal(lc, int(ofsY), lc.partsRow[0])
		lc.srcOfs = ofsY
		lc.hasSrc = true
	}
	// copy_weighted_parts_128bpp
	for i := 0; i < nPx*2; i++ {
		lc.partsRow[1][i] = ((lc.partsRow[0][i] * uint64(w1)) >> 8) & lane24Mask
	}
	ofsY++

	for i := uint32(0); i < n; i++ {
		sc.scaleHorizontal(lc, int(ofsY), lc.partsRow[0])
		lc.srcOfs = ofsY
		for j := 0; j < nPx*2; j++ {
			lc.partsRow[1][j] += lc.partsRow[0][j]
		}
		ofsY++
	}

	if w2 > 0 && ofsY < v.srcSizePx {
		sc.scaleHorizontal(lc, int(ofsY), lc.partsRow[0])
		lc.srcOfs = ofsY
		for j := 0; j < nPx*2; j++ {
			lc.partsRow[1][j] += ((lc.partsRow[0][j] * uint64(w2)) >> 8) & lane24Mask
		}
	}

	// finalize_vertical_128bpp
	for i := 0; i < nPx*2; i++ {
		lc.partsRow[2][i] = scale128bppHalf(lc.partsRow[1][i], uint64(v.spanMul))
	}

	if destRowIndex == 0 && v.firstOpacity < opacityMax {
		applySubpixelOpacityRow(lc.partsRow[2], nPx*2, uint64(v.firstOpacity))
	} else if destRowIndex == v.placementSizePx-1 && v.lastOpacity < opacityMax {
		applySubpixelOpacityRow(lc.partsRow[2], nPx*2, uint64(v.lastOpacity))
	}
}

// ------------------------------------------------------------- top level

// newScaleCtx mirrors smol_scale_new_full() for our fixed configuration.
// The placement always covers the whole destination.
func newScaleCtx(src rowFunc, srcW, srcH int, destW, destH int, color [4]uint8) *scaleCtx {
	sc := &scaleCtx{srcRow: src}

	initDim(&sc.hdim, uint32(srcW*subpixelMul), uint32(destW*subpixelMul), 0, int32(destW*subpixelMul))
	initDim(&sc.vdim, uint32(srcH*subpixelMul), uint32(destH*subpixelMul), 0, int32(destH*subpixelMul))

	if sc.hdim.filter == filterCopy && sc.hdim.clipBeforePx == 0 &&
		sc.hdim.placementSizePx == sc.hdim.srcSizePx {
		sc.skipHFilter = true
	}

	initDimPrecalc(&sc.hdim)
	initDimPrecalc(&sc.vdim)

	// The composite colour is converted to the internal representation with
	// the same repack as the source rows (SRC_ALPHA forces its alpha opaque).
	p := uint32(color[0]) | uint32(color[1])<<8 | uint32(color[2])<<16 | uint32(0xff)<<24
	var cp [2]uint64
	unpackPixelA234UTo234AP16L(p, &cp)
	sc.colorPixel = cp

	var srcPx, clearPx [2]uint64
	compositeOverColorSrcAlphaP16(srcPx[:], clearPx[:], &sc.colorPixel, 1)
	sc.clearPixel = packPixelP16LTo4123U(&clearPx)

	return sc
}

// scaleRows renders destination rows [first, first+n) into out, which must hold
// n rows of destW RGBA8 pixels. lc carries the per-goroutine scratch and the
// source-row cache; reusing one across calls is safe because every cached
// value is re-checked against the row it is about to be used for.
func (sc *scaleCtx) scaleRows(lc *localCtx, out []uint8, destW int, first, n int) {
	nPx := int(sc.hdim.placementSizePx)

	for r := first; r < first+n; r++ {
		rowOut := out[(r-first)*destW*4:]

		if uint32(r) < uint32(sc.vdim.clearBeforePx) ||
			uint32(r) >= sc.vdim.destSizePx-uint32(sc.vdim.clearAfterPx) {
			sc.clearRow(rowOut, destW)
			continue
		}

		sc.clearRow(rowOut, int(sc.hdim.clearBeforePx))

		idx, cached := sc.scaleDestRow(lc, uint32(r)-uint32(sc.vdim.clearBeforePx))
		outRow := lc.partsRow[idx]

		dst := outRow
		if cached {
			dst = lc.destRow
		}
		compositeOverColorSrcAlphaP16(outRow, dst, &sc.colorPixel, nPx)

		base := int(sc.hdim.placementOfsPx) * 4
		for i := 0; i < nPx; i++ {
			v := packPixelP16LTo4123U(&[2]uint64{dst[i*2], dst[i*2+1]})
			rowOut[base+i*4] = uint8(v)
			rowOut[base+i*4+1] = uint8(v >> 8)
			rowOut[base+i*4+2] = uint8(v >> 16)
			rowOut[base+i*4+3] = uint8(v >> 24)
		}

		if sc.hdim.clearAfterPx > 0 {
			sc.clearRow(rowOut[(int(sc.hdim.placementOfsPx)+nPx)*4:], int(sc.hdim.clearAfterPx))
		}
	}
}

// clearRow fills n pixels with the composite colour at zero coverage, which is
// what populate_clear_batch() derives for the SRC_ALPHA op.
func (sc *scaleCtx) clearRow(out []uint8, n int) {
	v := sc.clearPixel
	for i := 0; i < n; i++ {
		out[i*4] = uint8(v)
		out[i*4+1] = uint8(v >> 8)
		out[i*4+2] = uint8(v >> 16)
		out[i*4+3] = uint8(v >> 24)
	}
}
