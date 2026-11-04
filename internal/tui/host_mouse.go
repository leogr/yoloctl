package tui

import (
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/terminal"
)

// XTSHIFTESCAPE is independent of tracking/encoding and goes through the
// trusted Raw command path. Keep only one
// request in flight: Cmds run concurrently, so issuing every focus change as a
// separate command could apply an old tab's preference after the new one.
type hostMouseShift struct {
	pending, known, capture, touched bool
}

type mouseShiftRequest bool

func (m mouseShiftRequest) String() string {
	if m {
		return "\x1b[>1s"
	}
	return "\x1b[>0s"
}

func (m *model) wantedMouseShift() (capture, explicit bool) {
	for _, owner := range m.mouseOwners {
		if owner.captureShift {
			return true, true
		}
	}
	if m.browserDialog != nil {
		return false, false
	}
	var surface mouseSurface
	if m.focusPalette {
		if m.console != nil && m.console.Busy() {
			surface = m.console
		}
	} else if p := m.tabs.Focused(); p != nil && !p.Exited && !p.Empty() {
		surface = p.Session
	}
	if surface == nil {
		return false, false
	}
	mode := surface.MouseMode()
	if mode.Tracking == 0 || mode.ShiftCapture == terminal.MouseShiftDefault {
		return false, false
	}
	if surface.Frame().Offset != 0 {
		return false, false
	}
	return mode.ShiftCapture == terminal.MouseShiftReport, mode.ShiftCapture != terminal.MouseShiftDefault
}

func (m *model) syncMouseShift() tea.Cmd {
	h := &m.hostMouseShift
	capture, explicit := m.wantedMouseShift()
	if h.pending || capture == h.capture && (h.known || !explicit) {
		return nil
	}
	h.pending = true
	h.touched = true
	return tea.Raw(mouseShiftRequest(capture))
}

// Call only after Program.Run has joined its renderer. XTSHIFTESCAPE has no
// query/save/restore operation: after touching it, leave selection override
// enabled, just as leaving the palette. Host "always/never" policy still wins.
func (m *model) resetHostMouse(w io.Writer) {
	if m.hostMouseEncoding.active {
		_, _ = io.WriteString(w, mouseTrackingOff+mouseEncodingOff)
	}
	if m.hostMouseShift.touched {
		_, _ = io.WriteString(w, mouseShiftRequest(false).String())
	}
}
