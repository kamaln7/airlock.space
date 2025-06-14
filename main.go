package airlockspace

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Just a generic tea.Model to demo terminal information of ssh.
type Model struct {
	Term      string
	Profile   string
	Width     int
	Height    int
	Bg        string
	TxtStyle  lipgloss.Style
	QuitStyle lipgloss.Style
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Height = msg.Height
		m.Width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) View() string {
	s := fmt.Sprintf("Your term is %s\nYour window size is %dx%d\nBackground: %s\nColor Profile: %s", m.Term, m.Width, m.Height, m.Bg, m.Profile)
	return m.TxtStyle.Render(s) + "\n\n" + m.QuitStyle.Render("Press 'q' to quit\n")
}
