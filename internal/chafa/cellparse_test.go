package chafa

import (
	"strconv"
	"strings"
	"testing"
)

// A parsed cell: the glyph plus the colours the terminal would actually paint
// it with (inversion already applied). This lets the fidelity test compare
// rendered appearance rather than escape-sequence spelling.
type parsedCell struct {
	glyph rune
	fg    penColor
	bg    penColor
}

type penColor struct {
	kind  int // 0 = default, 1 = indexed, 2 = direct
	index int
	r     uint8
	g     uint8
	b     uint8
}

var defaultPen = penColor{}

type sgrState struct {
	fg       penColor
	bg       penColor
	inverted bool
}

// parseCells decodes a canvas print into cols*rows cells. It fails the test on
// any structure it does not expect, so a malformed render cannot silently score
// as agreement.
func parseCells(t *testing.T, s string, cols, rows int) []parsedCell {
	t.Helper()

	cells := make([]parsedCell, 0, cols*rows)
	var st sgrState
	row, col := 0, 0

	rs := []rune(s)
	for i := 0; i < len(rs); {
		switch {
		case rs[i] == 0x1b:
			if i+1 >= len(rs) || rs[i+1] != '[' {
				t.Fatalf("unexpected escape at %d", i)
			}
			j := i + 2
			for j < len(rs) && rs[j] != 'm' {
				j++
			}
			if j >= len(rs) {
				t.Fatalf("unterminated SGR at %d", i)
			}
			applySGR(t, &st, string(rs[i+2:j]))
			i = j + 1

		case rs[i] == '\n':
			if col != cols {
				t.Fatalf("row %d has %d cells, want %d", row, col, cols)
			}
			row++
			col = 0
			i++

		default:
			fg, bg := st.fg, st.bg
			if st.inverted {
				fg, bg = bg, fg
			}
			cells = append(cells, parsedCell{rs[i], fg, bg})
			col++
			i++
		}
	}

	if len(cells) != cols*rows {
		t.Fatalf("got %d cells, want %d (%dx%d)", len(cells), cols*rows, cols, rows)
	}
	return cells
}

func applySGR(t *testing.T, st *sgrState, body string) {
	t.Helper()
	if body == "" {
		body = "0"
	}
	parts := strings.Split(body, ";")
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			t.Fatalf("bad SGR parameter %q", parts[i])
		}
		switch {
		case n == 0:
			*st = sgrState{}
		case n == 1: // bold; unused on these paths
		case n == 7:
			st.inverted = true
		case n == 27:
			st.inverted = false
		case n == 39:
			st.fg = defaultPen
		case n == 49:
			st.bg = defaultPen
		case n >= 30 && n <= 37:
			st.fg = penColor{kind: 1, index: n - 30}
		case n >= 40 && n <= 47:
			st.bg = penColor{kind: 1, index: n - 40}
		case n >= 90 && n <= 97:
			st.fg = penColor{kind: 1, index: n - 90 + 8}
		case n >= 100 && n <= 107:
			st.bg = penColor{kind: 1, index: n - 100 + 8}
		case n == 38 || n == 48:
			pen, adv := parseExtended(t, parts[i+1:])
			if n == 38 {
				st.fg = pen
			} else {
				st.bg = pen
			}
			i += adv
		default:
			t.Fatalf("unhandled SGR parameter %d", n)
		}
	}
}

func parseExtended(t *testing.T, rest []string) (penColor, int) {
	t.Helper()
	if len(rest) == 0 {
		t.Fatal("truncated extended colour")
	}
	switch rest[0] {
	case "5":
		if len(rest) < 2 {
			t.Fatal("truncated 256-colour")
		}
		n, _ := strconv.Atoi(rest[1])
		return penColor{kind: 1, index: n}, 2
	case "2":
		if len(rest) < 4 {
			t.Fatal("truncated direct colour")
		}
		r, _ := strconv.Atoi(rest[1])
		g, _ := strconv.Atoi(rest[2])
		b, _ := strconv.Atoi(rest[3])
		return penColor{kind: 2, r: uint8(r), g: uint8(g), b: uint8(b)}, 4
	}
	t.Fatalf("unknown extended colour form %q", rest[0])
	return penColor{}, 0
}
