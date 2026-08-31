package main

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
	"github.com/charmbracelet/lipgloss"
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

// profileKey stores the detected termenv.Profile in the session context.
var profileKey struct{ _ byte }

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(GetEnv("SSH_HOST_KEY", ".airlocksshd/id_ed25519")),
		// ponytail: flat 1h idle cutoff; per-session activity tracking if long-lived dashboards matter
		wish.WithIdleTimeout(time.Hour),
		wish.WithMiddleware(
			bubbletea.Middleware(teaHandler),
			// runs after the tea program exits, outside the alt screen
			func(next ssh.Handler) ssh.Handler {
				return func(s ssh.Session) {
					next(s)
					re := lipgloss.NewRenderer(s)
					if p, ok := s.Context().Value(profileKey).(termenv.Profile); ok {
						re.SetColorProfile(p)
					}
					wish.WriteString(s, airlockspace.Goodbye(re))
				}
			},
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
	var colorTerm, termProgram string
	for _, env := range s.Environ() {
		if v, ok := strings.CutPrefix(env, "COLORTERM="); ok {
			colorTerm = v
		}
		if v, ok := strings.CutPrefix(env, "TERM_PROGRAM="); ok {
			termProgram = v
		}
		if v, ok := strings.CutPrefix(env, "LC_TERMINAL="); ok && termProgram == "" {
			termProgram = v
		}
	}
	profile := getSSHTermInfo(pty.Term, colorTerm, termProgram)
	renderer.SetColorProfile(profile)
	s.Context().SetValue(profileKey, profile)

	m := &airlockspace.Model{
		Width:         pty.Window.Width,
		Height:        pty.Window.Height,
		Style:         renderer.NewStyle(),
		Profile:       profile,
		KittyGraphics: supportsKittyGraphics(pty.Term, termProgram),
		Session:       s,
	}
	return m, []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseAllMotion()}
}

// supportsKittyGraphics reports whether the client terminal implements the
// kitty graphics protocol with unicode placeholders. Env-based heuristic: we
// can't query the terminal through wish, so only well-known implementations
// are listed. (wezterm speaks the protocol but not placeholders.)
func supportsKittyGraphics(term, termProgram string) bool {
	switch strings.ToLower(term) {
	case "xterm-kitty", "xterm-ghostty":
		return true
	}
	switch strings.ToLower(termProgram) {
	case "kitty", "ghostty":
		return true
	}
	return false
}

func GetEnv(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func getSSHTermInfo(term, colorTerm, termProgram string) termenv.Profile {
	term = strings.ToLower(term)
	colorTerm = strings.ToLower(colorTerm)

	switch strings.ToLower(termProgram) {
	case "iterm2", "iterm.app", "kitty", "ghostty", "wezterm":
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
		"foot",
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
