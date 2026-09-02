package chafa

import (
	"math/bits"
	"sort"
)

const (
	symbolWidthPixels  = 8
	symbolHeightPixels = 8
	symbolNPixels      = symbolWidthPixels * symbolHeightPixels
)

// symbol mirrors ChafaSymbol for the fields the symbol path uses.
type symbol struct {
	c        rune
	coverage [symbolNPixels]uint8
	bitmap   uint64
	popcount int
	fgWeight int
	bgWeight int

	// blocks is a six-bit mask of the sextant blocks this symbol covers, in
	// gen_sextant_sym()'s bit order (y*2 + x). Not in the C: it is derived
	// from coverage, which for every symbol in this map is constant over each
	// block, and it lets the renderer assemble a symbol's mean colours and
	// error from six block sums instead of walking all 64 pixels. See
	// blockOfPixel.
	blocks uint8
}

const nBlocks = 6

// blockOfPixel maps a cell pixel to its sextant block, matching the row and
// column grouping gen_sextant_sym() paints: columns 0-3 / 4-7, and rows 0-2 /
// 3-4 / 5-7 (the middle band is two rows because three sextant rows are
// squeezed into eight pixel rows).
var blockOfPixel = func() (m [symbolNPixels]uint8) {
	for row := 0; row < symbolHeightPixels; row++ {
		var y uint8
		switch {
		case row <= 2:
			y = 0
		case row <= 4:
			y = 1
		default:
			y = 2
		}
		for col := 0; col < symbolWidthPixels; col++ {
			m[row*symbolWidthPixels+col] = y*2 + uint8(col/4)
		}
	}
	return m
}()

// genSextantSym is chafa-symbols.c:gen_sextant_sym().
func genSextantSym(cov *[symbolNPixels]uint8, val uint8) {
	*cov = [symbolNPixels]uint8{}

	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			bit := y*2 + x
			if val&(1<<uint(bit)) == 0 {
				continue
			}
			for v := 0; v < 3; v++ {
				for u := 0; u < 4; u++ {
					row := y*3 + v
					if row > 3 {
						row--
					}
					cov[row*8+x*4+u] = 1
				}
			}
		}
	}
}

// coverageToBitmap is chafa-symbols.c:coverage_to_bitmap() with rowstride 8.
func coverageToBitmap(cov *[symbolNPixels]uint8) uint64 {
	var bitmap uint64
	for y := 0; y < symbolHeightPixels; y++ {
		for x := 0; x < symbolWidthPixels; x++ {
			bitmap <<= 1
			if cov[y*8+x] != 0 {
				bitmap |= 1
			}
		}
	}
	return bitmap
}

func (s *symbol) finish() {
	s.bitmap = coverageToBitmap(&s.coverage)
	s.popcount = popcount64(s.bitmap)
	s.fgWeight = 0
	s.bgWeight = 0
	for i := 0; i < symbolNPixels; i++ {
		p := int(s.coverage[i])
		s.fgWeight += p
		s.bgWeight += 1 - p
	}

	// Derive the block mask, and check the invariant it rests on: coverage
	// must be constant across each block, or the block-sum shortcut in
	// workCell would not be exact.
	var seen [nBlocks]int8
	for i := range seen {
		seen[i] = -1
	}
	for i := 0; i < symbolNPixels; i++ {
		b := blockOfPixel[i]
		c := int8(s.coverage[i])
		if seen[b] < 0 {
			seen[b] = c
			if c != 0 {
				s.blocks |= 1 << b
			}
		} else if seen[b] != c {
			panic("chafa: symbol coverage is not constant over sextant blocks")
		}
	}
}

// popcount64 is the C's popcount; on arm64 this compiles to a single
// instruction rather than a loop over the set bits.
func popcount64(v uint64) int { return bits.OnesCount64(v) }

// sextantSymbols is the prepared symbol map for
// chafa_symbol_map_add_by_tags (map, CHAFA_SYMBOL_TAG_SEXTANT).
//
// Members: the four builtin defs carrying CHAFA_SYMBOL_TAG_SEXTANT
// (U+0020, U+2588, U+258C, U+2590) plus the generated 2x3 mosaics
// U+1FB00..U+1FB3A. Sorted by (popcount, code point), which is
// compare_symbols_popcount() in chafa-symbol-map.c.
var sextantSymbols = buildSextantSymbols()

func buildSextantSymbols() []symbol {
	syms := make([]symbol, 0, 63)

	add := func(c rune, fill func(cov *[symbolNPixels]uint8)) {
		var s symbol
		s.c = c
		fill(&s.coverage)
		s.finish()
		syms = append(syms, s)
	}

	// U+0020 SPACE: empty outline.
	add(0x20, func(cov *[symbolNPixels]uint8) {})

	// U+2588 FULL BLOCK.
	add(0x2588, func(cov *[symbolNPixels]uint8) {
		for i := range cov {
			cov[i] = 1
		}
	})

	// U+258C LEFT HALF BLOCK.
	add(0x258c, func(cov *[symbolNPixels]uint8) {
		for y := 0; y < 8; y++ {
			for x := 0; x < 4; x++ {
				cov[y*8+x] = 1
			}
		}
	})

	// U+2590 RIGHT HALF BLOCK.
	add(0x2590, func(cov *[symbolNPixels]uint8) {
		for y := 0; y < 8; y++ {
			for x := 4; x < 8; x++ {
				cov[y*8+x] = 1
			}
		}
	})

	// Teletext sextant/2x3 mosaics, generate_sextant_syms().
	for c := rune(0x1fb00); c < 0x1fb3b; c++ {
		bitmap := int(c-0x1fb00) + 1
		if bitmap > 20 {
			bitmap++
		}
		if bitmap > 41 {
			bitmap++
		}
		val := uint8(bitmap)
		add(c, func(cov *[symbolNPixels]uint8) { genSextantSym(cov, val) })
	}

	sort.Slice(syms, func(i, j int) bool {
		if syms[i].popcount != syms[j].popcount {
			return syms[i].popcount < syms[j].popcount
		}
		return syms[i].c < syms[j].c
	})

	return syms
}

var sextantPackedBitmaps = func() []uint64 {
	b := make([]uint64, len(sextantSymbols))
	for i := range sextantSymbols {
		b[i] = sextantSymbols[i].bitmap
	}
	return b
}()

const nCandidatesMax = 8

type candidate struct {
	symbolIndex int
	hammingDist int
	isInverted  bool
}

// insertCandidate is chafa-symbol-map.c:insert_candidate().
func insertCandidate(candidates *[nCandidatesMax]candidate, newCand candidate) {
	i := nCandidatesMax - 1

	for i != 0 {
		i--
		if newCand.hammingDist >= candidates[i].hammingDist {
			copy(candidates[i+2:nCandidatesMax], candidates[i+1:nCandidatesMax-1])
			candidates[i+1] = newCand
			return
		}
	}

	copy(candidates[1:nCandidatesMax], candidates[0:nCandidatesMax-1])
	candidates[0] = newCand
}

// findCandidates is chafa-symbol-map.c:chafa_symbol_map_find_candidates().
func findCandidates(bitmap uint64, doInverse bool, out []candidate, nWanted int) int {
	var candidates [nCandidatesMax]candidate
	for i := range candidates {
		candidates[i] = candidate{0, 65, false}
	}

	for i, b := range sextantPackedBitmaps {
		hd := popcount64(bitmap ^ b)

		if hd < candidates[nCandidatesMax-1].hammingDist {
			insertCandidate(&candidates, candidate{i, hd, false})
		}

		if doInverse {
			hd = 64 - hd
			if hd < candidates[nCandidatesMax-1].hammingDist {
				insertCandidate(&candidates, candidate{i, hd, true})
			}
		}
	}

	n := 0
	for ; n < nCandidatesMax; n++ {
		if candidates[n].hammingDist > 64 {
			break
		}
	}
	if n > nWanted {
		n = nWanted
	}
	copy(out[:n], candidates[:n])
	return n
}

func symbolMapHasSymbol(c rune) bool {
	for i := range sextantSymbols {
		if sextantSymbols[i].c == c {
			return true
		}
	}
	return false
}
