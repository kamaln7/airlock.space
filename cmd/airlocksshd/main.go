package main

// An example Bubble Tea server. This will put an ssh session into alt screen
// and continually print up to date terminal information.

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	airlockspace "github.com/kamaln7/airlock.space"
	"github.com/muesli/termenv"
)

var (
	host = GetEnv("SSH_HOST", "localhost")
	port = GetEnv("SSH_PORT", "23234")
)

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(GetEnv("SSH_HOST_KEY", ".airlocksshd/id_ed25519")),
		wish.WithMiddleware(
			bubbletea.Middleware(teaHandler),
			activeterm.Middleware(), // Bubble Tea apps usually require a PTY.
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatal("could not create server", "error", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		var err error
		if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) {
			// systemd run
			f := os.NewFile(3, "from systemd")
			l, err := net.FileListener(f)
			if err != nil {
				log.Fatal("could not create listener", "error", err)
			}
			log.Info("starting SSH server", "socket", "fd:3")
			err = s.Serve(l)
		} else {
			log.Info("starting SSH server", "host", host, "port", port)
			err = s.ListenAndServe()
		}
		if err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("error starting server", "error", err)
			done <- nil
		}
	}()

	<-done
	log.Info("stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Error("could not stop server", "error", err)
	}
}

// You can wire any Bubble Tea model up to the middleware with a function that
// handles the incoming ssh.Session. Here we just grab the terminal info and
// pass it to the new model. You can also return tea.ProgramOptions (such as
// tea.WithAltScreen) on a session by session basis.
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	// This should never fail, as we are using the activeterm middleware.
	pty, _, _ := s.Pty()

	// When running a Bubble Tea app over SSH, you shouldn't use the default
	// lipgloss.NewStyle function.
	// That function will use the color profile from the os.Stdin, which is the
	// server, not the client.
	// We provide a MakeRenderer function in the bubbletea middleware package,
	// so you can easily get the correct renderer for the current session, and
	// use it to create the styles.
	// The recommended way to use these styles is to then pass them down to
	// your Bubble Tea model.
	renderer := bubbletea.MakeRenderer(s)
	var colorTerm string
	var isIterm2 bool
	for _, env := range s.Environ() {
		if strings.HasPrefix(env, "COLORTERM=") {
			colorTerm = strings.TrimPrefix(env, "COLORTERM=")
			continue
		}

		if strings.EqualFold(env, "TERM_PROGRAM=iTerm2") || strings.EqualFold(env, "LC_TERMINAL=iTerm2") {
			isIterm2 = true
			continue
		}
	}
	renderer.SetColorProfile(getSSHTermInfo(pty.Term, colorTerm, isIterm2))

	m := &airlockspace.Model{
		Width:  pty.Window.Width,
		Height: pty.Window.Height,
		Style:  renderer.NewStyle(),
	}
	return m, []tea.ProgramOption{tea.WithAltScreen()}
}

func GetEnv(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func getSSHTermInfo(term, colorTerm string, isIterm2 bool) termenv.Profile {
	term = strings.ToLower(term)
	colorTerm = strings.ToLower(colorTerm)

	if isIterm2 {
		return termenv.TrueColor
	}

	switch colorTerm {
	case "24bit", "truecolor":
		return termenv.TrueColor
	case "yes", "true":
		return termenv.ANSI256
	}

	switch term {
	case
		"alacritty",
		"contour",
		"rio",
		"wezterm",
		"xterm-ghostty",
		"xterm-kitty":
		return termenv.TrueColor
	case "linux", "xterm":
		return termenv.ANSI
	}

	if strings.Contains(term, "256color") {
		return termenv.ANSI256
	}
	if strings.Contains(term, "color") {
		return termenv.ANSI
	}
	if strings.Contains(term, "ansi") {
		return termenv.ANSI
	}

	return termenv.Ascii
}
