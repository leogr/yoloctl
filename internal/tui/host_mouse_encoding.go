package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Tracking is ours, not Bubble Tea's: its renderer always selects cell SGR
// when View.MouseMode changes. One ordered writer must own the encoding too.
// Disable tracking while changing units, then use DECRPM in the host input
// stream as the boundary before enabling it again. No units are guessed from
// a command being queued for output.
type hostMouseEncoding struct {
	active, pending, supported, pixels bool
	negotiating, target, failed        bool
	tracking                           int
	epoch                              uint64
}

const mouseTrackingOff = "\x1b[?9l\x1b[?1000l\x1b[?1002l\x1b[?1003l"
const mouseEncodingOff = "\x1b[?1005l\x1b[?1006l\x1b[?1015l\x1b[?1016l"

type mouseInitRequest struct{}

func (mouseInitRequest) String() string {
	return mouseTrackingOff + mouseEncodingOff + "\x1b[?1006h\x1b[?1002h\x1b[?1016$p"
}

type mouseTrackingRequest int

func (r mouseTrackingRequest) String() string {
	if r == 1003 {
		return mouseTrackingOff + "\x1b[?1003h"
	}
	return mouseTrackingOff + "\x1b[?1002h"
}

type mouseEncodingRequest bool

func (r mouseEncodingRequest) String() string {
	if r {
		return mouseTrackingOff + "\x1b[?1016h\x1b[?1016$p"
	}
	return mouseTrackingOff + "\x1b[?1016l\x1b[?1006h\x1b[?1016$p"
}

type mouseEncodingTimeout uint64

func (m *model) initHostMouse() tea.Cmd {
	m.hostMouseEncoding.active, m.hostMouseEncoding.pending = true, true
	return tea.Raw(mouseInitRequest{})
}

func (m *model) wantedMouseTracking() int {
	if m.browserDialog != nil {
		return 1002
	}
	if m.focusPalette {
		if m.console.Busy() && m.console.MouseAnyMotion() {
			return 1003
		}
	} else if p := m.tabs.Focused(); p != nil && !p.Exited && !p.Empty() && p.Session.MouseAnyMotion() {
		return 1003
	}
	return 1002
}

func (m *model) wantedMousePixels() bool {
	if !m.hostMouseEncoding.supported || m.hostGeometry.width == 0 || m.hostGeometry.height == 0 {
		return false
	}
	// Retain units until every captured gesture is complete. A background
	// owner still needs its release even after the new surface wants cells.
	if len(m.mouseOwners) > 0 {
		return m.hostMouseEncoding.pixels
	}
	if m.browserDialog != nil {
		return false
	}
	var surface mouseSurface
	if m.focusPalette {
		if m.console.Busy() {
			surface = m.console
		}
	} else if p := m.tabs.Focused(); p != nil && !p.Exited && !p.Empty() {
		surface = p.Session
	}
	if surface == nil || surface.Frame().Offset != 0 {
		return false
	}
	mode := surface.MouseMode()
	return mode.Tracking != 0 && mode.Format == 4
}

func (m *model) syncHostMouseEncoding() tea.Cmd {
	h := &m.hostMouseEncoding
	if !h.active || h.pending || h.negotiating || h.failed {
		return nil
	}
	if want := m.wantedMousePixels(); want != h.pixels {
		h.negotiating, h.target = true, want
		h.tracking = 0
		h.epoch++
		epoch := h.epoch
		return tea.Sequence(tea.Raw(mouseEncodingRequest(want)), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return mouseEncodingTimeout(epoch) }))
	}
	if want := m.wantedMouseTracking(); want != h.tracking {
		h.pending = true
		return tea.Raw(mouseTrackingRequest(want))
	}
	return nil
}

func (m *model) mouseEncodingReport(value ansi.ModeSetting) {
	h := &m.hostMouseEncoding
	if !h.active || h.failed {
		return
	}
	if !h.negotiating {
		h.supported = value == ansi.ModeSet || value == ansi.ModeReset
		return
	}
	if value == ansi.ModeSet && h.target || value == ansi.ModeReset && !h.target {
		h.pixels, h.negotiating = h.target, false
	}
	// A missing/mismatched acknowledgment follows the bounded failure path;
	// it must never turn a pixel report into a cell coordinate or a key.
}
