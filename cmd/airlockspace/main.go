package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	airlockspace "github.com/kamaln7/airlock.space"
)

func main() {
	// slog must not write to the TUI's terminal: a stray log line scrolls the
	// screen and desyncs bubbletea's renderer (stale frame fragments)
	logPath := filepath.Join(os.TempDir(), "airlockspace.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	}

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

	// alt screen and mouse mode are View fields in bubbletea v2, not options
	m := &airlockspace.Model{KittyGraphics: kitty, Session: os.Stdout}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	colorprofile.NewWriter(os.Stdout, os.Environ()).WriteString(airlockspace.Goodbye())
}
