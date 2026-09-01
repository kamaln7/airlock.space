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
	"sync"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/samber/lo"
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

	// cellW and cellH are the terminal's own cell size in pixels, which it
	// reports when asked. Zero until it answers, or forever if it will not.
	cellW, cellH int

	State          State
	apod           *apod.APOD
	date           time.Time // the day being browsed; zero until the first load
	latest         time.Time // the most recent day NASA has posted
	imageOK        bool      // image bytes are ready to render
	kittyReady     bool      // this day's photo is uploaded and placeable
	preferArt      bool      // sextant art instead of the real photo
	photoToggled   bool      // user overrode the art/photo default with the keybind
	copiedRecently bool
	hoverKey       string // help bar item under the mouse
	hoverLink      bool   // mouse is over the URL under the explanation
	mouseX         int    // last seen mouse position
	mouseY         int
	selAnchor      point // in-app text selection (mouse tracking eats the
	selEnd         point // terminal's own selection, so we provide our own)
	selActive      bool
	selPending     bool // left button went down inside the selectable region
	explCol        int  // where the explanation block landed, for selection
	explWidth      int
	explCache      string // memoized processed explanation
	explCacheFor   *apod.APOD
	vp             viewport.Model // the scrolling explanation
	vpFor          *apod.APOD     // the day its content was set from
	vpWidth        int
	sentW, sentH   int       // the pixel box the photo on screen was encoded for
	resizeGen      int       // which resize the pending settle belongs to
	daySince       time.Time // when the day on screen started loading
	photoSince     time.Time // when the photo started on its way
	placedCols     int       // current kitty virtual placement size
	placedRows     int
	imageLoading   bool
	spinFrame      int
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

// resizeSettle is how long the window must hold still before the photo is
// encoded for it.
const resizeSettle = 750 * time.Millisecond

// msgResized says a resize has stopped moving, if no later one has started.
type msgResized struct{ gen int }

func spinTick() tea.Cmd {
	return tea.Tick(time.Second/8, func(time.Time) tea.Msg { return msgSpin{} })
}

func (m *Model) spinnerView() string {
	return m.txtYellow().Render(spinnerFrames[m.spinFrame%len(spinnerFrames)])
}

func (m *Model) Init() tea.Cmd {
	m.daySince = time.Now()
	return tea.Batch(m.loadAPOD(m.date), spinTick(), tea.RequestBackgroundColor, requestCellSize)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	prevState := m.State

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Height = msg.Height
		m.Width = msg.Width
		m.applyArtDefault()
		// a resize can mean a new font size, and so a new cell
		m.resizeGen++
		gen := m.resizeGen
		cmds = append(cmds, requestCellSize, tea.Tick(resizeSettle, func(time.Time) tea.Msg {
			return msgResized{gen}
		}))
	case msgResized:
		// dragging a window edge fires these by the dozen and the photo is
		// megabytes, so nothing is sent until the dragging stops
		if msg.gen == m.resizeGen {
			cmds = append(cmds, m.sendKitty())
		}
	case uv.CellSizeEvent:
		if msg.Width > 0 && msg.Height > 0 {
			// no need to drop the placement: a new cell shape fits the picture
			// to a different box, and imageArea re-sends on any change of size
			m.cellW, m.cellH = msg.Width, msg.Height
		}
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, keyQuit):
			return m, tea.Quit
		case key.Matches(msg, keyPrevDay):
			cmds = append(cmds, m.showDay(m.date.AddDate(0, 0, -1)))
		case key.Matches(msg, keyNextDay):
			cmds = append(cmds, m.showDay(m.date.AddDate(0, 0, 1)))
		case key.Matches(msg, keyFullscreen):
			if !m.imageOK {
				break // nothing to make big on a video day
			}
			if m.State == StateFullscreen {
				m.State = StateAPOD
			} else {
				m.State = StateFullscreen
			}
			// the photo was cut for the column it sat in; fullscreen has room
			// for a sharper one, and sendKitty only goes again if it is bigger
			cmds = append(cmds, m.sendKitty())
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
		default:
			// anything this app has no use for scrolls the explanation, where
			// the explanation is what is on screen
			if m.explanationOnScreen() {
				var cmd tea.Cmd
				m.vp, cmd = m.vp.Update(msg)
				cmds = append(cmds, cmd)
			}
		}
	case tea.MouseWheelMsg:
		if m.explanationOnScreen() {
			m.vp, _ = m.vp.Update(msg)
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
		m.sentW, m.sentH = 0, 0
		m.imageLoading = true
		// a new day starts as art again: the photo is megabytes, and having
		// asked for it on one picture is not asking for it on every one after
		m.photoToggled = false
		m.applyArtDefault()
		m.placedCols, m.placedRows = 0, 0
		m.State = StateAPOD
		cmds = append(cmds, m.loadImage())
	case imageMsg:
		m.imageOK = msg.ok
		m.imageLoading = false
		m.applyArtDefault()
		cmds = append(cmds, m.sendKitty())
	case kittyMsg:
		m.photoSince = time.Time{}
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
	m.daySince = time.Now()
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
// defaultCellAspect is the usual terminal cell: twice as tall as it is wide.
const defaultCellAspect = 2

// requestCellSize asks the terminal how big one of its cells is. Sent through
// bubbletea so it lands in order with the frames, and answered - by terminals
// that implement it - with a CellSizeEvent.
func requestCellSize() tea.Msg {
	return tea.RawMsg{Msg: ansi.WindowOp(ansi.RequestCellSizeWinOp)}
}

// cellAspect is how many times taller this client's cells are than they are
// wide, which is what turns image pixels into rows and columns.
//
// Three sources, worst last. The terminal's own answer is the only one that
// knows about its cell metrics: ghostty's adjust-cell-height, say, makes cells
// taller without changing the window, and a terminal that reports its pixel
// size before applying that adjustment leaves the ssh numbers describing a
// cell it does not actually draw. Get that ratio wrong and the picture is
// fitted to the wrong shape - a margin the terminal, not we, put there.
func (m *Model) cellAspect() float64 {
	if m.cellW > 0 && m.cellH > 0 {
		return float64(m.cellH) / float64(m.cellW)
	}
	if m.WidthPixels > 0 && m.HeightPixels > 0 && m.Width > 0 && m.Height > 0 {
		return (float64(m.HeightPixels) / float64(m.Height)) /
			(float64(m.WidthPixels) / float64(m.Width))
	}
	return defaultCellAspect
}

func (m *Model) cellPixels() (w, h int) {
	if m.cellW > 0 && m.cellH > 0 {
		return m.cellW, m.cellH // the terminal's own answer
	}
	if m.WidthPixels > 0 && m.HeightPixels > 0 && m.Width > 0 && m.Height > 0 {
		return m.WidthPixels / m.Width, m.HeightPixels / m.Height
	}
	return 10, 20
}

// photoChrome is the rows the page keeps for itself around the picture: the
// header block, the link and the help bar, with their blank lines.
const photoChrome = 12

// photoBox is the device-pixel box the photo is encoded for - the cells it
// will actually be drawn into, not the whole terminal. In the main view that
// is a column beside the text, which on a wide screen is a fraction of the
// screen, and pixels are the only real lever on what a photo weighs: a
// lossless png of one is twice its jpeg whatever else is done to it. Only
// fullscreen wants the lot, and asks for it by going there.
func (m *Model) photoBox() (w, h int) {
	cw, ch := m.cellPixels()
	if m.State == StateFullscreen {
		return max(1, m.Width) * cw, max(1, m.Height) * ch
	}
	return min(imageMaxWidth, max(1, m.Width-2)) * cw, max(1, m.Height-photoChrome) * ch
}

// sendKitty uploads the photo to the terminal, once per day and only when the
// photo is what the reader is about to see. The payload is megabytes - a 565KB
// source jpeg becomes 5MB of base64 - and the sextant art, which is what most
// viewports get, needs none of it. Until it arrives, imageArea falls back to
// the art, so the wait shows something rather than nothing.
func (m *Model) sendKitty() tea.Cmd {
	if !m.KittyGraphics || m.Session == nil || !m.imageOK || m.preferArt {
		return nil
	}
	maxW, maxH := m.photoBox()
	if m.kittyReady && m.sentW >= maxW && m.sentH >= maxH {
		return nil // the one already up there is at least this sharp
	}
	a, out := m.apod, m.Session
	m.sentW, m.sentH = maxW, maxH
	m.photoSince = time.Now()
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
		chunks := kittyChunks(png)
		total := 0
		for _, c := range chunks {
			total += len(c)
		}
		// One chunk at a time, and how fast is the connection's business: a
		// terminal on the other end of this process wants them as quickly as
		// they will go, one at the far end of a network wants them paced.
		for _, c := range chunks {
			if _, err := io.WriteString(out, c); err != nil {
				slog.Warn("failed to send image to the terminal", "error", err)
				return kittyMsg{apod: a}
			}
		}
		slog.Info("sent photo", "date", a.Date, "box", fmt.Sprintf("%dx%d", maxW, maxH), "bytes", total)
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
		if m.apod != nil && !worthMentioning(m.daySince) {
			// hold the day already on screen: most are in hand and arrive in
			// a millisecond, and a spinner between them is just a flicker
			return m.viewAPOD()
		}
		return m.viewLoading()
	case StateAPOD:
		return m.viewAPOD()
	case StateFullscreen:
		return m.viewFullscreen()
	}
	return "error"
}

// the reading box. 72 columns is the measure the text column has always used;
// the height cap only stops a maximised terminal drawing one absurd column,
// and sits well above the longest explanation NASA posts, so in practice the
// terminal runs out of rows before the cap does. The picture gets a column of its own the same
// width at most, so neither crowds the other on a very wide screen.
const (
	explMaxWidth  = 72
	explMaxHeight = 40
	imageMaxWidth = 72
	imageMinWidth = 24 // narrower than this and a picture is not worth the room
	paneGap       = 4
	wideAspect    = 1.4 // past this a picture wants the width a text column wants

	// helpGutterPad keeps the stacked keys off the very edge of the screen
	helpGutterPad = 2
)

// imageIsWide reports whether the picture would rather have the full width
// than a column beside the text.
func (m *Model) imageIsWide() bool {
	sz := m.apod.ImageSize
	return sz.Y > 0 && float64(sz.X)/float64(sz.Y) > wideAspect
}

// explBlock renders the scrolling explanation and records where it landed.
// The selection helpers read those columns rather than guessing at them, which
// is what lets the text be selectable wherever the layout puts it.
func (m *Model) explBlock(width, maxHeight, col int) string {
	m.explCol, m.explWidth = col, max(0, width-scrollWidth)
	return m.viewExplanation(width, maxHeight)
}

// viewAPOD is the main view: the day's picture and its explanation together.
// Tall and square pictures take a column beside the text; wide ones sit above
// it, since a panorama and a column of text both want the same width.
func (m *Model) viewAPOD() string {
	avail := m.Width - 2 // side margins
	helpView := m.viewHelp()
	linkBlock := m.viewLinkLine()

	textW := min(explMaxWidth, avail)
	room := min(imageMaxWidth, avail-textW-paneGap)

	// how many rows the body has, measured against the widest the content
	// could be. Wide enough for a picture beside the text, the header is a
	// fixed five rows, so this does not shift under the layout it decides.
	probeW := textW
	if room >= imageMinWidth {
		probeW = textW + paneGap + room
	}
	chrome := countLines(m.viewAPODText(probeW)) + countLines(linkBlock) + countLines(helpView) + 2
	bodyH := max(3, m.Height-chrome)

	// the picture's column is sized to the picture, not the other way round: a
	// tall one is bound by the rows available and would otherwise sit in the
	// middle of a column twice its width
	hasImage := m.imageOK || m.imageLoading
	imgW, imgH := room, bodyH // while loading, hold the column open so the
	if m.imageOK {            // page does not jump when the picture arrives
		imgW, imgH = fitCells(m.apod.ImageSize.X, m.apod.ImageSize.Y, room, bodyH, m.cellAspect())
	}
	// the gate is horizontal room only. A tall picture is bound by the rows,
	// not the columns, so it fits to a narrow strip - and stacking it would
	// hand it half the rows and make it smaller still. Only a wide one, which
	// wants the width the text wants, is better off above the text.
	beside := hasImage && room >= imageMinWidth && !(m.imageOK && m.imageIsWide())

	contentW := textW
	switch {
	case beside:
		contentW = textW + paneGap + imgW
	case hasImage:
		contentW = min(avail, explMaxWidth+paneGap+imageMaxWidth)
	}

	var body string
	switch {
	case !hasImage:
		body = m.explBlock(textW, min(bodyH, explMaxHeight), (contentW-textW)/2)
	case beside:
		text := m.explBlock(textW, min(bodyH, explMaxHeight), imgW+paneGap)
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			m.imagePane(imgW, imgH),
			strings.Repeat(" ", paneGap),
			text)
	default:
		// a wide picture over the text, each with a share of the rows. The
		// text is narrower than the picture, so it has to be indented to the
		// column explBlock records - beside a picture the join does that.
		imgH := max(3, min(bodyH/2, bodyH-6))
		col := (contentW - textW) / 2
		text := m.explBlock(textW, min(bodyH-imgH-1, explMaxHeight), col)
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.imagePane(contentW, imgH), "", txt.MarginLeft(col).Render(text))
	}

	// rendered again now the body has been built: whether the explanation
	// scrolls is only known once the viewport has its content, and the first
	// frame of a day would otherwise offer no scroll hint. Its line count does
	// not change, so the rows budgeted above still hold.
	helpView = m.viewHelp()

	content := lipgloss.JoinVertical(lipgloss.Left,
		m.viewAPODText(contentW),
		// the body holds the rows it was given whether or not it fills them,
		// so a video day - explanation and nothing beside it - sits where a
		// day with a picture does, top-aligned, rather than floating to the
		// middle of the screen with the link and help bar tucked under it
		txt.Width(contentW).Height(bodyH).AlignVertical(lipgloss.Top).Render(body),
		"",
		txt.Width(contentW).Align(lipgloss.Center).Render(linkBlock),
		txt.Width(contentW).Align(lipgloss.Center).Render(helpView),
	)
	// Place centers every line, so the explanation's own columns shift by the
	// same margin the content box does
	m.explCol += (m.Width - contentW) / 2
	return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, content)
}

// imagePane is the picture in a box of its own height, never a taller one:
// padding it to the rows available would centre it, which puts blank rows
// above and below the picture and drops its top edge below the text beside it.
// Only the spinner, which has no size of its own, gets the full box.
func (m *Model) imagePane(w, h int) string {
	if !m.imageOK {
		if !worthMentioning(m.daySince) {
			return txt.Width(w).Height(h).Render("") // cached: it is about to be here
		}
		return txt.Width(w).Height(h).Align(lipgloss.Center, lipgloss.Center).Render(m.loadingLine())
	}
	return txt.Width(w).Align(lipgloss.Center).Render(m.picture(w, h))
}

// viewLinkLine is the clickable URL shown under the explanation, with inline
// copy feedback. Same row either way, so the layout doesn't jump.
func (m *Model) viewLinkLine() string {
	// the feedback replaces the URL rather than sitting under it: one row
	// either way, so nothing shifts, and no row is reserved for a hint that
	// is almost never showing
	if m.copiedRecently {
		return m.txtYellow().Bold(true).Render("link copied!")
	}
	link := m.apod.Link()
	st := m.txtMuted()
	if m.hoverLink {
		st = m.txtYellow().Underline(true)
	}
	// hyperlink outside the style, never inside: lipgloss styles rune by rune
	// under Underline, which shreds an embedded escape into literal text
	return hyperlink(link, st.Render(link))
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
		return append([]key.Binding{keyFullscreen}, append(m.photoKey(), keyQuit)...)
	}
	// the day arrows live in the header now, not here
	var keys []key.Binding
	if m.imageOK {
		keys = append(append(keys, keyFullscreen), m.photoKey()...)
	}
	// only worth saying when there is more explanation than is on screen
	if m.explanationOnScreen() && m.scrolls() {
		keys = append(keys, key.NewBinding(
			key.WithKeys("down", "j", glyphDown),
			key.WithHelp(glyphDown, "scroll"),
		))
	}
	return append(keys, keyCopy, keyQuit) // q quit stays last
}

// photoKey is the photo/ascii toggle, offered wherever the picture is and the
// client can draw a real one. Its label names the action, not both sides.
func (m *Model) photoKey() []key.Binding {
	if !m.KittyGraphics || !m.imageOK {
		return nil
	}
	desc := "ascii"
	if m.preferArt {
		desc = "image"
	}
	return []key.Binding{key.NewBinding(key.WithKeys("p", "ctrl+p"), key.WithHelp("p", desc))}
}

// viewHelp renders the help bar. Items under the mouse are highlighted; Update
// hit-tests clicks against the same "key desc" spans.
func (m *Model) viewHelp() string {
	var items []string
	for _, k := range m.helpKeys() {
		items = append(items, m.helpItem(k))
	}
	return txt.MarginTop(1).Render(strings.Join(items, m.divDot().Render()))
}

// helpItem renders one keybinding, lit up when the mouse is on it.
func (m *Model) helpItem(k key.Binding) string {
	h := k.Help()
	keySt, descSt := txt.Bold(true), m.txtMuted()
	if m.hoverKey == h.Key {
		keySt = keySt.Foreground(colorYellow).Underline(true)
		descSt = m.txtYellow()
	}
	return keySt.Render(h.Key) + " " + descSt.Render(h.Desc)
}

// viewHelpStacked is the same bar running down instead of across, for the
// margin beside a tall picture in fullscreen.
func (m *Model) viewHelpStacked() string {
	var rows []string
	for _, k := range m.helpKeys() {
		rows = append(rows, m.helpItem(k))
	}
	return strings.Join(rows, "\n")
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

	// explanation rows, matched against the actual text so the picture beside
	// them, the gaps and the scrollbar never highlight
	if m.State == StateAPOD && m.explWidth > 0 {
		colStart := m.explCol
		colEnd := min(m.explCol+m.explWidth, ansi.StringWidth(line))
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
	// two items is the rule where explanation text shares the frame and could
	// otherwise be mistaken for the bar. Stacked down a fullscreen margin a
	// row carries one item - and the only text on that frame is the bar.
	if m.State == StateFullscreen {
		return matches >= 1
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

// the scrollbar is drawn rather than composed: the viewport offers a left
// gutter, but the bar belongs on the right, away from the text.
const (
	scrollTrack = "\u2502"
	scrollThumb = "\u2588"
	scrollWidth = 2 // the bar and the space before it
)

// explKeyMap is the viewport's keys with this app's own removed. The stock map
// takes left/right and h/l, which are the day arrows, and f, which is
// fullscreen; scrolling would quietly eat all three.
func explKeyMap() viewport.KeyMap {
	return viewport.KeyMap{
		Up:           key.NewBinding(key.WithKeys("up", "k")),
		Down:         key.NewBinding(key.WithKeys("down", "j", glyphDown)),
		PageUp:       key.NewBinding(key.WithKeys("pgup", "b")),
		PageDown:     key.NewBinding(key.WithKeys("pgdown", "space")),
		HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u")),
		HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d")),
	}
}

// syncViewport points the viewport at the day on screen, re-wrapping only when
// the day or the width it is wrapped to changes. Its height follows the
// content up to maxHeight, so a short explanation stays a short block.
func (m *Model) syncViewport(width, maxHeight int) {
	if m.vpFor != m.apod || m.vpWidth != width {
		// wrapped here rather than by the viewport: SoftWrap breaks mid-word
		m.vp.KeyMap = explKeyMap()
		m.vp.SetWidth(width)
		m.vp.SetContent(lipgloss.Wrap(m.explanationText(), width, ""))
		m.vp.SetYOffset(0) // a new day starts at its beginning
		m.vpFor, m.vpWidth = m.apod, width
	}
	m.vp.SetHeight(max(1, min(m.vp.TotalLineCount(), maxHeight)))
}

// explanationOnScreen reports whether the explanation is the thing being
// read, which is what decides whether scroll input belongs to it.
func (m *Model) explanationOnScreen() bool {
	return m.State == StateAPOD
}

// scrolls reports whether the explanation has more than fits on screen.
func (m *Model) scrolls() bool {
	return m.vp.TotalLineCount() > m.vp.Height()
}

// scrollbar draws the track and thumb beside the explanation, nothing at all
// when it all fits.
func (m *Model) scrollbar() string {
	h, total := m.vp.Height(), m.vp.TotalLineCount()
	if total <= h || h < 1 {
		return ""
	}
	thumb := max(1, h*h/total)
	pos := 0
	if span := total - h; span > 0 {
		pos = m.vp.YOffset() * (h - thumb) / span
	}
	var b strings.Builder
	for i := range h {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i >= pos && i < pos+thumb {
			b.WriteString(m.txtMuted().Render(scrollThumb))
		} else {
			b.WriteString(m.txtSuperMuted().Render(scrollTrack))
		}
	}
	return b.String()
}

// viewExplanation is the scrolling explanation with its scrollbar. The bar's
// columns are reserved whether or not it is showing, so the text does not
// reflow the moment it becomes scrollable.
func (m *Model) viewExplanation(width, maxHeight int) string {
	m.syncViewport(max(1, width-scrollWidth), maxHeight)
	if sb := m.scrollbar(); sb != "" {
		return lipgloss.JoinHorizontal(lipgloss.Top, m.vp.View(), " ", sb)
	}
	return m.vp.View()
}

// loadingDelay is how long something may take before it is worth saying so.
// Under it, a spinner or a banner is a flash and nothing more - the reader
// sees a flicker where they should have seen the thing they asked for.
const loadingDelay = 100 * time.Millisecond

// worthMentioning reports whether work started at t has been going long enough
// to be worth telling the reader about. Zero means nothing is running.
func worthMentioning(t time.Time) bool {
	return !t.IsZero() && time.Since(t) >= loadingDelay
}

// picture is the image area with the sending notice laid over the middle of it
// while the real photo is on its way.
//
// Not held back the tenth of a second a spinner is: that delay asks how long
// we have been busy, and here that is the wrong clock. The sending is over in
// a moment - the bytes are in the pipe, not on the screen - while the client
// spends whatever it spends drawing megabytes. The notice is written ahead of
// them and taken away after, so what governs how long it shows is the picture
// travelling, and the answer is never nothing. Sextant art is what is on screen until
// it lands, so the notice goes on top of that rather than replacing it: the
// reader keeps the picture and gains the reason it is about to change.
func (m *Model) picture(w, h int) string {
	art := m.imageArea(w, h)
	if m.photoSince.IsZero() {
		return art
	}
	notice := m.txtYellow().Bold(true).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.color(colorMuted)).
		Padding(0, 2).
		Render("loading photo\u2026")

	return overlayCenter(art, notice)
}

// overlayCenter lays over across the middle of base, keeping base's size and
// what is around it. Spliced by hand, the way the selection highlight is:
// lipgloss's cell canvas drew one layer or the other here, never both.
func overlayCenter(base, over string) string {
	lines := strings.Split(base, "\n")
	rows := strings.Split(over, "\n")
	bw, ow := lipgloss.Width(base), lipgloss.Width(over)
	if ow > bw || len(rows) > len(lines) {
		return over // no room to lay it over, and it is the more useful of the two
	}
	top, left := (len(lines)-len(rows))/2, (bw-ow)/2
	for i, r := range rows {
		line := lines[top+i]
		lines[top+i] = ansi.Cut(line, 0, left) + r + ansi.TruncateLeft(line, left+ow, "")
	}
	return strings.Join(lines, "\n")
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
func Goodbye(width int, client string) string {
	if width <= 0 {
		width = 80 // no size to hand: assume the classic terminal
	}
	muted := txt.Foreground(colorMuted.dark)
	yellow := txt.Foreground(colorYellow)

	msg := yellow.Render(" ♥ thanks for visiting! wishing you clear skies ~") + "\n\n"

	// the art used to sit beside the explanation, in the space the picture now
	// has. This is the one place left with room and nothing competing for it.
	// Printed to the scrollback, where height costs nothing, so only width is
	// a real limit - and every art has to fit some terminal, or the shuffle
	// returns the same one every time.
	art := randomArt(client, width-2, 40,
		colorMuted.dark, colorCosmic.dark, colorStellar.dark, colorNebula.dark)
	if art != "" {
		art = "\n" + art + "\n"
	}

	// StaleOnError can return a usable stale APOD alongside an error
	a, _ := apod.Today()
	if a == nil {
		return "\n" + art + msg
	}
	return fmt.Sprintf("\n%s\n 🌌 %s %s\n 🔗 %s\n\n%s",
		art,
		txt.Bold(true).Render(a.Title),
		muted.Render("— "+a.ApodDate.Format(time.DateOnly)),
		yellow.Render(a.Link()),
		msg)
}

// artMemory bounds how many clients the goodbye remembers. The log is a
// nicety on a 512MB box, so it is capped and the least recently seen client is
// forgotten first. What each one holds is bounded too: once a client has seen
// every art that fits, the slate is wiped and they start again.
const artMemory = 256

// ponytail: eviction walks the order slice, which is fine at 256 entries and
// once per session; a real LRU if the cap ever grows.
var artLog = struct {
	sync.Mutex
	seen  map[string][]int // client -> art indices shown lately
	order []string         // least recently seen first
}{seen: map[string][]int{}}

// pickArt chooses one of the arts that fit, preferring any this client has not
// been shown lately, so a returning visitor gets a different drawing.
func pickArt(client string, fits []int) int {
	artLog.Lock()
	defer artLog.Unlock()

	seen := artLog.seen[client]
	unseen := make([]int, 0, len(fits))
	for _, i := range fits {
		if !slices.Contains(seen, i) {
			unseen = append(unseen, i)
		}
	}
	if len(unseen) == 0 {
		// they have had them all; forget and start the rotation over
		unseen, seen = fits, nil
	}
	pick := lo.Sample(unseen)

	artLog.seen[client] = append(seen, pick)
	artLog.order = append(slices.DeleteFunc(artLog.order, func(c string) bool {
		return c == client
	}), client)
	for len(artLog.order) > artMemory {
		delete(artLog.seen, artLog.order[0])
		artLog.order = artLog.order[1:]
	}
	return pick
}

// randomArt picks an ascii art that fits a box of cells, colorized, avoiding
// what this client saw last time where it can.
func randomArt(client string, maxWidth, maxHeight int, colors ...color.Color) string {
	var fits []int
	for i, art := range ASCIIAll {
		if countLines(art) <= maxHeight && lipgloss.Width(art) <= maxWidth {
			fits = append(fits, i)
		}
	}
	if len(fits) == 0 {
		return ""
	}
	return colorize(ASCIIAll[pickArt(client, fits)], colors...)
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
	// both paths get the same aspect-correct box. Handing kitty the whole box
	// and letting it scale looks tempting - it knows its own cell metrics -
	// but the protocol stretches an image to fill whatever c x r it is given
	// once both are named, so the box itself has to carry the aspect ratio.
	c, r := fitCells(m.apod.ImageSize.X, m.apod.ImageSize.Y, cols, rows, m.cellAspect())
	if c < 1 || r < 1 {
		return ""
	}

	if m.kittyReady && !m.preferArt {
		c, r = min(c, len(kittyDiacritics)), min(r, len(kittyDiacritics))
		if m.placedCols != c || m.placedRows != r {
			// virtual placements draw nothing; safe to write outside the renderer
			io.WriteString(m.Session, kittyVirtualPlacement(c, r))
			m.placedCols, m.placedRows = c, r
		}
		return kittyPlaceholders(c, r)
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
	glyphDown = "\u2193"
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

// viewNav is the header's calendar line: the day on screen with the days
// either side of it to step to. The title sits on its own row beneath it.
//
// One row while it fits: the arrows pinned to the edges of a navMaxWidth
// container with the day centered between them, which is justify-between with
// the middle item centered rather than merely evenly spaced - so the day holds
// the centre whatever the arrows either side of it are called. Too tight for
// that and the three blocks wrap onto rows of their own, as flex-wrap would.
// The caller centers whatever comes back.
func (m *Model) viewNav(width int) string {
	prev, next := m.dayArrows()
	center := m.txtMuted().Render(m.apod.ApodDate.Format("Jan 2, 2006"))

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

// viewAPODText renders the header block: the app name, the calendar and the
// title. The explanation is its own scrolling block - see viewExplanation.
func (m *Model) viewAPODText(width int) string {
	var s strings.Builder

	header := m.txtMuted().Render("🌌 Astronomy Picture of the Day")

	if m.apod == nil {
		s.WriteString(header)
		s.WriteString("\n")
		s.WriteString(txt.Render("error fetching APOD :("))
		s.WriteString("\n")
		return s.String()
	}

	// calendar over title, both centered, on rows of their own: with a date on
	// each arrow the day line is too wide to share one with the header, so the
	// flexbox the title used to do would only ever have collapsed - and jumped
	// between days as it did
	title := hyperlink(m.apod.Link(), txt.Bold(true).Render(m.apod.Title))
	s.WriteString(header)
	s.WriteString("\n\n")
	s.WriteString(txt.Width(width).Align(lipgloss.Center).Render(m.viewNav(width) + "\n" + title))
	s.WriteString("\n\n")

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

	// a tall picture leaves a margin either side of itself; the keys go down
	// the left of it rather than stealing a row from the bottom
	imgW, imgH := fitCells(m.apod.ImageSize.X, m.apod.ImageSize.Y,
		m.Width, m.Height, m.cellAspect())
	stacked := m.viewHelpStacked()
	if side := (m.Width - imgW) / 2; side >= lipgloss.Width(stacked)+helpGutterPad {
		// centered down the margin, but the keys line up with each other
		// rather than each row centering on its own and coming out ragged
		gutter := txt.Width(side).Height(imgH).AlignVertical(lipgloss.Center)
		return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center,
			lipgloss.JoinHorizontal(lipgloss.Top,
				gutter.Align(lipgloss.Left).PaddingLeft(helpGutterPad).Render(stacked),
				txt.Width(imgW).Render(m.picture(imgW, imgH)),
				gutter.Render(""),
			))
	}

	// otherwise the picture takes all but the last row and the keys sit
	// centered beneath it, on a line of their own rather than spliced into an
	// ANSI-heavy image row
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.Place(m.Width, m.Height-1, lipgloss.Center, lipgloss.Center,
			m.picture(m.Width, m.Height-1)),
		txt.Width(m.Width).Align(lipgloss.Center).Render(strings.TrimSpace(m.viewHelp())),
	)
}
