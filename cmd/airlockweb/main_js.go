//go:build js && wasm

package main

import (
	"io"
	"os"
	"syscall/js"
	"time"

	tea "charm.land/bubbletea/v2"
	booba "github.com/NimbleMarkets/go-booba"
	"github.com/charmbracelet/colorprofile"
	airlockspace "github.com/kamaln7/airlock.space"
)

// terminalWriter sends frames and raw Kitty image uploads through the same
// browser terminal. Booba's buffered output writer is not exposed to callers.
// JavaScript calls are synchronous, so each write lands as one complete chunk.
type terminalWriter struct{ write js.Value }

func (w terminalWriter) Write(p []byte) (int, error) {
	w.write.Invoke(string(p))
	return len(p), nil
}

func main() {
	terminal := js.Global().Get("airlockTerminal")
	out := terminalWriter{write: js.Global().Get("airlockWrite")}
	cols, rows := airlockspace.ClampSize(terminal.Get("cols").Int(), terminal.Get("rows").Int())
	m := &airlockspace.Model{
		Width: cols, Height: rows,
		KittyGraphics: true, Session: out,
		// the page has been waiting since it loaded, its loader up after the
		// same delay: the program's first spinner carries that wait on
		WaitingSince: time.UnixMilli(int64(js.Global().Get("performance").Get("timeOrigin").Float())),
	}
	if _, err := booba.NewProgram(m,
		tea.WithOutput(out),
		tea.WithColorProfile(colorprofile.TrueColor),
		tea.WithWindowSize(cols, rows),
	).Run(); err != nil {
		panic(err)
	}
	io.WriteString(out, airlockspace.Goodbye(m.Width, "browser", m.APOD()))
	os.Exit(0)
}
