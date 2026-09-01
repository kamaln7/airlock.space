package airlockspace

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/termenv"
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

var (
	colorMuted      = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
	colorSuperMuted = lipgloss.AdaptiveColor{Light: "#DDDADA", Dark: "#3C3C3C"}
	colorNebula     = lipgloss.AdaptiveColor{Light: "#B4A7D6", Dark: "#6B4E8C"} // Purple nebula tones
	colorCosmic     = lipgloss.AdaptiveColor{Light: "#A7D6D6", Dark: "#4E8C8C"} // Deep space teal
	colorStellar    = lipgloss.AdaptiveColor{Light: "#D6B4A7", Dark: "#8C6B4E"} // Warm star glow
)

type Model struct {
	Width         int
	Height        int
	Style         lipgloss.Style
	Profile       termenv.Profile
	KittyGraphics bool      // client can render real images via the kitty graphics protocol
	Session       io.Writer // raw session output, for kitty image transmission

	State            State
	imgOrExplanation bool // true -> img, false -> explanation
	apod             *apod.APOD
	imageOK          bool // image bytes are ready to render
	kittySent        bool // image transmitted to the client's terminal
	preferArt        bool // sextant art instead of the real photo
	photoToggled     bool // user overrode the art/photo default with the keybind
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
	return tea.Batch(m.loadAPOD(), spinTick())
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	prevState := m.State

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Height = msg.Height
		m.Width = msg.Width
		m.applyArtDefault()
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keyQuit):
			return m, tea.Quit
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
			if m.kittySent {
				m.preferArt = !m.preferArt
				m.photoToggled = true
			}
		case key.Matches(msg, keyCopy):
			if m.apod != nil {
				cmds = append(cmds, m.copyLink())
			}
		}
	case tea.MouseMsg:
		switch {
		case msg.Action == tea.MouseActionMotion && msg.Button == tea.MouseButtonLeft:
			// drag: extend the text selection
			m.mouseX, m.mouseY = msg.X, msg.Y
			if m.selPending {
				m.selEnd = point{msg.X, msg.Y}
				m.selActive = m.selEnd != m.selAnchor
			}
		case msg.Action == tea.MouseActionMotion:
			m.mouseX, m.mouseY = msg.X, msg.Y
			m.hoverKey, m.hoverLink = m.hitTest(msg.X, msg.Y)
		case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
			m.selActive, m.selPending = false, false
			helpKey, onLink := m.hitTest(msg.X, msg.Y)
			if helpKey != "" {
				// dispatch the clicked help item as if its key was pressed
				return m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(helpKey)})
			}
			if onLink {
				cmds = append(cmds, m.copyLink())
			}
			// selection can start anywhere; it only ever applies to copyable text
			m.selPending = true
			m.selAnchor = point{msg.X, msg.Y}
			m.selEnd = m.selAnchor
		case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight:
			// right-click copies the selection (cmd+c/ctrl+shift+c never reach
			// the app, and ctrl+c must stay quit)
			if m.selActive {
				if text := m.selectionText(); text != "" {
					io.WriteString(m.Session, osc52Copy(text))
				}
				m.selActive = false
			}
		}
	case apodMsg:
		m.apod = msg
		m.imageOK = false
		m.kittySent = false
		m.placedCols, m.placedRows = 0, 0
		m.State = StateAPOD
		if msg != nil {
			m.imageLoading = true
			cmds = append(cmds, m.loadImage())
		}
	case imageMsg:
		m.imageOK = msg.ok
		m.kittySent = msg.kittySent
		m.imageLoading = false
		m.applyArtDefault()
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

type apodMsg *apod.APOD

type imageMsg struct {
	ok        bool
	kittySent bool
}

func (m *Model) loadAPOD() tea.Cmd {
	return func() tea.Msg {
		a, err := apod.Today()
		if err != nil {
			slog.Warn("failed to get APOD", "error", err)
			if a == nil {
				slog.Error("no valid APOD to fallback to", "error", err)
			}
		}
		return apodMsg(a)
	}
}

func (m *Model) loadImage() tea.Cmd {
	a := m.apod
	return func() tea.Msg {
		if _, err := a.ImageBytes(); err != nil {
			slog.Warn("APOD has no image", "error", err)
			return imageMsg{}
		}
		msg := imageMsg{ok: true}
		if m.KittyGraphics && m.Session != nil {
			png, err := a.PNGBytes()
			if err != nil {
				slog.Warn("failed to prepare png for kitty", "error", err)
			} else if _, err := io.WriteString(m.Session, kittyTransmit(png)); err == nil {
				msg.kittySent = true
			}
		}
		return msg
	}
}

var (
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
)

func (m *Model) View() string {
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
			imgBlock = m.Style.Width(freeWidth).Height(freeHeight).Align(lipgloss.Center, lipgloss.Center).Render(m.imageArea(freeWidth, freeHeight))
		} else {
			// pin the spinner to the exact screen center (same row as viewLoading)
			pad := max(0, m.Height/2-1-countLines(apodView))
			imgBlock = m.Style.Width(freeWidth).Height(freeHeight).Render(
				strings.Repeat("\n", pad) + m.Style.Width(freeWidth).Align(lipgloss.Center).Render(m.loadingLine()),
			)
		}
		return m.Style.Margin(1, 1, 0, 1).Render(
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
				m.Style.MarginLeft(8).Height(lipgloss.Height(textCol)).Align(lipgloss.Center, lipgloss.Center).Render(asciiArt),
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
	line := st.Render(termenv.Hyperlink(link, link))
	if m.copiedRecently {
		line += m.Style.Bold(true).Render("  copied!")
	}
	return line
}

type msgCopyExpired struct{}

// copyLink puts the APOD link on the client clipboard via OSC 52 and shows
// brief feedback.
func (m *Model) copyLink() tea.Cmd {
	io.WriteString(m.Session, osc52Copy(m.apod.Link()))
	m.copiedRecently = true
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return msgCopyExpired{} })
}

// helpKeys returns the keybindings shown (and clickable) in the current state.
func (m *Model) helpKeys() []key.Binding {
	switch m.State {
	case StateFullscreen:
		return []key.Binding{keyFullscreen}
	}
	keys := []key.Binding{keyCopy, keyQuit}
	// no image to toggle to on a video day, so no e
	if m.imageOK || m.imageLoading {
		// show only the action the key would perform, not both toggle sides
		eDesc := "image"
		if m.showingImage() {
			eDesc = "explanation"
		}
		keys = slices.Insert(keys, 0, key.NewBinding(key.WithKeys("e", "ctrl+e"), key.WithHelp("e", eDesc)))
	}
	if m.showingImage() {
		keys = slices.Insert(keys, len(keys)-1, keyFullscreen)
	}
	// image/ascii toggle only where the image is on screen; action-only label
	if m.kittySent && m.showingImage() {
		pDesc := "ascii"
		if m.preferArt {
			pDesc = "image"
		}
		// q quit stays last
		keys = slices.Insert(keys, len(keys)-1, key.NewBinding(key.WithKeys("p", "ctrl+p"), key.WithHelp("p", pDesc)))
	}
	return keys
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
		keySt := m.Style.Bold(true)
		descSt := m.txtMuted()
		if m.hoverKey == h.Key {
			keySt = keySt.Foreground(lipgloss.Color("220")).Underline(true)
			descSt = m.txtYellow()
		}
		b.WriteString(keySt.Render(h.Key) + " " + descSt.Render(h.Desc))
	}
	return m.Style.MarginTop(1).Render(b.String())
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
		mid := m.Style.Reverse(true).Render(ansi.Strip(ansi.Cut(line, x1, x2)))
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

// hitTest resolves what's under the mouse in one frame render: a help bar key
// (or "") and whether the link URL is hovered.
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
	return m.Style.Width(m.Width).Height(m.Height).Render(
		strings.Repeat("\n", pad) + m.Style.Width(m.Width).Align(lipgloss.Center).Render(m.loadingLine()),
	)
}

func (m *Model) txtMuted() lipgloss.Style {
	return m.Style.Foreground(colorMuted)
}

func (m *Model) txtSuperMuted() lipgloss.Style {
	return m.Style.Foreground(colorSuperMuted)
}

func (m *Model) divDot() lipgloss.Style {
	return m.txtSuperMuted().SetString(" • ")
}

func (m *Model) txtYellow() lipgloss.Style {
	return m.Style.Foreground(lipgloss.Color("220"))
}

func countLines(str string) int {
	return len(strings.Split(str, "\n"))
}

// Goodbye is printed on exit, outside the alt screen. The renderer determines
// the color profile of the target terminal.
func Goodbye(re *lipgloss.Renderer) string {
	txt := re.NewStyle()
	muted := txt.Foreground(colorMuted)
	yellow := txt.Foreground(lipgloss.Color("220"))

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
		asciiArt = colorize(m.Style, art, colorMuted, colorCosmic, colorStellar, colorNebula)
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
	if m.photoToggled || !m.kittySent {
		return
	}
	m.preferArt = m.Width >= 60 && m.Height >= 20
}

// imageArea renders the APOD image into a box of cols x rows cells: a kitty
// graphics placement when the client supports it, sextant art otherwise.
func (m *Model) imageArea(cols, rows int) string {
	if m.kittySent && !m.preferArt {
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

// viewAPODText renders the header, date, title, and optionally the explanation.
// When the width allows, the title shares the header row (flexbox-style);
// otherwise it gets its own centered block below.
func (m *Model) viewAPODText(width int, writeExplanation bool) string {
	txt := m.Style
	var s strings.Builder

	header := m.txtMuted().Render("🌌 Astronomy Picture of the Day")

	if m.apod == nil {
		s.WriteString(header)
		s.WriteString("\n")
		s.WriteString(txt.Render("error fetching APOD :("))
		s.WriteString("\n")
		return s.String()
	}

	dateLine := m.txtMuted().Render(m.apod.ApodDate.Format(time.DateOnly))
	title := txt.Bold(true).Render(termenv.Hyperlink(m.apod.Link(), m.apod.Title))

	// center the title relative to the viewport, not the space next to the
	// header; overlay works when the centered span clears the header
	titleStart := (width - lipgloss.Width(title)) / 2
	if titleStart >= lipgloss.Width(header)+4 {
		s.WriteString(header + strings.Repeat(" ", titleStart-lipgloss.Width(header)) + title)
		s.WriteString("\n")
		s.WriteString(dateLine)
		s.WriteString("\n")
		s.WriteString("\n")
	} else {
		s.WriteString(header)
		s.WriteString("\n")
		s.WriteString(dateLine)
		s.WriteString("\n\n\n")
		s.WriteString(txt.Width(width).Align(lipgloss.Center).Render(title))
		s.WriteString("\n\n")
	}

	if writeExplanation {
		s.WriteString(txt.Render(wordwrap.String(m.explanationText(), width)))
		s.WriteString("\n\n")
		s.WriteString(m.viewLinkLine())
		s.WriteString("\n")
	}

	return s.String()
}

func colorize(style lipgloss.Style, str string, colors ...lipgloss.TerminalColor) string {
	var s strings.Builder
	for _, char := range str {
		if unicode.IsSpace(char) {
			s.WriteRune(char)
			continue
		}
		color := lo.Sample(colors)
		s.WriteString(style.Foreground(color).Render(string(char)))
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
