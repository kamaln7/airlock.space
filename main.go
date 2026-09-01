package airlockspace

import (
	"fmt"
	"image/color"
	"io"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/muesli/reflow/wordwrap"
	"github.com/samber/lo"
	lom "github.com/samber/lo/mutable"
)

var paragraphBreaks = regexp.MustCompile(`\s{3,}`)

// NASA appends this migration notice to every explanation these days
var apodMovingNotice = regexp.MustCompile(`\s*APOD.s main NASA (web\s?)?site is moving:.*`)

// explanationText is the displayed explanation: notice stripped, NASA's
// space-run separators turned into real paragraph breaks. Memoized: it is
// consulted per selected row on every drag event.
func (m *Model) explanationText() string {
	if m.explCacheFor != m.apod {
		expl := apodMovingNotice.ReplaceAllString(m.apod.Explanation, "")
		m.explCache = paragraphBreaks.ReplaceAllString(expl, "\n\n")
		m.explCacheFor = m.apod
	}
	return m.explCache
}

// txt is the base style. v2 has no per-session renderer: the program converts
// colors to the client's profile on the way out, so one plain style serves
// every session.
var txt = lipgloss.NewStyle()

// adaptive is a light/dark color pair. lipgloss v2 dropped AdaptiveColor along
// with the renderer that knew the background, so the pair is resolved per
// model instead - see Model.color.
type adaptive struct{ light, dark color.Color }

var (
	colorMuted      = adaptive{lipgloss.Color("#9B9B9B"), lipgloss.Color("#5C5C5C")}
	colorSuperMuted = adaptive{lipgloss.Color("#DDDADA"), lipgloss.Color("#3C3C3C")}
	colorNebula     = adaptive{lipgloss.Color("#B4A7D6"), lipgloss.Color("#6B4E8C")} // Purple nebula tones
	colorCosmic     = adaptive{lipgloss.Color("#A7D6D6"), lipgloss.Color("#4E8C8C")} // Deep space teal
	colorStellar    = adaptive{lipgloss.Color("#D6B4A7"), lipgloss.Color("#8C6B4E")} // Warm star glow
	colorYellow     = lipgloss.Color("220")
)

// color picks the side of a pair matching the terminal background.
func (m *Model) color(a adaptive) color.Color {
	return lipgloss.LightDark(!m.isLight)(a.light, a.dark)
}

type Model struct {
	Width         int
	Height        int
	KittyGraphics bool      // client can render real images via the kitty graphics protocol
	Session       io.Writer // raw session output, for kitty image transmission
	// WidthPixels and HeightPixels are the terminal's drawable size in device
	// pixels, which ssh reports alongside the cell grid. Zero when the client
	// does not send it.
	WidthPixels  int
	HeightPixels int

	// Profile and isLight come from the terminal via bubbletea, not from the
	// server's own environment. Stated the light way round so the zero value
	// is dark: that is the right guess for a picture of space, and it holds
	// until the terminal answers - if it ever does.
	Profile colorprofile.Profile
	isLight bool

	State            State
	imgOrExplanation bool // true -> img, false -> explanation
	apod             *apod.APOD
	date             time.Time // the day being browsed; zero until the first load
	latest           time.Time // the most recent day NASA has posted
	imageOK          bool      // image bytes are ready to render
	kittyReady       bool      // this day's photo is uploaded and placeable
	preferArt        bool      // sextant art instead of the real photo
	photoToggled     bool      // user overrode the art/photo default with the keybind
	copiedRecently   bool
	hoverKey         string // help bar item under the mouse
	hoverLink        bool   // mouse is over the URL under the explanation
	mouseX           int    // last seen mouse position
	mouseY           int
	selAnchor        point // in-app text selection (mouse tracking eats the
	selEnd           point // terminal's own selection, so we provide our own)
	selActive        bool
	selPending       bool   // left button went down inside the selectable region
	artKey           string // memoized decorative art (stable across renders)
	art              string
	explCache        string // memoized processed explanation
	explCacheFor     *apod.APOD
	placedCols       int // current kitty virtual placement size
	placedRows       int
	imageLoading     bool
	spinFrame        int
}

type State int

const (
	StateLoading State = iota
	StateAPOD
	StateFullscreen
)

// the moon-phase spinner is advanced by a single plain tick chain started in
// Init; no start/stop bookkeeping means no way for the chain to die
var spinnerFrames = []string{"🌑", "🌒", "🌓", "🌔", "🌕", "🌖", "🌗", "🌘"}

type msgSpin struct{}

func spinTick() tea.Cmd {
	return tea.Tick(time.Second/8, func(time.Time) tea.Msg { return msgSpin{} })
}

// showingImage reports whether the image (or its spinner) is what the APOD
// view is displaying. On a video day there is no image, so it is always the
// explanation regardless of the toggle.
func (m *Model) showingImage() bool {
	return m.imgOrExplanation && (m.imageOK || m.imageLoading)
}

func (m *Model) spinnerView() string {
	return m.txtYellow().Render(spinnerFrames[m.spinFrame%len(spinnerFrames)])
}

func (m *Model) Init() tea.Cmd {
	m.imgOrExplanation = true
	return tea.Batch(m.loadAPOD(m.date), spinTick(), tea.RequestBackgroundColor)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	prevState := m.State

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Height = msg.Height
		m.Width = msg.Width
		m.applyArtDefault()
		cmds = append(cmds, m.sendKitty())
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, keyQuit):
			return m, tea.Quit
		case key.Matches(msg, keyPrevDay):
			cmds = append(cmds, m.showDay(m.date.AddDate(0, 0, -1)))
		case key.Matches(msg, keyNextDay):
			cmds = append(cmds, m.showDay(m.date.AddDate(0, 0, 1)))
		case key.Matches(msg, keyExplanation):
			// nothing to toggle to on a video day: the explanation is all there is
			if !m.imageOK && !m.imageLoading {
				break
			}
			m.State = StateAPOD
			m.imgOrExplanation = !m.imgOrExplanation
			m.artKey = "" // reshuffle the decorative art, as before, on interaction
		case key.Matches(msg, keyFullscreen):
			// no fullscreen from the explanation view
			if !m.imageOK || !m.imgOrExplanation {
				break
			}
			if m.State == StateFullscreen {
				m.State = StateAPOD
			} else {
				m.State = StateFullscreen
			}
		case key.Matches(msg, keyPhoto):
			if m.KittyGraphics && m.imageOK {
				m.preferArt = !m.preferArt
				m.photoToggled = true
				cmds = append(cmds, m.sendKitty())
			}
		case key.Matches(msg, keyCopySelection):
			cmds = append(cmds, m.copySelection())
		case key.Matches(msg, keyCopy):
			if m.apod != nil {
				cmds = append(cmds, m.copyLink())
			}
		}
	case tea.MouseMotionMsg:
		m.mouseX, m.mouseY = msg.X, msg.Y
		if msg.Button == tea.MouseLeft {
			// drag: extend the text selection
			if m.selPending {
				m.selEnd = point{msg.X, msg.Y}
				m.selActive = m.selEnd != m.selAnchor
			}
			break
		}
		m.hoverKey, m.hoverLink = m.hitTest(msg.X, msg.Y)
	case tea.MouseClickMsg:
		switch msg.Button {
		case tea.MouseLeft:
			m.selActive, m.selPending = false, false
			helpKey, onLink := m.hitTest(msg.X, msg.Y)
			if helpKey != "" {
				// dispatch the clicked help item as if its key was pressed
				r := []rune(helpKey)[0]
				return m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			if onLink {
				cmds = append(cmds, m.copyLink())
			}
			// selection can start anywhere; it only ever applies to copyable text
			m.selPending = true
			m.selAnchor = point{msg.X, msg.Y}
			m.selEnd = m.selAnchor
		case tea.MouseRight:
			// right-click copies the selection, for terminals that keep
			// ctrl+shift+c for themselves
			cmds = append(cmds, m.copySelection())
		}
	case apodMsg:
		if !msg.date.Equal(m.date) {
			break // a later day was asked for while this one was loading
		}
		if msg.apod == nil {
			if m.apod != nil {
				m.date = m.apod.ApodDate // stay on the day we are already showing
			}
			m.State = StateAPOD
			break
		}
		m.apod = msg.apod
		m.date = msg.apod.ApodDate
		if msg.apod.ApodDate.After(m.latest) {
			m.latest = msg.apod.ApodDate // NASA posted a new one under us
		}
		m.imageOK = false
		m.kittyReady = false
		m.imageLoading = true
		m.placedCols, m.placedRows = 0, 0
		m.State = StateAPOD
		cmds = append(cmds, m.loadImage())
	case imageMsg:
		m.imageOK = msg.ok
		m.imageLoading = false
		m.applyArtDefault()
		cmds = append(cmds, m.sendKitty())
	case kittyMsg:
		if msg.apod == m.apod {
			m.kittyReady = msg.ok
			m.placedCols, m.placedRows = 0, 0
		}
	case tea.ColorProfileMsg:
		m.Profile = msg.Profile
	case tea.BackgroundColorMsg:
		m.isLight = !msg.IsDark()
	case msgSpin:
		// when the spinner isn't visible the frame is unchanged and bubbletea
		// skips the flush, so the perpetual tick is free
		m.spinFrame++
		cmds = append(cmds, spinTick())
	case msgCopyExpired:
		m.copiedRecently = false
	}

	// re-run hover hit-testing whenever the layout may have changed under a
	// stationary mouse: any keypress (footer items appear/disappear even
	// within a state, e.g. the explanation/image toggle) or state change
	if _, isKey := msg.(tea.KeyMsg); isKey || m.State != prevState {
		m.hoverKey, m.hoverLink = m.hitTest(m.mouseX, m.mouseY)
		m.selActive = false // the frame changed under the selection
	}
	return m, tea.Batch(cmds...)
}

// apodMsg carries the day that was asked for, so a slow answer that a later
// keypress has already overtaken can be dropped.
type apodMsg struct {
	date time.Time
	apod *apod.APOD
}

type imageMsg struct {
	ok bool
}

// kittyMsg reports an upload finishing. It carries the day it belongs to: the
// reader may have moved on while megabytes were in flight.
type kittyMsg struct {
	apod *apod.APOD
	ok   bool
}

// showDay switches to a day, if there is one there: NASA has nothing before
// First and nothing after the latest post. Loading takes a moment, so the
// spinner comes back while it does.
func (m *Model) showDay(date time.Time) tea.Cmd {
	if m.apod == nil || date.Before(apod.First) || date.After(m.latest) {
		return nil
	}
	m.date = date
	m.State = StateLoading
	m.artKey = "" // reshuffle the decorative art, as on any other interaction
	return m.loadAPOD(date)
}

// loadAPOD fetches one day. The zero date, and the latest day, go through
// Today: it is the only path that notices when NASA posts a new picture.
func (m *Model) loadAPOD(date time.Time) tea.Cmd {
	latest := m.latest
	return func() tea.Msg {
		var a *apod.APOD
		var err error
		if date.IsZero() || date.Equal(latest) {
			a, err = apod.Today()
		} else {
			a, err = apod.ByDate(date)
		}
		if err != nil {
			slog.Warn("failed to get APOD", "error", err, "date", date)
			if a == nil {
				slog.Error("no valid APOD to fallback to", "error", err)
			}
		}
		return apodMsg{date: date, apod: a}
	}
}

func (m *Model) loadImage() tea.Cmd {
	a := m.apod
	return func() tea.Msg {
		byt, err := a.ImageBytes()
		if err != nil || len(byt) == 0 {
			slog.Warn("APOD has no image", "error", err)
			return imageMsg{}
		}
		return imageMsg{ok: true}
	}
}

// pixelBox is the largest the photo could ever be drawn: the whole terminal,
// which is what fullscreen uses. ssh carries the real device-pixel size, so on
// a hidpi screen this is the hidpi box and the photo stays sharp.
// ponytail: 10x20 per cell when the client sends no pixel size, and the cell
// size is read once - a font size change mid-session goes unnoticed.
func (m *Model) pixelBox() (w, h int) {
	if m.WidthPixels > 0 && m.HeightPixels > 0 {
		return m.WidthPixels, m.HeightPixels
	}
	return m.Width * 10, m.Height * 20
}

// sendKitty uploads the photo to the terminal, once per day and only when the
// photo is what the reader is about to see. The payload is megabytes - a 565KB
// source jpeg becomes 5MB of base64 - and the sextant art, which is what most
// viewports get, needs none of it. Until it arrives, imageArea falls back to
// the art, so the wait shows something rather than nothing.
func (m *Model) sendKitty() tea.Cmd {
	if !m.KittyGraphics || m.Session == nil || m.kittyReady || !m.imageOK || m.preferArt {
		return nil
	}
	a, out := m.apod, m.Session
	maxW, maxH := m.pixelBox()
	return func() tea.Msg {
		img, err := a.Decode()
		if err != nil {
			slog.Warn("failed to decode image for kitty", "error", err)
			return kittyMsg{apod: a}
		}
		png, err := kittyPNG(img, maxW, maxH)
		if err != nil {
			slog.Warn("failed to prepare png for kitty", "error", err)
			return kittyMsg{apod: a}
		}
		wire := kittyTransmit(png)
		if _, err := io.WriteString(out, wire); err != nil {
			slog.Warn("failed to send image to the terminal", "error", err)
			return kittyMsg{apod: a}
		}
		slog.Info("sent photo", "date", a.Date, "box", fmt.Sprintf("%dx%d", maxW, maxH), "bytes", len(wire))
		return kittyMsg{apod: a, ok: true}
	}
}

var (
	// the arrow glyphs are keys too, so clicking the footer item dispatches them
	keyPrevDay = key.NewBinding(
		key.WithKeys("left", "h", glyphPrev),
		key.WithHelp(glyphPrev, "prev day"),
	)
	keyNextDay = key.NewBinding(
		key.WithKeys("right", "l", glyphNext),
		key.WithHelp(glyphNext, "next day"),
	)
	keyQuit = key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	)
	keyExplanation = key.NewBinding(
		key.WithKeys("e", "ctrl+e"),
		key.WithHelp("e", "explanation/image"),
	)
	keyFullscreen = key.NewBinding(
		key.WithKeys("f", "ctrl+f"),
		key.WithHelp("f", "fullscreen"),
	)
	keyPhoto = key.NewBinding(
		key.WithKeys("p", "ctrl+p"),
		key.WithHelp("p", "photo/art"),
	)
	keyCopy = key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "copy link"),
	)
	// v2 asks the terminal for key disambiguation, so ctrl+shift+c finally
	// arrives as itself rather than as ctrl+c. Kept off the help bar: whether
	// it reaches us at all is the terminal's call.
	keyCopySelection = key.NewBinding(key.WithKeys("ctrl+shift+c"))
)

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

// render draws the frame. Kept apart from View so the selection helpers, which
// re-render to read a row back, deal in plain strings.
func (m *Model) render() string {
	if m.Width == 0 || m.Height == 0 {
		// don't draw until the terminal size is known; a zero-width frame
		// leaves artifacts behind once the real one renders
		return ""
	}
	view := m.baseView()
	if m.selActive {
		view = m.applySelection(view)
	}
	return view
}

// baseView renders the current page without the selection overlay; selection
// helpers must use this (via frameLine) to avoid recursing through View.
func (m *Model) baseView() string {
	switch m.State {
	case StateLoading:
		return m.viewLoading()
	case StateAPOD:
		return m.viewAPOD()
	case StateFullscreen:
		return m.viewFullscreen()
	}
	return "error"
}

func (m *Model) viewAPOD() string {
	showImage := m.showingImage()
	totalWidth := m.Width - 2 // -2 for the margin
	apodWidth := min(72, totalWidth)
	freeWidth := totalWidth - apodWidth
	if showImage {
		// full-width apod if we are showing the image
		apodWidth = totalWidth
		freeWidth = totalWidth
	}
	apodView := m.viewAPODText(apodWidth, !showImage)
	helpView := m.viewHelp()

	freeHeight := m.Height - 2 - countLines(helpView) // -2 for top margin + last row
	if showImage {
		freeHeight -= countLines(apodView)
		var imgBlock string
		if m.imageOK {
			imgBlock = txt.Width(freeWidth).Height(freeHeight).Align(lipgloss.Center, lipgloss.Center).Render(m.imageArea(freeWidth, freeHeight))
		} else {
			// pin the spinner to the exact screen center (same row as viewLoading)
			pad := max(0, m.Height/2-1-countLines(apodView))
			imgBlock = txt.Width(freeWidth).Height(freeHeight).Render(
				strings.Repeat("\n", pad) + txt.Width(freeWidth).Align(lipgloss.Center).Render(m.loadingLine()),
			)
		}
		return txt.Margin(1, 1, 0, 1).Render(
			lipgloss.JoinVertical(lipgloss.Left,
				apodView,
				imgBlock,
				helpView,
			),
		)
	} else {
		asciiArt := m.decorArt(max(0, freeWidth-8), freeHeight) // -8 for the gap

		// max-width container centered in the viewport (margin: 0 auto):
		// text column and art side by side at their natural widths
		textCol := apodView + helpView
		content := textCol
		if asciiArt != "" {
			content = lipgloss.JoinHorizontal(lipgloss.Top,
				textCol,
				txt.MarginLeft(8).Height(lipgloss.Height(textCol)).Align(lipgloss.Center, lipgloss.Center).Render(asciiArt),
			)
		}
		return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, content)
	}
}

// viewLinkLine is the clickable URL shown under the explanation, with inline
// copy feedback. Same row either way, so the layout doesn't jump.
func (m *Model) viewLinkLine() string {
	link := m.apod.Link()
	st := m.txtMuted()
	if m.hoverLink {
		st = m.txtYellow().Underline(true)
	}
	hint := "" // its own row, so the URL never shifts when this appears
	if m.copiedRecently {
		hint = txt.Bold(true).Render("copied!")
	}
	// hyperlink outside the style, never inside: lipgloss styles rune by rune
	// under Underline, which shreds an embedded escape into literal text
	return hyperlink(link, st.Render(link)) + "\n" + hint
}

type msgCopyExpired struct{}

// copyLink puts the APOD link on the client clipboard and shows brief feedback.
func (m *Model) copyLink() tea.Cmd {
	m.copiedRecently = true
	return tea.Batch(
		tea.SetClipboard(m.apod.Link()),
		tea.Tick(2*time.Second, func(time.Time) tea.Msg { return msgCopyExpired{} }),
	)
}

// copySelection copies the highlighted text, if any, and clears the highlight.
func (m *Model) copySelection() tea.Cmd {
	if !m.selActive {
		return nil
	}
	m.selActive = false
	text := m.selectionText()
	if text == "" {
		return nil
	}
	return tea.SetClipboard(text)
}

// helpKeys returns the keybindings shown (and clickable) in the current state.
func (m *Model) helpKeys() []key.Binding {
	switch m.State {
	case StateFullscreen:
		return []key.Binding{keyFullscreen}
	}
	// the day arrows live in the header now, not here
	var keys []key.Binding
	// no image to toggle to on a video day, so no e
	if m.imageOK || m.imageLoading {
		// show only the action the key would perform, not both toggle sides
		eDesc := "image"
		if m.showingImage() {
			eDesc = "explanation"
		}
		keys = append(keys, key.NewBinding(key.WithKeys("e", "ctrl+e"), key.WithHelp("e", eDesc)))
	}
	if m.showingImage() {
		keys = append(keys, keyFullscreen)
		// image/ascii toggle only where the image is on screen; action-only label
		if m.KittyGraphics {
			pDesc := "ascii"
			if m.preferArt {
				pDesc = "image"
			}
			keys = append(keys, key.NewBinding(key.WithKeys("p", "ctrl+p"), key.WithHelp("p", pDesc)))
		}
	}
	return append(keys, keyCopy, keyQuit) // q quit stays last
}

// viewHelp renders the help bar. Items under the mouse are highlighted; Update
// hit-tests clicks against the same "key desc" spans.
func (m *Model) viewHelp() string {
	var b strings.Builder
	for i, k := range m.helpKeys() {
		if i > 0 {
			b.WriteString(m.divDot().Render())
		}
		h := k.Help()
		keySt := txt.Bold(true)
		descSt := m.txtMuted()
		if m.hoverKey == h.Key {
			keySt = keySt.Foreground(colorYellow).Underline(true)
			descSt = m.txtYellow()
		}
		b.WriteString(keySt.Render(h.Key) + " " + descSt.Render(h.Desc))
	}
	return txt.MarginTop(1).Render(b.String())
}

type point struct{ X, Y int }

// selBounds returns the selection corners in top-to-bottom order.
func (m *Model) selBounds() (a, b point) {
	a, b = m.selAnchor, m.selEnd
	if a.Y > b.Y || (a.Y == b.Y && a.X > b.X) {
		a, b = b, a
	}
	return a, b
}

// copyableSpan returns the column span of copyable text on a stripped frame
// line: the APOD title (any view) or a line of the explanation. Everything
// else — headers, dates, gaps, art, footer — is not selectable. It takes the
// line rather than rendering the frame itself: it runs per row per drag event.
func (m *Model) copyableSpan(line string) (x1, x2 int, ok bool) {
	if m.apod == nil {
		return 0, 0, false
	}

	// the full title on one row (main screen header row, or its own row)
	if s, e, found := columnSpan(line, m.apod.Title); found {
		return s, e, true
	}

	// explanation view: text-column content matched against the actual
	// title/explanation text, so gaps and decorations never highlight
	if m.State == StateAPOD && !m.showingImage() {
		colStart := 1
		colEnd := min(1+min(72, m.Width-2), ansi.StringWidth(line))
		if colStart >= colEnd {
			return 0, 0, false
		}
		seg := ansi.Cut(line, colStart, colEnd)
		trimmedLeft := strings.TrimLeft(seg, " ")
		trimmed := strings.TrimRight(trimmedLeft, " ")
		if trimmed == "" {
			return 0, 0, false
		}
		if !strings.Contains(m.explanationText(), trimmed) && !strings.Contains(m.apod.Title, trimmed) {
			return 0, 0, false
		}
		x1 = colStart + ansi.StringWidth(seg) - ansi.StringWidth(trimmedLeft)
		return x1, x1 + ansi.StringWidth(trimmed), true
	}
	return 0, 0, false
}

// selSpan gives the highlighted column range on row y (stripped line given):
// the raw linewise drag span intersected with the row's copyable text.
func (m *Model) selSpan(y int, line string) (x1, x2 int, ok bool) {
	a, b := m.selBounds()
	if y < a.Y || y > b.Y {
		return 0, 0, false
	}
	c1, c2, copyable := m.copyableSpan(line)
	if !copyable {
		return 0, 0, false
	}
	x1, x2 = c1, c2
	if y == a.Y {
		x1 = max(x1, a.X)
	}
	if y == b.Y {
		x2 = min(x2, b.X+1)
	}
	return x1, x2, x1 < x2
}

// applySelection overlays reverse-video highlighting on the selected region.
func (m *Model) applySelection(frame string) string {
	lines := strings.Split(frame, "\n")
	a, b := m.selBounds()
	for y := max(0, a.Y); y <= b.Y && y < len(lines); y++ {
		line := lines[y]
		x1, x2, ok := m.selSpan(y, ansi.Strip(line))
		if !ok {
			continue
		}
		// the selected span loses its colors (stripped) — standard selection look
		mid := txt.Reverse(true).Render(ansi.Strip(ansi.Cut(line, x1, x2)))
		lines[y] = ansi.Cut(line, 0, x1) + mid + ansi.TruncateLeft(line, x2, "")
	}
	return strings.Join(lines, "\n")
}

// selectionText extracts the selected text, trimmed like terminals do.
func (m *Model) selectionText() string {
	lines := strings.Split(m.baseView(), "\n")
	a, b := m.selBounds()
	var out []string
	for y := max(0, a.Y); y <= b.Y && y < len(lines); y++ {
		line := ansi.Strip(lines[y])
		x1, x2, ok := m.selSpan(y, line)
		if !ok {
			out = append(out, "")
			continue
		}
		out = append(out, strings.TrimRight(ansi.Cut(line, x1, x2), " "))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// columnSpan finds substr in the (ANSI-stripped) line and returns its display
// column span. Byte offsets drift right of the true columns as soon as the
// line contains multi-byte runes, so widths are measured, not indexed.
func columnSpan(line, substr string) (start, end int, ok bool) {
	idx := strings.Index(line, substr)
	if idx < 0 {
		return 0, 0, false
	}
	start = ansi.StringWidth(line[:idx])
	return start, start + ansi.StringWidth(substr), true
}

// frameLine returns the ANSI-stripped screen row under y.
func (m *Model) frameLine(y int) string {
	lines := strings.Split(m.baseView(), "\n")
	// a frame taller than the viewport scrolls: the screen shows its tail, so
	// screen row y is that many lines further down the frame
	y += max(0, len(lines)-m.Height)
	if y < 0 || y >= len(lines) {
		return ""
	}
	return ansi.Strip(lines[y])
}

// isHelpLine reports whether a stripped frame line is the help bar (most
// items present).
func (m *Model) isHelpLine(line string) bool {
	matches := 0
	for _, k := range m.helpKeys() {
		h := k.Help()
		if strings.Contains(line, h.Key+" "+h.Desc) {
			matches++
		}
	}
	return matches >= 2
}

// navHit reports which day arrow, if any, sits at column x of a stripped frame
// line. The whole label is the target, not just the glyph.
func (m *Model) navHit(line string, x int) string {
	prev, next := m.dayArrows()
	for _, it := range []struct{ key, label string }{{glyphPrev, prev}, {glyphNext, next}} {
		if it.label == "" {
			continue
		}
		if start, end, ok := columnSpan(line, it.label); ok && x >= start && x < end {
			return it.key
		}
	}
	return ""
}

// hitTest resolves what's under the mouse in one frame render: a key to
// dispatch (or "") and whether the link URL is hovered. Both the help bar and
// the header's day arrows answer with the key they stand for, so a click on
// either is dispatched the same way.
func (m *Model) hitTest(x, y int) (helpKey string, link bool) {
	line := m.frameLine(y)
	if m.isHelpLine(line) {
		for _, k := range m.helpKeys() {
			h := k.Help()
			if start, end, ok := columnSpan(line, h.Key+" "+h.Desc); ok && x >= start && x < end {
				helpKey = h.Key
				break
			}
		}
	}
	if helpKey == "" {
		helpKey = m.navHit(line, x)
	}
	if m.apod != nil {
		start, end, ok := columnSpan(line, m.apod.Link())
		link = ok && x >= start && x < end
	}
	return helpKey, link
}

// loadingLine is the spinner + label, shared by both loading phases.
func (m *Model) loadingLine() string {
	return m.spinnerView() + " " + m.txtMuted().Render("loading...")
}

func (m *Model) viewLoading() string {
	// spinner pinned to the exact screen center so it doesn't jump when the
	// view transitions to the image-loading phase
	pad := max(0, m.Height/2)
	return txt.Width(m.Width).Height(m.Height).Render(
		strings.Repeat("\n", pad) + txt.Width(m.Width).Align(lipgloss.Center).Render(m.loadingLine()),
	)
}

func (m *Model) txtMuted() lipgloss.Style {
	return txt.Foreground(m.color(colorMuted))
}

func (m *Model) txtSuperMuted() lipgloss.Style {
	return txt.Foreground(m.color(colorSuperMuted))
}

func (m *Model) divDot() lipgloss.Style {
	return m.txtSuperMuted().SetString(" • ")
}

func (m *Model) txtYellow() lipgloss.Style {
	return txt.Foreground(colorYellow)
}

// hyperlink wraps already-styled text in an OSC 8 hyperlink.
func hyperlink(url, text string) string {
	return ansi.SetHyperlink(url) + text + ansi.ResetHyperlink()
}

func countLines(str string) int {
	return len(strings.Split(str, "\n"))
}

// Goodbye is printed on exit, outside the alt screen. It writes truecolor
// escapes; the caller sends them through a colorprofile.Writer, which
// downsamples them to what the client can show.
func Goodbye() string {
	muted := txt.Foreground(colorMuted.dark)
	yellow := txt.Foreground(colorYellow)

	msg := yellow.Render(" ♥ thanks for visiting! wishing you clear skies ~") + "\n\n"
	// StaleOnError can return a usable stale APOD alongside an error
	a, _ := apod.Today()
	if a == nil {
		return "\n" + msg
	}
	return fmt.Sprintf("\n 🌌 %s %s\n 🔗 %s\n\n%s",
		txt.Bold(true).Render(a.Title),
		muted.Render("— "+a.ApodDate.Format(time.DateOnly)),
		yellow.Render(a.Link()),
		msg)
}

// decorArt picks and colorizes a random ascii art fitting the box, memoized so
// it stays put across renders (mouse motion re-renders constantly) and only
// reshuffles when the box size changes.
func (m *Model) decorArt(freeWidth, freeHeight int) string {
	key := fmt.Sprintf("%dx%d", freeWidth, freeHeight)
	if m.artKey == key {
		return m.art
	}

	var asciiArt string
	allAsciiArt := slices.Clone(ASCIIAll)
	lom.Shuffle(allAsciiArt)
	for _, art := range allAsciiArt {
		if countLines(art) > freeHeight {
			continue
		}
		if lipgloss.Width(art) > freeWidth {
			continue
		}
		asciiArt = colorize(art, m.color(colorMuted), m.color(colorCosmic), m.color(colorStellar), m.color(colorNebula))
		break
	}
	m.artKey, m.art = key, asciiArt
	return asciiArt
}

// applyArtDefault picks art vs real photo until the user toggles explicitly:
// sextant art is the house style, but on tiny viewports it gets too coarse,
// so small windows default to the real photo.
// ponytail: 60x20 threshold is a guess; tune if it feels wrong
func (m *Model) applyArtDefault() {
	if m.photoToggled || !m.KittyGraphics {
		return
	}
	m.preferArt = m.Width >= 60 && m.Height >= 20
}

// imageArea renders the APOD image into a box of cols x rows cells: a kitty
// graphics placement when the client supports it, sextant art otherwise.
func (m *Model) imageArea(cols, rows int) string {
	if m.kittyReady && !m.preferArt {
		// hand kitty the whole box: the terminal scales the image to fit
		// using its real cell metrics, which beats our 1:2 cell-aspect guess
		c := min(cols, len(kittyDiacritics))
		r := min(rows, len(kittyDiacritics))
		if c < 1 || r < 1 {
			return ""
		}
		if m.placedCols != c || m.placedRows != r {
			// virtual placements draw nothing; safe to write outside the renderer
			io.WriteString(m.Session, kittyVirtualPlacement(c, r))
			m.placedCols, m.placedRows = c, r
		}
		return kittyPlaceholders(c, r)
	}

	c, r := fitCells(m.apod.ImageSize.X, m.apod.ImageSize.Y, cols, rows)
	if c < 1 || r < 1 {
		return ""
	}
	s, err := cachedSextant(m.apod.Date, m.apod.Decode, c, r, canvasMode(m.Profile))
	if err != nil {
		slog.Warn("failed to render image", "error", err)
		return m.txtMuted().Render("image unavailable :(")
	}
	return strings.TrimSuffix(s, "\n")
}

// the arrow glyphs are keys as well as labels: clicking one dispatches it
const (
	glyphPrev = "\u2190"
	glyphNext = "\u2192"
)

// navDate is the compact form the day arrows label their day with. The day on
// screen keeps its year: browsing reaches back to 1995, where "Jun 16" alone
// would say very little.
const navDate = "Jan 2"

// navMaxWidth caps the day line. Justified to the full width of a wide
// terminal the arrows would sit half a screen apart, so they are pinned to the
// edges of a centered container instead - the text column's own max width.
const navMaxWidth = 72

// navGap is the least blank space between an arrow and the day it frames.
const navGap = 2

// dayArrows returns the labels for the days either side of the one on screen,
// empty where there is no such day.
func (m *Model) dayArrows() (prev, next string) {
	if m.apod == nil {
		return "", ""
	}
	if m.date.After(apod.First) {
		prev = glyphPrev + " " + m.date.AddDate(0, 0, -1).Format(navDate)
	}
	if m.date.Before(m.latest) {
		next = m.date.AddDate(0, 0, 1).Format(navDate) + " " + glyphNext
	}
	return prev, next
}

// viewNav is the header's day line: the day on screen and its title, with the
// days either side of it to step to.
//
// One row while it fits: the arrows pinned to the edges of a navMaxWidth
// container with the day centered between them, which is justify-between with
// the middle item centered rather than merely evenly spaced - so the title
// holds the centre whatever the arrows either side of it are called. Too tight
// for that and the three blocks wrap onto rows of their own, as flex-wrap
// would. The caller centers whatever comes back.
func (m *Model) viewNav(width int) string {
	prev, next := m.dayArrows()
	center := m.txtMuted().Render(m.apod.ApodDate.Format("Jan 2, 2006")) + " " +
		hyperlink(m.apod.Link(), txt.Bold(true).Render(m.apod.Title))

	inner := min(navMaxWidth, width)
	pw, cw, nw := lipgloss.Width(prev), lipgloss.Width(center), lipgloss.Width(next)
	start := (inner - cw) / 2 // where the day sits, centered in the container

	if start >= pw+navGap && inner-start-cw >= nw+navGap {
		return m.txtArrow(glyphPrev).Render(prev) +
			strings.Repeat(" ", start-pw) + center +
			strings.Repeat(" ", inner-start-cw-nw) + m.txtArrow(glyphNext).Render(next)
	}

	rows := make([]string, 0, 3)
	if prev != "" {
		rows = append(rows, m.txtArrow(glyphPrev).Render(prev))
	}
	rows = append(rows, center)
	if next != "" {
		rows = append(rows, m.txtArrow(glyphNext).Render(next))
	}
	return strings.Join(rows, "\n")
}

// txtArrow styles one day arrow, lit up under the mouse like a help bar item.
// An absent day has no label and must not pick up an escape sequence.
func (m *Model) txtArrow(key string) lipgloss.Style {
	if m.hoverKey == key {
		return m.txtYellow().Underline(true)
	}
	return m.txtMuted()
}

// viewAPODText renders the header, the day line, and optionally the
// explanation.
func (m *Model) viewAPODText(width int, writeExplanation bool) string {
	var s strings.Builder

	header := m.txtMuted().Render("🌌 Astronomy Picture of the Day")

	if m.apod == nil {
		s.WriteString(header)
		s.WriteString("\n")
		s.WriteString(txt.Render("error fetching APOD :("))
		s.WriteString("\n")
		return s.String()
	}

	// the day line gets a row of its own: with a date on each arrow it is too
	// wide to share one with the header, so the flexbox the title used to do
	// would only ever have collapsed - and jumped between days as it did
	s.WriteString(header)
	s.WriteString("\n\n")
	s.WriteString(txt.Width(width).Align(lipgloss.Center).Render(m.viewNav(width)))
	s.WriteString("\n\n")

	if writeExplanation {
		s.WriteString(txt.Render(wordwrap.String(m.explanationText(), width)))
		s.WriteString("\n\n")
		s.WriteString(m.viewLinkLine())
		s.WriteString("\n")
	}

	return s.String()
}

func colorize(str string, colors ...color.Color) string {
	var s strings.Builder
	for _, char := range str {
		if unicode.IsSpace(char) {
			s.WriteRune(char)
			continue
		}
		s.WriteString(txt.Foreground(lo.Sample(colors)).Render(string(char)))
	}
	return s.String()
}

// fitImage calculates the new dimensions for an image to fit within a container
// while maintaining the original aspect ratio.
func fitImage(imageWidth, imageHeight, containerWidth, containerHeight int) (int, int) {
	if imageWidth <= 0 || imageHeight <= 0 || containerWidth <= 0 || containerHeight <= 0 {
		return 0, 0
	}

	// Calculate scale factors for both dimensions
	scaleX := float64(containerWidth) / float64(imageWidth)
	scaleY := float64(containerHeight) / float64(imageHeight)

	// Use the smaller scale factor to ensure the image fits within the container
	scale := math.Min(scaleX, scaleY)

	// Calculate new dimensions
	newWidth := int(math.Round(float64(imageWidth) * scale))
	newHeight := int(math.Round(float64(imageHeight) * scale))

	return newWidth, newHeight
}

func (m *Model) viewFullscreen() string {
	if !m.imageOK {
		return m.viewAPOD()
	}
	helpView := strings.TrimSpace(m.viewHelp())

	// image gets all but the last row; help gets its own line instead of
	// being byte-spliced into an ANSI-heavy image line
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.Place(m.Width, m.Height-1, lipgloss.Center, lipgloss.Center,
			m.imageArea(m.Width, m.Height-1)),
		helpView,
	)
}
