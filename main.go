package airlockspace

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/termenv"
	"github.com/samber/lo"
	lom "github.com/samber/lo/mutable"
)

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
	hoverLink        bool   // mouse is over the URL in the link view
	artKey           string // memoized decorative art (stable across renders)
	art              string
	placedCols       int // current kitty virtual placement size
	placedRows       int
	imageLoading     bool
	spinner          spinner.Model
	reloadedRecently bool
}

type State int

const (
	StateLoading State = iota
	StateAPOD
	StateLink
	StateFullscreen
)

func (m *Model) Init() tea.Cmd {
	m.imgOrExplanation = true
	m.spinner = spinner.New(spinner.WithSpinner(spinner.Moon), spinner.WithStyle(m.txtYellow()))
	return tea.Batch(m.loadAPOD(), m.spinner.Tick)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Height = msg.Height
		m.Width = msg.Width
		m.applyArtDefault()
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keyQuit):
			return m, tea.Quit
		case key.Matches(msg, keyReload):
			m.reloadedRecently = true
			m.State = StateLoading
			cmds = append(cmds, m.loadAPOD(), m.spinner.Tick)
		case key.Matches(msg, keyExplanation):
			m.State = StateAPOD
			m.imgOrExplanation = !m.imgOrExplanation
			m.artKey = "" // reshuffle the decorative art, as before, on interaction
		case key.Matches(msg, keyLink):
			if m.apod == nil {
				break
			}
			if m.State == StateLink {
				m.State = StateAPOD
			} else {
				m.State = StateLink
			}
		case key.Matches(msg, keyFullscreen):
			if !m.imageOK {
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
		case msg.Action == tea.MouseActionMotion:
			m.hoverKey = m.helpHitTest(msg.X, msg.Y)
			m.hoverLink = m.linkHitTest(msg.X, msg.Y)
		case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
			if k := m.helpHitTest(msg.X, msg.Y); k != "" {
				// dispatch the clicked help item as if its key was pressed
				return m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
			}
			if m.linkHitTest(msg.X, msg.Y) {
				cmds = append(cmds, m.copyLink())
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
			cmds = append(cmds, m.loadImage(), m.spinner.Tick)
		}
		cmds = append(cmds, tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
			return msgRerender{}
		}))
	case imageMsg:
		m.imageOK = msg.ok
		m.kittySent = msg.kittySent
		m.imageLoading = false
		m.applyArtDefault()
	case spinner.TickMsg:
		// always tick: a gated tick kills the chain permanently (spinner
		// freeze); when the spinner isn't visible the frame is unchanged and
		// bubbletea skips the flush, so this is free
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	case msgRerender:
		m.reloadedRecently = false
	case msgCopyExpired:
		m.copiedRecently = false
	}
	return m, tea.Batch(cmds...)
}

type msgRerender struct{}

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
	keyLink = key.NewBinding(
		key.WithKeys("l", "ctrl+l"),
		key.WithHelp("l", "link"),
	)
	keyReload = key.NewBinding(
		key.WithKeys("r", "ctrl+r"),
		key.WithHelp("r", "reload"),
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
	// contextual labels for the link view footer
	keyLinkCopy = key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "copy"),
	)
	keyLinkBack = key.NewBinding(
		key.WithKeys("l", "ctrl+l"),
		key.WithHelp("l", "back"),
	)
)

func (m *Model) View() string {
	if m.Width == 0 || m.Height == 0 {
		// don't draw until the terminal size is known; a zero-width frame
		// leaves artifacts behind once the real one renders
		return ""
	}
	switch m.State {
	case StateLoading:
		return m.viewLoading()
	case StateAPOD:
		return m.viewAPOD()
	case StateLink:
		return m.viewLink()
	case StateFullscreen:
		return m.viewFullscreen()
	}
	return "error"
}

func (m *Model) viewAPOD() string {
	showImage := m.imgOrExplanation && (m.imageOK || m.imageLoading)
	totalWidth := m.Width - 2 // -2 for the margin
	apodWidth := min(60, totalWidth)
	freeWidth := totalWidth - apodWidth
	if showImage {
		// full-width apod if we are showing the image
		apodWidth = totalWidth
		freeWidth = totalWidth
	}
	apodView := m.viewAPODText(apodWidth, !showImage)
	helpView := m.viewHelp()

	freeHeight := m.Height - 3 - countLines(helpView) // -3 for the margins
	if showImage {
		freeHeight -= countLines(apodView)
		asciiImage := m.spinner.View() + " " + m.txtMuted().Render("loading...")
		if m.imageOK {
			asciiImage = m.imageArea(freeWidth, freeHeight)
		}
		return m.Style.Margin(1, 1).Render(
			lipgloss.JoinVertical(lipgloss.Left,
				apodView,
				m.Style.Width(freeWidth).Height(freeHeight).Align(lipgloss.Center, lipgloss.Center).Render(asciiImage),
				helpView,
			),
		)
	} else {
		asciiArt := m.decorArt(freeWidth, freeHeight)

		return m.Style.Margin(1, 1).Render(
			lipgloss.JoinHorizontal(lipgloss.Top,
				apodView+helpView,
				m.Style.Width(freeWidth).Height(freeHeight).Align(lipgloss.Center, lipgloss.Center).Render(asciiArt),
			),
		)
	}
}

func (m *Model) viewLink() string {
	link := m.apod.Link()
	linkText := link
	if m.hoverLink {
		linkText = m.txtYellow().Underline(true).Render(link)
	}
	hint := m.txtSuperMuted().Render("click or press c to copy")
	if m.copiedRecently {
		hint = m.Style.Bold(true).Render("copied!")
	}
	// yellow applies to the link block only; the help bar keeps its own colors
	content := m.txtYellow().Render("🔗 link to APOD:\n\n"+termenv.Hyperlink(link, linkText)) + "\n\n" + hint + "\n" + m.viewHelp()
	return m.Style.Width(m.Width).Height(m.Height).Align(lipgloss.Center, lipgloss.Center).Render(content)
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
	case StateLink:
		return []key.Binding{keyLinkCopy, keyLinkBack, keyQuit}
	case StateFullscreen:
		keys := []key.Binding{keyFullscreen}
		if m.kittySent {
			keys = append(keys, keyPhoto)
		}
		return keys
	}
	keys := []key.Binding{keyExplanation, keyLink, keyReload, keyFullscreen, keyQuit}
	if m.kittySent {
		keys = slices.Insert(keys, 4, keyPhoto)
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
	lines := strings.Split(m.View(), "\n")
	if y < 0 || y >= len(lines) {
		return ""
	}
	return ansi.Strip(lines[y])
}

// helpHitTest returns the key under the mouse position, or "".
func (m *Model) helpHitTest(x, y int) string {
	line := m.frameLine(y)
	keys := m.helpKeys()
	// only treat the line as the help bar if most items are present
	matches := 0
	for _, k := range keys {
		h := k.Help()
		if strings.Contains(line, h.Key+" "+h.Desc) {
			matches++
		}
	}
	if matches < 2 {
		return ""
	}
	for _, k := range keys {
		h := k.Help()
		if start, end, ok := columnSpan(line, h.Key+" "+h.Desc); ok && x >= start && x < end {
			return h.Key
		}
	}
	return ""
}

// linkHitTest reports whether the mouse is on the URL in the link view.
func (m *Model) linkHitTest(x, y int) bool {
	if m.State != StateLink || m.apod == nil {
		return false
	}
	line := m.frameLine(y)
	start, end, ok := columnSpan(line, m.apod.Link())
	return ok && x >= start && x < end
}

func (m *Model) viewLoading() string {
	// no emoji here: their ambiguous width breaks bubbletea's line diffing,
	// leaving stale "loading" fragments behind
	return m.txtYellow().Width(m.Width).Height(m.Height).Align(lipgloss.Center, lipgloss.Center).Render(m.spinner.View() + " loading...")
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
func (m *Model) viewAPODText(width int, writeExplanation bool) string {
	txt := m.Style
	var s strings.Builder

	// header
	s.WriteString(m.txtMuted().Render("🌌 Astronomy Picture of the Day"))
	s.WriteString("\n")

	// apod
	if m.apod == nil {
		s.WriteString(txt.Render("error fetching APOD :("))
		s.WriteString("\n")
		return s.String()
	}
	s.WriteString(m.txtMuted().Render(m.apod.ApodDate.Format(time.DateOnly)))
	if m.reloadedRecently {
		s.WriteString(m.divDot().Render() + m.txtYellow().Render("reloaded!"))
	}
	s.WriteString("\n")

	s.WriteString("\n")
	s.WriteString("\n")
	s.WriteString(txt.Width(width).Align(lipgloss.Center).Bold(true).Render(termenv.Hyperlink(m.apod.Link(), m.apod.Title)))
	s.WriteString("\n")
	s.WriteString("\n")

	if writeExplanation {
		s.WriteString(txt.Render(wordwrap.String(m.apod.Explanation, width)))
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
