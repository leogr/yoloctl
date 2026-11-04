package term

import "github.com/leogr/yoloctl/internal/terminal"

func (e *emulator) MouseMode() terminal.MouseMode {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil || e.closed {
		return terminal.MouseMode{}
	}
	return e.vt.MouseMode()
}

// Encoding and enqueueing share the terminal lock with mode changes. The
// transport queue preserves ordering with keys, paste and terminal replies.
func (e *emulator) SendMouse(m terminal.Mouse) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil // the press owner was closed; never retarget its release
	}
	if e.err != nil {
		return e.err
	}
	p, err := e.vt.EncodeMouse(m)
	if err != nil || len(p) == 0 {
		return err
	}
	_, err = e.input.Write(p)
	return err
}
