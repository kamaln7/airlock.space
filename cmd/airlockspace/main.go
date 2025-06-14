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
	m := &airlockspace.Model{
		Style: lipgloss.NewRenderer(os.Stdout).NewStyle(),
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
