package term

import (
	"errors"

	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

// SelectMouse maps displayed cells while holding the same lock as output,
// reflow and frame capture. A synchronized-output snapshot must not select
// different, still-hidden text from the live core.
func (e *emulator) SelectMouse(action terminal.MouseAction, x, y int, rectangle bool, now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.heldFrame != nil {
		return nil
	}
	if e.err != nil {
		return e.err
	}
	return e.vt.Select(action, x, y, rectangle, now, e.trimFrame)
}

func (e *emulator) ClearSelection() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed && e.err == nil {
		e.vt.ClearSelection()
	}
}

func (e *emulator) SelectedText() (string, error) {
	return e.selected(false)
}

// SelectedStyledText is SelectedText with each cell's SGR style, for copy
// mappings that need cell colours (native chat image previews).
func (e *emulator) SelectedStyledText() (string, error) {
	return e.selected(true)
}

func (e *emulator) selected(styled bool) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return "", nil
	}
	if e.heldFrame != nil {
		for _, span := range e.heldFrame.Selection {
			if span.End > span.Start {
				return "", errors.New("terminal display update in progress; copy again when it finishes")
			}
		}
		return "", nil
	}
	if e.err != nil {
		return "", e.err
	}
	if styled {
		// Styles add bytes; the mapped clipboard text is bounded again by
		// the caller.
		return e.vt.SelectedStyledText(1 << 20)
	}
	return e.vt.SelectedText(termio.MaxOSC52DecodedBytes)
}
