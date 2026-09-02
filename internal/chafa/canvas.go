package chafa

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
)

const (
	symbolErrorMax = math.MaxInt32 / 8

	fixedMult    = 4096
	intensityMax = 256 * 8

	indexed16CropPct = 5
	indexed2CropPct  = 20

	alphaThreshold = 127
	workFactorInt  = 5 // config.work_factor 0.5f * 10 + 0.5f
)

type colorPair struct {
	colors [2]color // [0] = BG, [1] = FG
}

type canvasCell struct {
	c       rune
	fgColor uint32
	bgColor uint32
}

type canvas struct {
	mode Mode

	width, height             int
	widthPixels, heightPixels int

	cells []canvasCell

	fgPalette *palette
	bgPalette *palette

	defaultColors colorPair

	considerInverted bool
	extractColors    bool
	fgOnly           bool

	blankChar rune

	pixels []uint8 // scaled RGBA8, widthPixels*heightPixels*4
}

func newCanvas(mode Mode, cols, rows int) *canvas {
	cv := &canvas{
		mode:         mode,
		width:        cols,
		height:       rows,
		widthPixels:  cols * symbolWidthPixels,
		heightPixels: rows * symbolHeightPixels,
	}
	cv.cells = make([]canvasCell, cols*rows)

	cv.considerInverted = mode != FgBg
	cv.extractColors = mode != FgBg
	cv.fgOnly = mode == FgBg

	// update_display_colors(): fg 0xffffff, bg 0x000000, RGB colour space.
	// The fg_only stand-in grey is only applied when extract_colors is set,
	// which never coincides with fg_only here.
	fg := unpackColor(0xffffff)
	bg := unpackColor(0x000000)
	fg.ch[3] = 0xff
	bg.ch[3] = 0x00
	cv.defaultColors.colors[1] = fg
	cv.defaultColors.colors[0] = bg

	var pt paletteType
	switch mode {
	case TrueColor:
		pt = paletteDynamic256
	case Indexed240:
		pt = paletteFixed240
	case Indexed16:
		pt = paletteFixed16
	default:
		pt = paletteFixedFGBG
	}
	cv.fgPalette = newPalette(pt, fg, bg, alphaThreshold)
	cv.bgPalette = newPalette(pt, fg, bg, alphaThreshold)

	// find_best_blank_char(): the sextant map contains U+0020, so it returns
	// it immediately. find_best_solid_char() likewise returns U+2588, but the
	// solid char is only consulted on the INDEXED_16_8 and wide-symbol paths,
	// neither of which is reachable here.
	cv.blankChar = 0x20

	return cv
}

// ------------------------------------------------------- pixel preparation

// draw is chafa_canvas_draw_all_pixels()'s prepare_pixel_data() followed by
// update_cells().
//
// One departure from the C: it always accumulates the intensity histogram
// while scaling, then discards it unless the mode normalizes (INDEXED_16 and
// FGBG). Here it is only built when it will be read, which cannot change the
// output because nothing else looks at it.
func (cv *canvas) draw(src rowFunc, srcW, srcH int) {
	// Recycled, not freshly allocated: scaleRows writes every byte of every
	// destination row -- the cleared margins included -- so nothing here can
	// observe what the previous render left behind, and we save both the
	// allocation and its zeroing.
	cv.pixels = getPixelBuf(cv.widthPixels * cv.heightPixels * 4)
	defer putPixelBuf(cv.pixels)

	// bg_color_rgb = fg_palette colour at CHAFA_PALETTE_INDEX_BG, forced opaque.
	bgc := cv.fgPalette.getColor(paletteIndexBG)
	sc := newScaleCtx(src, srcW, srcH, cv.widthPixels, cv.heightPixels,
		[4]uint8{bgc.ch[0], bgc.ch[1], bgc.ch[2], 0xff})

	boost := cv.mode == Indexed16
	normalize := cv.mode == Indexed16 || cv.mode == FgBg

	stride := cv.widthPixels * 4
	lcs := make([]*localCtx, runtime.NumCPU())
	defer func() {
		for _, lc := range lcs {
			if lc != nil {
				lc.release()
			}
		}
	}()
	scale := func(w, first, n int) {
		lc := lcs[w]
		if lc == nil {
			lc = sc.newLocalCtx()
			lcs[w] = lc
		}
		sc.scaleRows(lc, cv.pixels[first*stride:], cv.widthPixels, first, n)
	}

	if !normalize {
		parallelRows(cv.heightPixels, scale)
		cv.updateCells()
		return
	}

	// Pass 1: scale, saturation boost, histogram.
	hists := make([][intensityMax]int32, len(lcs))
	nSamples := make([]int, len(lcs))

	parallelRows(cv.heightPixels, func(w, first, n int) {
		scale(w, first, n)

		p := cv.pixels[first*stride : (first+n)*stride]
		hist := &hists[w]
		ns := 0
		for i := 0; i < len(p); i += 4 {
			if boost {
				boostSaturationRGB(p[i : i+4])
			}
			if p[i+3] > 127 {
				v := int(p[i])*3 + int(p[i+1])*4 + int(p[i+2])
				hist[v]++
				ns++
			}
		}
		nSamples[w] += ns
	})

	var total [intensityMax]int32
	nTotal := 0
	for w := range hists {
		nTotal += nSamples[w]
		for i := 0; i < intensityMax; i++ {
			total[i] += hists[w][i]
		}
	}

	cropPct := indexed2CropPct
	if cv.mode == Indexed16 {
		cropPct = indexed16CropPct
	}
	hmin, hmax := histogramCalcBounds(&total, nTotal, cropPct)

	if hmin != hmax {
		factor := ((intensityMax - 1) * fixedMult) / (hmax - hmin)
		minv := hmin / 8

		parallelRows(cv.heightPixels, func(_, first, n int) {
			p := cv.pixels[first*stride : (first+n)*stride]
			for i := 0; i < len(p); i += 4 {
				p[i] = normalizeCh(p[i], minv, factor)
				p[i+1] = normalizeCh(p[i+1], minv, factor)
				p[i+2] = normalizeCh(p[i+2], minv, factor)
			}
		})
	}

	cv.updateCells()
}

func (cv *canvas) updateCells() {
	parallelRows(cv.height, func(_, first, n int) {
		for i := 0; i < n; i++ {
			cv.updateCellsRow(first + i)
		}
	})
}

// pixelBufPool recycles the scaled-pixel buffer across renders.
var pixelBufPool sync.Pool

func getPixelBuf(n int) []uint8 {
	if b, _ := pixelBufPool.Get().(*[]uint8); b != nil && cap(*b) >= n {
		return (*b)[:n]
	}
	return make([]uint8, n)
}

func putPixelBuf(b []uint8) { pixelBufPool.Put(&b) }

// parallelRows runs fn over row chunks covering [0, nRows) on up to NumCPU
// goroutines, handing chunks out dynamically. fn gets a worker index below
// NumCPU that no other goroutine is using concurrently, so per-worker scratch
// can be kept in a slice indexed by it.
//
// The C reproduces chafa_process_batches(), which cuts one equal batch per
// thread up front. That is a poor fit for a machine with fast and slow cores:
// the equal batch landing on a slow core holds every fast core idle at the
// join. Handing out several small chunks per worker instead lets the fast
// cores take more of them. Chunk boundaries are not observable in the output:
// each destination row is scaled independently of every other, cell rows are
// independent, and histogram accumulation is integer addition.
func parallelRows(nRows int, fn func(worker, first, n int)) {
	if nRows <= 0 {
		return
	}

	nw := runtime.NumCPU()
	// Several chunks per worker for balance, but not so many that the
	// per-chunk cache reset in the scaler starts to cost more than it saves.
	chunk := (nRows + nw*chunksPerWorker - 1) / (nw * chunksPerWorker)
	if chunk < 1 {
		chunk = 1
	}
	nChunks := (nRows + chunk - 1) / chunk
	if nw > nChunks {
		nw = nChunks
	}
	if nw <= 1 {
		fn(0, 0, nRows)
		return
	}

	var next atomic.Int64
	work := func(w int) {
		for {
			c := int(next.Add(1)) - 1
			if c >= nChunks {
				return
			}
			first := c * chunk
			n := chunk
			if first+n > nRows {
				n = nRows - first
			}
			fn(w, first, n)
		}
	}

	// The caller runs as worker 0: one fewer goroutine to spawn, and one
	// fewer handoff before the work starts.
	var wg sync.WaitGroup
	wg.Add(nw - 1)
	for i := 1; i < nw; i++ {
		go func(i int) {
			defer wg.Done()
			work(i)
		}(i)
	}
	work(0)
	wg.Wait()
}

const chunksPerWorker = 4

// boostSaturationRGB is chafa-pixops.c:boost_saturation_rgb().
func boostSaturationRGB(p []uint8) {
	r := float32(p[0])
	g := float32(p[1])
	b := float32(p[2])
	P := float32(math.Sqrt(float64(r*r*0.299 + g*g*0.587 + b*b*0.144)))

	p[0] = clampToU8(int32(P + (r-P)*2))
	p[1] = clampToU8(int32(P + (g-P)*2))
	p[2] = clampToU8(int32(P + (b-P)*2))
}

func clampToU8(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// histogramCalcBounds is chafa-pixops.c:histogram_calc_bounds().
func histogramCalcBounds(hist *[intensityMax]int32, nSamples int, cropPct int) (int, int) {
	pixelsCrop := (int64(nSamples) * ((int64(cropPct) * 1024) / 100)) / 1024

	var i int
	t := int32(pixelsCrop)
	for i = 0; i < intensityMax; i++ {
		t -= hist[i]
		if t <= 0 {
			break
		}
	}
	hmin := i

	t = int32(pixelsCrop)
	for i = intensityMax - 1; i >= 0; i-- {
		t -= hist[i]
		if t <= 0 {
			break
		}
	}
	hmax := i

	return hmin, hmax
}

func normalizeCh(v uint8, min, factor int) uint8 {
	vt := int(v)
	vt -= min
	vt *= factor
	vt /= fixedMult
	if vt < 0 {
		return 0
	}
	if vt > 255 {
		return 255
	}
	return uint8(vt)
}

// ------------------------------------------------------------- work cells

type workCell struct {
	pixels [symbolNPixels]color

	// Per-sextant-block channel sums and sums of squares, plus their totals
	// over the whole cell. Every symbol's coverage is constant over each
	// block (see symbol.blocks), so a candidate's mean colours and its error
	// are both exact functions of these six sums -- no 64-pixel walk per
	// candidate, which is where this path used to spend most of its time.
	sum     [nBlocks][4]int32
	sq      [nBlocks][3]int32
	total   [4]int32
	totalSq [3]int32
}

func (cv *canvas) initWorkCell(wc *workCell, cx, cy int) {
	stride := cv.widthPixels * 4
	base := cy*symbolHeightPixels*stride + cx*symbolWidthPixels*4

	wc.sum = [nBlocks][4]int32{}
	wc.sq = [nBlocks][3]int32{}
	wc.total = [4]int32{}
	wc.totalSq = [3]int32{}

	i := 0
	for y := 0; y < symbolHeightPixels; y++ {
		row := cv.pixels[base+y*stride : base+y*stride+symbolWidthPixels*4]
		for x := 0; x < symbolWidthPixels; x++ {
			px := row[x*4 : x*4+4 : x*4+4]
			wc.pixels[i].ch[0] = px[0]
			wc.pixels[i].ch[1] = px[1]
			wc.pixels[i].ch[2] = px[2]
			wc.pixels[i].ch[3] = px[3]

			b := blockOfPixel[i]
			sum := &wc.sum[b]
			sq := &wc.sq[b]
			for ch := 0; ch < 3; ch++ {
				v := int32(px[ch])
				sum[ch] += v
				sq[ch] += v * v
				wc.total[ch] += v
				wc.totalSq[ch] += v * v
			}
			sum[3] += int32(px[3])
			wc.total[3] += int32(px[3])

			i++
		}
	}
}

// fgSums adds up the channel sums over the blocks the symbol covers. This is
// the foreground accumulator chafa_work_cell_get_mean_colors_for_symbol()
// builds pixel by pixel; the background accumulator is the cell total minus
// it, since every pixel is one or the other.
func (wc *workCell) fgSums(sym *symbol) [4]int32 {
	var fg [4]int32
	m := sym.blocks
	for b := 0; m != 0; b, m = b+1, m>>1 {
		if m&1 == 0 {
			continue
		}
		s := &wc.sum[b]
		fg[0] += s[0]
		fg[1] += s[1]
		fg[2] += s[2]
		fg[3] += s[3]
	}
	return fg
}

// meanColorsFrom is the tail of chafa_work_cell_get_mean_colors_for_symbol():
// the same int16 accumulators and truncating divisions as the C.
func (wc *workCell) meanColorsFrom(fg *[4]int32, sym *symbol, out *colorPair) {
	var accums [2][4]int16
	for j := 0; j < 4; j++ {
		accums[1][j] = int16(fg[j])
		accums[0][j] = int16(wc.total[j] - fg[j])
	}

	if sym.fgWeight > 1 {
		d := int16(sym.fgWeight)
		for j := 0; j < 4; j++ {
			accums[1][j] /= d
		}
	}
	if sym.bgWeight > 1 {
		d := int16(sym.bgWeight)
		for j := 0; j < 4; j++ {
			accums[0][j] /= d
		}
	}

	for j := 0; j < 4; j++ {
		out.colors[0].ch[j] = uint8(accums[0][j])
		out.colors[1].ch[j] = uint8(accums[1][j])
	}
}

// getMeanColorsForSymbol is chafa_work_cell_get_mean_colors_for_symbol().
func (wc *workCell) getMeanColorsForSymbol(sym *symbol, out *colorPair) {
	fg := wc.fgSums(sym)
	wc.meanColorsFrom(&fg, sym, out)
}

// toBitmap is chafa_work_cell_to_bitmap().
func (wc *workCell) toBitmap(pair *colorPair) uint64 {
	var bitmap uint64
	for i := 0; i < symbolNPixels; i++ {
		bitmap <<= 1
		e0 := colorDiffFast(wc.pixels[i], pair.colors[0])
		e1 := colorDiffFast(wc.pixels[i], pair.colors[1])
		if e0 > e1 {
			bitmap |= 1
		}
	}
	return bitmap
}

// getContrastingColorPair is chafa_work_cell_get_contrasting_color_pair().
func (wc *workCell) getContrastingColorPair(out *colorPair) {
	var minIndex, maxIndex [4]int
	// Carry the running extremes alongside their indices rather than
	// re-reading pixels[minIndex[ch]] on every comparison. Same values, same
	// tie-breaking: >= still keeps the last maximum.
	minVal := wc.pixels[0].ch
	maxVal := wc.pixels[0].ch

	for i := 1; i < symbolNPixels; i++ {
		p := &wc.pixels[i]
		for ch := 0; ch < 4; ch++ {
			v := p.ch[ch]
			if v < minVal[ch] {
				minVal[ch] = v
				minIndex[ch] = i
			}
			if v >= maxVal[ch] {
				maxVal[ch] = v
				maxIndex[ch] = i
			}
		}
	}

	bestRange := int(wc.pixels[maxIndex[0]].ch[0]) - int(wc.pixels[minIndex[0]].ch[0])
	bestCh := 0

	for ch := 1; ch < 4; ch++ {
		r := int(wc.pixels[maxIndex[ch]].ch[ch]) - int(wc.pixels[minIndex[ch]].ch[ch])
		if r > bestRange {
			bestRange = r
			bestCh = ch
		}
	}

	out.colors[0] = wc.pixels[minIndex[bestCh]]
	out.colors[1] = wc.pixels[maxIndex[bestCh]]
}

// cellError is chafa-symbol-renderer.c:calc_cell_error_plain(), which sums
// chafa_color_diff_fast(colours[coverage[i]], pixels[i]) over the 64 pixels.
//
// Expanding (px - c)^2 into px^2 - 2*c*px + c^2 turns that sum into the cell's
// sums of squares, its channel sums split foreground/background, and the
// symbol's pixel weights -- all of which are already to hand. It is an
// integer identity, so the result is the same value the pixel walk produced,
// and every intermediate stays well inside int32.
func cellError(wc *workCell, fg *[4]int32, pair *colorPair, sym *symbol) int {
	fgW := int32(sym.fgWeight)
	bgW := int32(sym.bgWeight)

	var err int32
	for ch := 0; ch < 3; ch++ {
		c1 := int32(pair.colors[1].ch[ch])
		c0 := int32(pair.colors[0].ch[ch])
		s1 := fg[ch]
		s0 := wc.total[ch] - s1

		err += wc.totalSq[ch] -
			2*c1*s1 + fgW*c1*c1 -
			2*c0*s0 + bgW*c0*c0
	}
	return int(err)
}

type symbolEval struct {
	colors colorPair
	err    int
}

func (cv *canvas) evalSymbol(wc *workCell, symIndex int, bestSymIndex *int, bestEval *symbolEval) {
	sym := &sextantSymbols[symIndex]
	var eval symbolEval

	fg := wc.fgSums(sym)
	if cv.fgOnly {
		eval.colors = cv.defaultColors
	} else {
		wc.meanColorsFrom(&fg, sym, &eval.colors)
	}

	// use_quantized_error is false (never INDEXED_16_8), so the palettes are
	// NULL and the error uses the extracted colours directly.
	eval.err = cellError(wc, &fg, &eval.colors, sym)

	if eval.err < bestEval.err {
		*bestSymIndex = symIndex
		*bestEval = eval
	}
}

// pickSymbolAndColorsFast is chafa-symbol-renderer.c:pick_symbol_and_colors_fast().
func (cv *canvas) pickSymbolAndColorsFast(wc *workCell) (rune, colorPair, int) {
	var pair colorPair

	if cv.extractColors && !cv.fgOnly {
		wc.getContrastingColorPair(&pair)
	} else {
		pair = cv.defaultColors
	}

	bitmap := wc.toBitmap(&pair)

	var cands [nCandidatesMax]candidate
	n := findCandidates(bitmap, cv.considerInverted, cands[:], workFactorInt)

	bestSymbol := -1
	bestEval := symbolEval{err: symbolErrorMax}

	for i := 0; i < n; i++ {
		cv.evalSymbol(wc, cands[i].symbolIndex, &bestSymbol, &bestEval)
	}

	if cv.extractColors && cv.fgOnly {
		wc.getMeanColorsForSymbol(&sextantSymbols[bestSymbol], &bestEval.colors)
	}

	return sextantSymbols[bestSymbol].c, bestEval.colors, bestEval.err
}

func transparentCellColor(mode Mode) uint32 {
	if mode == TrueColor {
		return packColor(color{[4]uint8{0x80, 0x80, 0x80, 0x00}})
	}
	return paletteIndexTransparent
}

// updateCellColors is chafa-symbol-renderer.c:update_cell_colors().
func (cv *canvas) updateCellColors(cell *canvasCell, pair *colorPair) {
	if cv.mode == TrueColor {
		cell.fgColor = packColor(pair.colors[1])
		cell.bgColor = packColor(pair.colors[0])
	} else {
		cell.fgColor = uint32(cv.fgPalette.lookupNearest(pair.colors[1]))
		cell.bgColor = uint32(cv.bgPalette.lookupNearest(pair.colors[0]))
	}

	if cv.fgOnly {
		cell.bgColor = transparentCellColor(cv.mode)
	}
}

func (cv *canvas) updateCell(wc *workCell, cell *canvasCell) int {
	sym, pair, symErr := cv.pickSymbolAndColorsFast(wc)
	cell.c = sym
	cv.updateCellColors(cell, &pair)
	return symErr
}

// updateCellsRow is chafa-symbol-renderer.c:update_cells_row(). The wide-symbol
// and fill passes are unreachable here: the sextant map has no wide symbols and
// the fill symbol map is empty.
func (cv *canvas) updateCellsRow(row int) {
	cells := cv.cells[row*cv.width : (row+1)*cv.width]
	var wc workCell

	for cx := 0; cx < cv.width; cx++ {
		cells[cx] = canvasCell{c: ' '}

		cv.initWorkCell(&wc, cx, row)
		cv.updateCell(&wc, &cells[cx])

		// "If we produced a featureless cell, try fill" -- apply_fill and
		// apply_fill_fg_only both return immediately with an empty fill map,
		// except that the fg-only branch re-clears the BG colour.
		if cells[cx].c == ' ' || cells[cx].c == 0x2588 || cells[cx].fgColor == cells[cx].bgColor {
			if cv.fgOnly {
				cells[cx].bgColor = transparentCellColor(cv.mode)
			}
		}

		if cells[cx].c == ' ' || cells[cx].fgColor == cells[cx].bgColor {
			cells[cx].c = cv.blankChar

			if cv.blankChar == ' ' && cx > 0 {
				cells[cx].fgColor = cells[cx-1].fgColor

				if cv.mode == TrueColor {
					cells[cx].fgColor |= 0xff000000
				} else if cells[cx].fgColor == paletteIndexTransparent {
					cells[cx].fgColor = paletteIndexFG
				}
			}
		}
	}
}

// maybeClear is chafa-canvas.c:maybe_clear(); used when no pixels were drawn.
func (cv *canvas) maybeClear() {
	for i := range cv.cells {
		cv.cells[i] = canvasCell{c: ' '}
	}
}
