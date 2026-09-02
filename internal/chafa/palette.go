package chafa

// Ports of the fixed-palette parts of chafa-palette.c. Dynamic (truecolor)
// palettes are never generated on this path, so the PNN quantizer and the
// colour table are deliberately not ported.

const (
	paletteIndexTransparent = 256
	paletteIndexFG          = 257
	paletteIndexBG          = 258
	paletteIndexMax         = 259
)

type color struct {
	ch [4]uint8
}

// termColors256 is chafa-palette.c:term_colors_256[].
var termColors256 = [paletteIndexMax]uint32{
	0x000000, 0x800000, 0x007000, 0x707000, 0x000070, 0x700070, 0x007070, 0xc0c0c0,
	0x404040, 0xff0000, 0x00ff00, 0xffff00, 0x0000ff, 0xff00ff, 0x00ffff, 0xffffff,

	0x000000, 0x00005f, 0x000087, 0x0000af, 0x0000d7, 0x0000ff, 0x005f00, 0x005f5f,
	0x005f87, 0x005faf, 0x005fd7, 0x005fff, 0x008700, 0x00875f, 0x008787, 0x0087af,
	0x0087d7, 0x0087ff, 0x00af00, 0x00af5f, 0x00af87, 0x00afaf, 0x00afd7, 0x00afff,
	0x00d700, 0x00d75f, 0x00d787, 0x00d7af, 0x00d7d7, 0x00d7ff, 0x00ff00, 0x00ff5f,
	0x00ff87, 0x00ffaf, 0x00ffd7, 0x00ffff, 0x5f0000, 0x5f005f, 0x5f0087, 0x5f00af,
	0x5f00d7, 0x5f00ff, 0x5f5f00, 0x5f5f5f, 0x5f5f87, 0x5f5faf, 0x5f5fd7, 0x5f5fff,
	0x5f8700, 0x5f875f, 0x5f8787, 0x5f87af, 0x5f87d7, 0x5f87ff, 0x5faf00, 0x5faf5f,
	0x5faf87, 0x5fafaf, 0x5fafd7, 0x5fafff, 0x5fd700, 0x5fd75f, 0x5fd787, 0x5fd7af,
	0x5fd7d7, 0x5fd7ff, 0x5fff00, 0x5fff5f, 0x5fff87, 0x5fffaf, 0x5fffd7, 0x5fffff,
	0x870000, 0x87005f, 0x870087, 0x8700af, 0x8700d7, 0x8700ff, 0x875f00, 0x875f5f,
	0x875f87, 0x875faf, 0x875fd7, 0x875fff, 0x878700, 0x87875f, 0x878787, 0x8787af,
	0x8787d7, 0x8787ff, 0x87af00, 0x87af5f, 0x87af87, 0x87afaf, 0x87afd7, 0x87afff,
	0x87d700, 0x87d75f, 0x87d787, 0x87d7af, 0x87d7d7, 0x87d7ff, 0x87ff00, 0x87ff5f,
	0x87ff87, 0x87ffaf, 0x87ffd7, 0x87ffff, 0xaf0000, 0xaf005f, 0xaf0087, 0xaf00af,
	0xaf00d7, 0xaf00ff, 0xaf5f00, 0xaf5f5f, 0xaf5f87, 0xaf5faf, 0xaf5fd7, 0xaf5fff,
	0xaf8700, 0xaf875f, 0xaf8787, 0xaf87af, 0xaf87d7, 0xaf87ff, 0xafaf00, 0xafaf5f,
	0xafaf87, 0xafafaf, 0xafafd7, 0xafafff, 0xafd700, 0xafd75f, 0xafd787, 0xafd7af,
	0xafd7d7, 0xafd7ff, 0xafff00, 0xafff5f, 0xafff87, 0xafffaf, 0xafffd7, 0xafffff,
	0xd70000, 0xd7005f, 0xd70087, 0xd700af, 0xd700d7, 0xd700ff, 0xd75f00, 0xd75f5f,
	0xd75f87, 0xd75faf, 0xd75fd7, 0xd75fff, 0xd78700, 0xd7875f, 0xd78787, 0xd787af,
	0xd787d7, 0xd787ff, 0xd7af00, 0xd7af5f, 0xd7af87, 0xd7afaf, 0xd7afd7, 0xd7afff,
	0xd7d700, 0xd7d75f, 0xd7d787, 0xd7d7af, 0xd7d7d7, 0xd7d7ff, 0xd7ff00, 0xd7ff5f,
	0xd7ff87, 0xd7ffaf, 0xd7ffd7, 0xd7ffff, 0xff0000, 0xff005f, 0xff0087, 0xff00af,
	0xff00d7, 0xff00ff, 0xff5f00, 0xff5f5f, 0xff5f87, 0xff5faf, 0xff5fd7, 0xff5fff,
	0xff8700, 0xff875f, 0xff8787, 0xff87af, 0xff87d7, 0xff87ff, 0xffaf00, 0xffaf5f,
	0xffaf87, 0xffafaf, 0xffafd7, 0xffafff, 0xffd700, 0xffd75f, 0xffd787, 0xffd7af,
	0xffd7d7, 0xffd7ff, 0xffff00, 0xffff5f, 0xffff87, 0xffffaf, 0xffffd7, 0xffffff,
	0x080808, 0x121212, 0x1c1c1c, 0x262626, 0x303030, 0x3a3a3a, 0x444444, 0x4e4e4e,
	0x585858, 0x626262, 0x6c6c6c, 0x767676, 0x808080, 0x8a8a8a, 0x949494, 0x9e9e9e,
	0xa8a8a8, 0xb2b2b2, 0xbcbcbc, 0xc6c6c6, 0xd0d0d0, 0xdadada, 0xe4e4e4, 0xeeeeee,

	0x808080, // Transparent
	0xffffff, // Terminal's default foreground
	0x000000, // Terminal's default background
}

var fixedPalette256 [paletteIndexMax]color
var colorCube216ChannelIndex [256]uint8

func init() {
	for i := 0; i < paletteIndexMax; i++ {
		fixedPalette256[i] = unpackColor(termColors256[i])
		fixedPalette256[i].ch[3] = 0xff
	}
	fixedPalette256[paletteIndexTransparent].ch[3] = 0x00

	i := 0
	for ; i < (0x5f+0x01)/2; i++ {
		colorCube216ChannelIndex[i] = 0
	}
	for ; i < (0x5f+0x87)/2; i++ {
		colorCube216ChannelIndex[i] = 1
	}
	for ; i < (0x87+0xaf)/2; i++ {
		colorCube216ChannelIndex[i] = 2
	}
	for ; i < (0xaf+0xd7)/2; i++ {
		colorCube216ChannelIndex[i] = 3
	}
	for ; i < (0xd7+0xff)/2; i++ {
		colorCube216ChannelIndex[i] = 4
	}
	for ; i <= 0xff; i++ {
		colorCube216ChannelIndex[i] = 5
	}
}

func unpackColor(packed uint32) color {
	var c color
	c.ch[0] = uint8(packed >> 16)
	c.ch[1] = uint8(packed >> 8)
	c.ch[2] = uint8(packed)
	c.ch[3] = uint8(packed >> 24)
	return c
}

func packColor(c color) uint32 {
	return uint32(c.ch[0])<<16 | uint32(c.ch[1])<<8 | uint32(c.ch[2]) | uint32(c.ch[3])<<24
}

// colorDiffFast is the chafa_color_diff_fast() macro; alpha is ignored.
func colorDiffFast(a, b color) int {
	d0 := int(b.ch[0]) - int(a.ch[0])
	d1 := int(b.ch[1]) - int(a.ch[1])
	d2 := int(b.ch[2]) - int(a.ch[2])
	return d0*d0 + d1*d1 + d2*d2
}

// colorAverage2 is chafa_color_average_2(): per-byte (a>>1) + (b>>1).
func colorAverage2(a, b color) color {
	au := colorToU32(a)
	bu := colorToU32(b)
	return u32ToColor(((au >> 1) & 0x7f7f7f7f) + ((bu >> 1) & 0x7f7f7f7f))
}

func colorToU32(c color) uint32 {
	return uint32(c.ch[0]) | uint32(c.ch[1])<<8 | uint32(c.ch[2])<<16 | uint32(c.ch[3])<<24
}

func u32ToColor(u uint32) color {
	return color{[4]uint8{uint8(u), uint8(u >> 8), uint8(u >> 16), uint8(u >> 24)}}
}

const maxInt32 = int(^uint32(0) >> 1)

type colorCandidates struct {
	index [2]int
	err   [2]int
}

func (cc *colorCandidates) init() {
	cc.index[0], cc.index[1] = -1, -1
	cc.err[0], cc.err[1] = maxInt32, maxInt32
}

func (cc *colorCandidates) update(index, err int) {
	if err < cc.err[0] {
		cc.index[1] = cc.index[0]
		cc.index[0] = index
		cc.err[1] = cc.err[0]
		cc.err[0] = err
	} else if err < cc.err[1] {
		cc.index[1] = index
		cc.err[1] = err
	}
}

func (cc *colorCandidates) updateWithIndexDiff(c color, index int) int {
	err := colorDiffFast(c, fixedPalette256[index])
	cc.update(index, err)
	return err
}

func pickColorFixed216Cube(c color, cc *colorCandidates) {
	i := 16 + (int(colorCube216ChannelIndex[c.ch[0]])*6*6 +
		int(colorCube216ChannelIndex[c.ch[1]])*6 +
		int(colorCube216ChannelIndex[c.ch[2]]))
	cc.updateWithIndexDiff(c, i)
}

func pickColorFixed24Grays(c color, cc *colorCandidates) {
	i := 232 + 12
	lastErr := cc.updateWithIndexDiff(c, i)

	var step int
	err := colorDiffFast(c, fixedPalette256[i+1])
	if err < lastErr {
		cc.update(i+1, err)
		lastErr = err
		step = 1
		i++
	} else {
		step = -1
	}

	for {
		i += step
		err = colorDiffFast(c, fixedPalette256[i])
		if err > lastErr {
			break
		}
		cc.update(i, err)
		lastErr = err
		if !(i >= 232 && i <= 255) {
			break
		}
	}
}

func pickColorFixed16(c color, cc *colorCandidates) {
	for i := 0; i < 16; i++ {
		cc.updateWithIndexDiff(c, i)
	}
}

type paletteType int

const (
	paletteDynamic256 paletteType = iota
	paletteFixed240
	paletteFixed16
	paletteFixedFGBG
)

type palette struct {
	typ            paletteType
	alphaThreshold int
	colors         [paletteIndexMax]color
}

func newPalette(typ paletteType, fg, bg color, alphaThreshold int) *palette {
	p := &palette{typ: typ, alphaThreshold: alphaThreshold}
	p.colors = fixedPalette256
	p.colors[paletteIndexFG] = fg
	p.colors[paletteIndexBG] = bg
	return p
}

// lookupNearest is chafa_palette_lookup_nearest() for the fixed palettes.
// transparent_index is always CHAFA_PALETTE_INDEX_TRANSPARENT (256), so the
// remapping tail of the C function is a no-op and is not reproduced.
func (p *palette) lookupNearest(c color) int {
	var cc colorCandidates
	cc.init()

	if int(c.ch[3]) < p.alphaThreshold {
		return paletteIndexTransparent
	}

	switch p.typ {
	case paletteFixed240:
		pickColorFixed216Cube(c, &cc)
		pickColorFixed24Grays(c, &cc)
	case paletteFixed16:
		pickColorFixed16(c, &cc)
	default: // paletteFixedFGBG
		cc.update(paletteIndexFG, colorDiffFast(c, p.colors[paletteIndexFG]))
		cc.update(paletteIndexBG, colorDiffFast(c, p.colors[paletteIndexBG]))
	}

	return cc.index[0]
}

func (p *palette) getColor(index int) color {
	return p.colors[index]
}
