package airlockspace

import (
	"fmt"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/muesli/reflow/wordwrap"
	"github.com/qeesung/image2ascii/convert"
	"github.com/samber/lo"
	lom "github.com/samber/lo/mutable"
)

var ansiRegex = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")
var (
	colorMuted      = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
	colorSuperMuted = lipgloss.AdaptiveColor{Light: "#DDDADA", Dark: "#3C3C3C"}
	colorNebula     = lipgloss.AdaptiveColor{Light: "#B4A7D6", Dark: "#6B4E8C"} // Purple nebula tones
	colorCosmic     = lipgloss.AdaptiveColor{Light: "#A7D6D6", Dark: "#4E8C8C"} // Deep space teal
	colorStellar    = lipgloss.AdaptiveColor{Light: "#D6B4A7", Dark: "#8C6B4E"} // Warm star glow
)

// Just a generic tea.Model to demo terminal information of ssh.
type Model struct {
	Width            int
	Height           int
	Style            lipgloss.Style
	State            State
	imgOrExplanation bool // true -> img, false -> explanation
	apod             *apod.APOD
	reloadedRecently bool
}

type State int

const (
	StateLoading State = iota
	StateAPOD
	StateLink
)

func (m *Model) Init() tea.Cmd {
	m.imgOrExplanation = true
	return nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.apod == nil {
		cmds = append(cmds, m.loadAPOD())
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Height = msg.Height
		m.Width = msg.Width
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keyQuit):
			return m, tea.Quit
		case key.Matches(msg, keyReload):
			m.reloadedRecently = true
			m.State = StateLoading
			cmds = append(cmds, m.loadAPOD())
		case key.Matches(msg, keyExplanation):
			m.State = StateAPOD
			m.imgOrExplanation = !m.imgOrExplanation
		case key.Matches(msg, keyLink):
			if m.State == StateLink {
				m.State = StateAPOD
			} else {
				m.State = StateLink
			}
		}
	case apodMsg:
		m.apod = msg
		m.State = StateAPOD
		cmds = append(cmds, tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
			m.reloadedRecently = false
			return msgRerender{}
		}))
	}
	return m, tea.Batch(cmds...)
}

type msgRerender struct{}

type apodMsg *apod.APOD

func (m *Model) loadAPOD() tea.Cmd {
	return func() tea.Msg {
		apod, err := apod.Today()
		if err != nil {
			slog.Warn("failed to get APOD", "error", err)
			if apod == nil {
				slog.Error("no valid APOD to fallback to", "error", err)
			}
		}
		return apodMsg(apod)
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
)

func (m *Model) View() string {
	switch m.State {
	case StateLoading:
		return m.viewLoading()
	case StateAPOD:
		return m.viewAPOD()
	case StateLink:
		return m.viewLink()
	}
	return "error"
}

func (m *Model) viewAPOD() string {
	totalWidth := m.Width - 2 // -2 for the margin
	apodWidth := min(60, totalWidth)
	freeWidth := totalWidth - apodWidth
	if m.imgOrExplanation {
		// full-width apod if we are showing the image
		apodWidth = totalWidth
		freeWidth = totalWidth
	}
	apodView := (&apodView{
		apod:             m.apod,
		style:            m.Style,
		reloadedRecently: m.reloadedRecently,
		width:            apodWidth,
		txtMuted:         m.txtMuted,
		txtYellow:        m.txtYellow,
		divDot:           m.divDot,
		writeExplanation: !m.imgOrExplanation,
	}).View()
	helpView := m.viewHelp()

	freeHeight := m.Height - 3 - countLines(helpView) // -3 for the margins
	if m.imgOrExplanation {
		freeHeight -= countLines(apodView)
		image, err := m.apod.ImageDecoded()
		if err != nil {
			slog.Error("failed to get image decoded", "error", err)
		}
		converter := convert.NewImageConverter()

		imageWidth, imageHeight := fitImage(image.Bounds().Dx(), image.Bounds().Dy(), freeWidth, freeHeight)
		asciiImage := converter.Image2ASCIIString(image, &convert.Options{
			Colored:     true,
			FixedWidth:  imageWidth,
			FixedHeight: imageHeight,
		})
		return m.Style.Margin(1, 1).Render(
			lipgloss.JoinVertical(lipgloss.Left,
				apodView,
				m.Style.Width(freeWidth).Height(freeHeight).Align(lipgloss.Center, lipgloss.Center).Render(asciiImage),
				helpView,
			),
		)
	} else {
		// find ascii art fitting the free width and height
		var asciiArt string
		allAsciiArt := slices.Clone(ASCIIAll)
		lom.Shuffle(allAsciiArt)
		for _, art := range allAsciiArt {
			if countLines(art) > freeHeight {
				continue
			}
			longestLine := lenLongest(strings.Split(art, "\n")...)
			if longestLine > freeWidth {
				continue
			}
			asciiArt = colorize(m.Style, art, colorMuted, colorCosmic, colorStellar, colorNebula)
			break
		}

		return m.Style.Margin(1, 1).Render(
			lipgloss.JoinHorizontal(lipgloss.Top,
				apodView+helpView,
				m.Style.Width(freeWidth).Height(freeHeight).Align(lipgloss.Center, lipgloss.Center).Render(asciiArt),
			),
		)
	}
}

func (m *Model) viewLink() string {
	helpView := m.viewHelp()
	return m.txtYellow().Width(m.Width).Height(m.Height).Align(lipgloss.Center, lipgloss.Center).Render(
		"🔗 link to APOD:\n\n" + fmt.Sprintf("https://apod.nasa.gov/apod/ap%s.html", m.apod.ApodDate.Format("060102")) + "\n" + helpView,
	)
}

func (m *Model) viewHelp() string {
	hlp := help.New()
	hlp.Styles.ShortKey = hlp.Styles.ShortKey.Bold(true)
	hlpView := hlp.ShortHelpView([]key.Binding{keyExplanation, keyLink, keyReload, keyQuit})
	return m.Style.MarginTop(1).Render(hlpView)
}

func (m *Model) viewLoading() string {
	return m.txtYellow().Width(m.Width).Height(m.Height).Align(lipgloss.Center, lipgloss.Center).Render("✨ loading...")
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

func lenLongest(strs ...string) int {
	max := 0
	for _, str := range strs {
		// Strip ANSI escape sequences before measuring length
		stripped := ansiRegex.ReplaceAllString(str, "")
		// Count runes (including emojis) instead of bytes
		if count := utf8.RuneCountInString(stripped); count > max {
			max = count
		}
	}
	return max
}

func countLines(str string) int {
	return len(strings.Split(str, "\n"))
}

type apodView struct {
	apod             *apod.APOD
	style            lipgloss.Style
	reloadedRecently bool
	width            int
	writeExplanation bool
	txtMuted         func() lipgloss.Style
	txtYellow        func() lipgloss.Style
	divDot           func() lipgloss.Style
}

func (v *apodView) View() string {
	txt := v.style
	var s strings.Builder

	// header
	s.WriteString(v.txtMuted().Render("🌌 Astronomy Picture of the Day"))
	s.WriteString("\n")

	// apod
	if v.apod == nil {
		s.WriteString(txt.Render("error fetching APOD :("))
		s.WriteString("\n")
		return s.String()
	}
	s.WriteString(v.txtMuted().Render(v.apod.ApodDate.Format(time.DateOnly)))
	if v.reloadedRecently {
		s.WriteString(v.divDot().Render() + v.txtYellow().Render("reloaded!"))
	}
	s.WriteString("\n")

	s.WriteString("\n")
	s.WriteString("\n")
	s.WriteString(txt.Width(v.width).Align(lipgloss.Center).Bold(true).Render(v.apod.Title))
	s.WriteString("\n")
	s.WriteString("\n")

	if v.writeExplanation {
		s.WriteString(txt.Render(wordwrap.String(v.apod.Explanation, v.width)))
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
