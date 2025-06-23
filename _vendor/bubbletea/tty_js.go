//go:build js
// +build js

package tea

import (
	"errors"
	"os"
)

const suspendSupported = false

func suspendProcess() {}

func (p *Program) initInput() error {
	return nil
}

func openInputTTY() (*os.File, error) {
	return nil, errors.New("unavailable in js")
}
