package main

// An example Bubble Tea server. This will put an ssh session into alt screen
// and continually print up to date terminal information.

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	airlockspace "github.com/kamaln7/airlock.space"
)

const (
	host = "localhost"
	port = "23234"
)

func main() {
	renderer := lipgloss.NewRenderer(os.Stdout)
	txtStyle := renderer.NewStyle().Foreground(lipgloss.Color("10"))
	quitStyle := renderer.NewStyle().Foreground(lipgloss.Color("8"))

	bg := "light"
	if renderer.HasDarkBackground() {
		bg = "dark"
	}

	m := airlockspace.Model{
		Profile:   renderer.ColorProfile().Name(),
		Bg:        bg,
		TxtStyle:  txtStyle,
		QuitStyle: quitStyle,
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
