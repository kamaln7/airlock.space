package airlockspace

import (
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kamaln7/airlock.space/apod"
	"github.com/peteretelej/nasa"
)

// Just a generic tea.Model to demo terminal information of ssh.
type Model struct {
	Width            int
	Height           int
	Style            lipgloss.Style
	State            State
	apod             *nasa.Image
	reloadedRecently bool
}

type State int

const (
	StateLoading State = iota
	StateAPOD
)

func (m *Model) Init() tea.Cmd {
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
		}
	case apodMsg:
		m.apod = msg
		m.State = StateAPOD
		cmds = append(cmds, tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
			m.reloadedRecently = false
			return apodMsg(m.apod)
		}))
	}
	return m, tea.Batch(cmds...)
}

type apodMsg *nasa.Image

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
	keyReload = key.NewBinding(
		key.WithKeys("r", "ctrl+r"),
		key.WithHelp("r", "reload"),
	)
	keyQuit = key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	)
)

func (m *Model) View() string {
	switch m.State {
	case StateLoading:
		return m.viewLoading()
	case StateAPOD:
		return m.viewAPOD()
	}
	return "error"
}

func (m *Model) viewAPOD() string {
	txt := m.Style.Width(m.Width)

	var s strings.Builder
	s.WriteString(txt.Render("Astronomy Picture of the Day"))
	s.WriteString("\n")
	if m.apod == nil {
		s.WriteString(txt.Render("error fetching APOD :("))
	} else {
		dateTxt := m.apod.ApodDate.Format(time.DateOnly)
		if m.reloadedRecently {
			dateTxt += m.divDot().Render() + m.txtYellow().Render("reloaded!")
		}
		s.WriteString(txt.Render(dateTxt))
		s.WriteString("\n")
		s.WriteString("\n")
		s.WriteString(txt.Align(lipgloss.Center).Bold(true).Render(m.apod.Title))
		s.WriteString("\n")
		s.WriteString("\n")
		s.WriteString(txt.Render(m.apod.Explanation))
	}

	hlp := help.New()
	hlp.Styles.ShortKey = hlp.Styles.ShortKey.Bold(true)
	return s.String() + "\n\n\n" + hlp.ShortHelpView([]key.Binding{keyQuit, keyReload})
}

func (m *Model) viewLoading() string {
	return m.txtYellow().Padding(3, 6).Render("loading...")
}

func (m *Model) txtMuted() lipgloss.Style {
	return m.Style.Foreground(lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"})
}

func (m *Model) txtSuperMuted() lipgloss.Style {
	return m.Style.Foreground(lipgloss.AdaptiveColor{Light: "#DDDADA", Dark: "#3C3C3C"})
}

func (m *Model) divDot() lipgloss.Style {
	return m.txtSuperMuted().SetString(" • ")
}

func (m *Model) txtYellow() lipgloss.Style {
	return m.Style.Foreground(lipgloss.Color("220"))
}
