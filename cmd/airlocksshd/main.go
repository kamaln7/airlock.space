package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/log/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/activeterm"
	"charm.land/wish/v2/bubbletea"
	"charm.land/wish/v2/logging"
	"github.com/charmbracelet/colorprofile"
	airlockspace "github.com/kamaln7/airlock.space"
)

var (
	host = GetEnv("SSH_HOST", "localhost")
	port = GetEnv("SSH_PORT", "23234")
)

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(GetEnv("SSH_HOST_KEY", ".airlocksshd/id_ed25519")),
		// ponytail: flat 1h idle cutoff; per-session activity tracking if long-lived dashboards matter
		wish.WithIdleTimeout(time.Hour),
		// charm.land/ssh only reports a PTY for a session it emulates or
		// allocates one for; without this Pty() says no and activeterm turns
		// every client away. Emulated is what v1 did implicitly.
		ssh.EmulatePty(),
		wish.WithMiddleware(
			bubbletea.MiddlewareWithProgramHandler(program),
			// runs after the tea program exits, outside the alt screen
			func(next ssh.Handler) ssh.Handler {
				return func(s ssh.Session) {
					next(s)
					// the goodbye is written raw, past the program: downsample
					// its colors to what this client can actually show
					w := colorprofile.Writer{Forward: s, Profile: sessionProfile(s)}
					w.WriteString(airlockspace.Goodbye())
				}
			},
			// middleware runs in reverse order, so activeterm goes last here to
			// run first: PTY-less bots are dropped before logging sees them.
			logging.Middleware(),
			activeterm.Middleware(), // Bubble Tea apps usually require a PTY.
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

// oneWriter serializes everything sent to the client. bubbletea's renderer and
// our own kitty image transmissions run on different goroutines, and until
// both went through here they were two hoses into one connection: a frame
// landing inside an image payload leaves the terminal in pieces.
type oneWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *oneWriter) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.w.Write(p)
}

// program builds the tea.Program itself, which the default middleware does not
// allow: it appends its own WithOutput last, and we need ours to win.
func program(s ssh.Session) *tea.Program {
	m, opts := teaHandler(s)
	if m == nil {
		return nil
	}
	return tea.NewProgram(m, opts...)
}

// teaHandler builds the model and the program options for one session.
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	// never fails: activeterm has already turned away sessions with no PTY
	pty, _, _ := s.Pty()

	// one writer for the renderer and for our image writes, in that order of
	// discovery: wish's MakeOptions picks the right input, our WithOutput then
	// replaces the output it chose.
	out := &oneWriter{w: s}
	m := &airlockspace.Model{
		Width:         pty.Window.Width,
		Height:        pty.Window.Height,
		KittyGraphics: supportsKittyGraphics(pty.Term, termProgram(s.Environ())),
		Session:       out,
		WidthPixels:   pty.Window.WidthPixels,
		HeightPixels:  pty.Window.HeightPixels,
	}
	// alt screen and mouse tracking are View fields in v2. The profile goes
	// last: MakeOptions already guesses one, and ours knows more.
	opts := append(bubbletea.MakeOptions(s),
		tea.WithOutput(out),
		tea.WithColorProfile(sessionProfile(s)),
	)
	return m, opts
}

// termProgram is how iTerm2 and friends announce themselves. Over ssh it only
// arrives if the client forwards it, hence LC_TERMINAL as the fallback: it
// rides along in the LC_* vars ssh sends by default.
func termProgram(env []string) string {
	var prog string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "TERM_PROGRAM="); ok {
			prog = v
		}
		if v, ok := strings.CutPrefix(e, "LC_TERMINAL="); ok && prog == "" {
			prog = v
		}
	}
	return prog
}

// sessionProfile is the client's color profile. colorprofile.Env reads TERM
// and COLORTERM, which covers most clients; it knows nothing about
// TERM_PROGRAM, so the terminals that only announce themselves that way are
// upgraded here.
func sessionProfile(s ssh.Session) colorprofile.Profile {
	env := s.Environ()
	if pty, _, ok := s.Pty(); ok {
		env = append(env, "TERM="+pty.Term)
	}
	p := colorprofile.Env(env)
	switch strings.ToLower(termProgram(env)) {
	case "iterm2", "iterm.app", "kitty", "ghostty", "wezterm":
		return max(p, colorprofile.TrueColor)
	}
	return p
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
