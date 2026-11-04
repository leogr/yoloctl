package term

import (
	"image/color"
	"io"
	"sync"
)

// SetColors supplies measured host defaults. Child overrides stay in the VT.
func (e *emulator) SetColors(foreground, background color.Color) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.colors = [2]color.Color{foreground, background}
	e.applyHostColorsLocked()
}

func (e *emulator) applyHostColorsLocked() {
	if e.replica {
		e.publishColorsLocked()
		return
	}
	if e.err == nil {
		e.err = e.queries.Apply(e.colorMetadataLocked())
	}
}

// inputWriter serializes terminal replies and user input to the same PTY.
// Its target changes between console commands; a nil target is idle.
type inputWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *inputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.w == nil {
		return len(p), nil
	}
	return w.w.Write(p)
}

func (w *inputWriter) setTarget(target io.Writer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.w = target
}
