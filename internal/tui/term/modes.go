package term

import "github.com/leogr/yoloctl/internal/terminal"

// Query the core's parsed state, never a regex over arbitrary PTY bytes. RIS,
// grouped modes, OSC payloads and fragmentation therefore share one authority.
func (e *emulator) mode(mode uint16) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err == nil && e.vt.Mode(mode)
}
func (e *emulator) ApplicationCursorKeys() bool { return e.mode(1) }
func (e *emulator) MouseReporting() bool        { return e.MouseMode().Tracking != 0 }
func (e *emulator) MouseSGR() bool              { return e.MouseMode().Format == 2 }
func (e *emulator) MouseAnyMotion() bool        { return e.MouseMode().Tracking == 4 }
func (e *emulator) MouseDragMotion() bool       { return e.MouseMode().Tracking == 3 }
func (e *emulator) BracketedPaste() bool        { return e.mode(2004) }
func (e *emulator) AlternateScroll() bool       { return e.mode(1007) }
func (e *emulator) FocusReporting() bool        { return e.mode(1004) }

// UpdateFocus reports the current host/tab/overlay focus only after the child
// opts into mode 1004. Enabling the mode receives its initial state once;
// duplicate host events and ordinary redraws do not produce repeated input.
func (e *emulator) UpdateFocus(focused bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil || e.closed {
		return nil
	}
	enabled := e.vt.Mode(1004)
	changed := !e.focusKnown || e.focused != focused || (enabled && !e.focusEnabled)
	e.focusKnown, e.focused, e.focusEnabled = true, focused, enabled
	if !enabled || !changed {
		return nil
	}
	sequence := "\x1b[O"
	if focused {
		sequence = "\x1b[I"
	}
	_, err := e.input.Write([]byte(sequence))
	return err
}
func (e *emulator) KeyboardFlags() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return 0
	}
	return e.vt.Get(8)
}

func (e *emulator) EncodeKey(k terminal.Key) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	return e.vt.EncodeKey(k)
}
