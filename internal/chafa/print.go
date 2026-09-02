package chafa

import (
	"strconv"
	"strings"
)

// Port of chafa-canvas-printer.c against the fallback ChafaTermInfo
// (chafa_term_db_get_fallback_info()). That fallback carries vt220 + direct +
// 256 + 16 + 8 colour sequences but *not* rep_seqs, so CHAFA_TERM_SEQ_REPEAT_CHAR
// is absent and flush_chars() always writes the character out n_reps times.
//
// canvas_config optimizations default to CHAFA_OPTIMIZATION_ALL, so
// REUSE_ATTRIBUTES is on.

type printCtx struct {
	cv  *canvas
	out *strings.Builder

	curChar     rune
	nReps       int
	curInverted bool
	curBold     bool
	curFG       uint32
	curBG       uint32

	curFGDirect color
	curBGDirect color

	numBuf [8]byte
}

func (p *printCtx) flushChars() {
	if p.curChar == 0 {
		return
	}
	for p.nReps != 0 {
		p.out.WriteRune(p.curChar)
		p.nReps--
	}
	p.curChar = 0
}

func (p *printCtx) queueChar(c rune) {
	if p.curChar == c {
		p.nReps++
		return
	}
	if p.curChar != 0 {
		p.flushChars()
	}
	p.curChar = c
	p.nReps = 1
}

func (p *printCtx) resetFG() {
	p.out.WriteString("\x1b[39m")
	p.curFG = paletteIndexTransparent
	p.curFGDirect.ch[3] = 0
}

func (p *printCtx) resetAttributes() {
	p.out.WriteString("\x1b[0m")
	p.curInverted = false
	p.curBold = false
	p.curFG = paletteIndexTransparent
	p.curBG = paletteIndexTransparent
	p.curFGDirect.ch[3] = 0
	p.curBGDirect.ch[3] = 0
}

func (p *printCtx) invertColors() { p.out.WriteString("\x1b[7m") }

func (p *printCtx) writeUint(v uint) {
	// AppendUint into a reused buffer rather than FormatUint, which allocates
	// a string for every value above 99.
	p.out.Write(strconv.AppendUint(p.numBuf[:0], uint64(v), 10))
}

func thresholdAlpha(c color, threshold int) color {
	if int(c.ch[3]) < threshold {
		c.ch[3] = 0
	} else {
		c.ch[3] = 255
	}
	return c
}

func cmpColors(a, b color) bool { return a != b }

// ------------------------------------------------------------- truecolor

func (p *printCtx) emitAttributesTruecolor(fg, bg color, inverted bool) {
	// REUSE_ATTRIBUTES branch; fg_only_enabled is false in truecolor mode.
	if (p.curInverted && !inverted) ||
		(p.curFGDirect.ch[3] != 0 && fg.ch[3] == 0) ||
		(p.curBGDirect.ch[3] != 0 && bg.ch[3] == 0) {
		p.flushChars()
		p.resetAttributes()
	}

	if !p.curInverted && inverted {
		p.flushChars()
		p.invertColors()
	}

	if cmpColors(fg, p.curFGDirect) {
		if cmpColors(bg, p.curBGDirect) && bg.ch[3] != 0 {
			p.flushChars()
			p.out.WriteString("\x1b[38;2;")
			p.writeUint(uint(fg.ch[0]))
			p.out.WriteByte(';')
			p.writeUint(uint(fg.ch[1]))
			p.out.WriteByte(';')
			p.writeUint(uint(fg.ch[2]))
			p.out.WriteString(";48;2;")
			p.writeUint(uint(bg.ch[0]))
			p.out.WriteByte(';')
			p.writeUint(uint(bg.ch[1]))
			p.out.WriteByte(';')
			p.writeUint(uint(bg.ch[2]))
			p.out.WriteByte('m')
		} else if fg.ch[3] != 0 {
			p.flushChars()
			p.out.WriteString("\x1b[38;2;")
			p.writeUint(uint(fg.ch[0]))
			p.out.WriteByte(';')
			p.writeUint(uint(fg.ch[1]))
			p.out.WriteByte(';')
			p.writeUint(uint(fg.ch[2]))
			p.out.WriteByte('m')
		}
	} else if cmpColors(bg, p.curBGDirect) && bg.ch[3] != 0 {
		p.flushChars()
		p.out.WriteString("\x1b[48;2;")
		p.writeUint(uint(bg.ch[0]))
		p.out.WriteByte(';')
		p.writeUint(uint(bg.ch[1]))
		p.out.WriteByte(';')
		p.writeUint(uint(bg.ch[2]))
		p.out.WriteByte('m')
	}

	p.curFGDirect = fg
	p.curBGDirect = bg
	p.curInverted = inverted
}

func (p *printCtx) emitAnsiTruecolor(i, iMax int) {
	for ; i < iMax; i++ {
		cell := &p.cv.cells[i]

		fg := thresholdAlpha(unpackColor(cell.fgColor), alphaThreshold)
		bg := thresholdAlpha(unpackColor(cell.bgColor), alphaThreshold)

		if fg.ch[3] == 0 && bg.ch[3] != 0 {
			p.emitAttributesTruecolor(bg, fg, true)
		} else {
			p.emitAttributesTruecolor(fg, bg, false)
		}

		if fg.ch[3] == 0 && bg.ch[3] == 0 {
			p.queueChar(' ')
		} else {
			p.queueChar(cell.c)
		}
	}
}

// --------------------------------------------------------- indexed shared

func (p *printCtx) handleAttrsWithReuse(fg, bg uint32, inverted, bold bool) {
	if p.cv.fgOnly {
		return
	}

	if (p.curInverted && !inverted) ||
		(p.curBold && !bold) ||
		(p.curFG != paletteIndexTransparent && fg == paletteIndexTransparent) ||
		(p.curBG != paletteIndexTransparent && bg == paletteIndexTransparent) {
		p.flushChars()
		p.resetAttributes()
	}

	if !p.curInverted && inverted {
		p.flushChars()
		p.invertColors()
	}
}

func (p *printCtx) emitAttributes256(fg, bg uint32, inverted bool) {
	p.handleAttrsWithReuse(fg, bg, inverted, false)

	if fg != p.curFG {
		if bg != p.curBG && bg != paletteIndexTransparent {
			p.flushChars()
			p.out.WriteString("\x1b[38;5;")
			p.writeUint(uint(uint8(fg)))
			p.out.WriteString(";48;5;")
			p.writeUint(uint(uint8(bg)))
			p.out.WriteByte('m')
		} else if fg != paletteIndexTransparent {
			p.flushChars()
			p.out.WriteString("\x1b[38;5;")
			p.writeUint(uint(uint8(fg)))
			p.out.WriteByte('m')
		}
	} else if bg != p.curBG && bg != paletteIndexTransparent {
		p.flushChars()
		p.out.WriteString("\x1b[48;5;")
		p.writeUint(uint(uint8(bg)))
		p.out.WriteByte('m')
	}

	p.curFG = fg
	p.curBG = bg
	p.curInverted = inverted
}

func aix16FG(pen uint8) uint {
	if pen < 8 {
		return uint(pen) + 30
	}
	return uint(pen) + (90 - 8)
}

func aix16BG(pen uint8) uint {
	if pen < 8 {
		return uint(pen) + 40
	}
	return uint(pen) + (100 - 8)
}

func (p *printCtx) emitAttributes16(fg, bg uint32, inverted bool) {
	p.handleAttrsWithReuse(fg, bg, inverted, false)

	if fg != p.curFG {
		if bg != p.curBG && bg != paletteIndexTransparent {
			p.flushChars()
			p.out.WriteString("\x1b[")
			p.writeUint(aix16FG(uint8(fg)))
			p.out.WriteByte(';')
			p.writeUint(aix16BG(uint8(bg)))
			p.out.WriteByte('m')
		} else if fg != paletteIndexTransparent {
			p.flushChars()
			p.out.WriteString("\x1b[")
			p.writeUint(aix16FG(uint8(fg)))
			p.out.WriteByte('m')
		}
	} else if bg != p.curBG && bg != paletteIndexTransparent {
		p.flushChars()
		p.out.WriteString("\x1b[")
		p.writeUint(aix16BG(uint8(bg)))
		p.out.WriteByte('m')
	}

	p.curFG = fg
	p.curBG = bg
	p.curInverted = inverted
}

func (p *printCtx) emitAnsiIndexed(i, iMax int, indexed16 bool) {
	for ; i < iMax; i++ {
		cell := &p.cv.cells[i]
		fg := cell.fgColor
		bg := cell.bgColor

		emit := p.emitAttributes256
		if indexed16 {
			emit = p.emitAttributes16
		}

		if fg == paletteIndexTransparent && bg != paletteIndexTransparent {
			emit(bg, fg, true)
		} else {
			emit(fg, bg, false)
		}

		if fg == paletteIndexTransparent && bg == paletteIndexTransparent {
			p.queueChar(' ')
		} else {
			p.queueChar(cell.c)
		}
	}
}

func (p *printCtx) emitAnsiFgbg(i, iMax int) {
	for ; i < iMax; i++ {
		p.queueChar(p.cv.cells[i].c)
	}
}

// ----------------------------------------------------------------- driver

func (p *printCtx) buildAnsiRow(row int) {
	cv := p.cv
	i := row * cv.width
	iMax := (row + 1) * cv.width

	if row == 0 && cv.mode != FgBg {
		if cv.fgOnly {
			p.resetFG()
		} else {
			p.resetAttributes()
		}
	}

	switch cv.mode {
	case TrueColor:
		p.emitAnsiTruecolor(i, iMax)
	case Indexed240:
		p.emitAnsiIndexed(i, iMax, false)
	case Indexed16:
		p.emitAnsiIndexed(i, iMax, true)
	case FgBg:
		p.emitAnsiFgbg(i, iMax)
	}

	p.flushChars()

	if cv.mode != FgBg {
		if cv.fgOnly {
			p.resetFG()
		} else {
			p.resetAttributes()
		}
	}
}

func (cv *canvas) print() string {
	// Rough upper bound on the escape sequence bytes a cell can emit, so the
	// builder does not have to keep regrowing and recopying.
	perCell := 4
	switch cv.mode {
	case TrueColor:
		perCell = 36
	case Indexed240:
		perCell = 20
	case Indexed16:
		perCell = 14
	}

	var sb strings.Builder
	sb.Grow(cv.width*cv.height*perCell + cv.height*16)

	p := &printCtx{cv: cv, out: &sb}

	for i := 0; i < cv.height; i++ {
		p.buildAnsiRow(i)
		if i < cv.height-1 {
			sb.WriteByte('\n')
		}
	}

	return sb.String()
}
