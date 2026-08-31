package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	airlockspace "github.com/kamaln7/airlock.space"
	"github.com/muesli/termenv"
)

func main() {
	// slog must not write to the TUI's terminal: a stray log line scrolls the
	// screen and desyncs bubbletea's renderer (stale frame fragments)
	logPath := filepath.Join(os.TempDir(), "airlockspace.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	}

	renderer := lipgloss.NewRenderer(os.Stdout)
	term, termProgram := os.Getenv("TERM"), os.Getenv("TERM_PROGRAM")
	kitty := false
	switch strings.ToLower(term) {
	case "xterm-kitty", "xterm-ghostty":
		kitty = true
	}
	switch strings.ToLower(termProgram) {
	case "kitty", "ghostty":
		kitty = true
	}
	if os.Getenv("NO_KITTY") != "" {
		kitty = false
	}

	m := &airlockspace.Model{
		Style:         renderer.NewStyle(),
		Profile:       termenv.ColorProfile(),
		KittyGraphics: kitty,
		Session:       os.Stdout,
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	if _, err := p.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Print(airlockspace.Goodbye(renderer))
}
