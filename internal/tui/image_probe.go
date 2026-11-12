package tui

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/chatimage"
)

// Image capability of the host terminal, owned by the outer TUI (the tty
// owner). It is probed at most once, lazily, and never retried.
const (
	imageUnknown = iota
	imageProbing
	imageSupported
	imageUnsupported
)

type imageHost struct {
	state      int
	generation uint64
	probe      *chatimage.Probe
	terminal   chatimage.Terminal
	ids        *chatimage.IDs
}

type imageProbeTick uint64

// nextImageID allocates a 24-bit ID for a query or an image from this host
// process's randomly seeded sequence.
func (m *model) nextImageID() (uint32, bool) {
	h := &m.imageHost
	if h.ids == nil {
		var seed [4]byte
		_, _ = rand.Read(seed[:])
		h.ids = chatimage.NewIDs(binary.BigEndian.Uint32(seed[:]))
	}
	return h.ids.Next()
}

// startImageProbe asks the host terminal for its version and for a
// non-storing graphics query, once. It returns nil when a probe already ran
// or is running.
func (m *model) startImageProbe() tea.Cmd {
	h := &m.imageHost
	if h.state != imageUnknown {
		return nil
	}
	id, ok := m.nextImageID()
	if !ok {
		h.state = imageUnsupported
		return nil
	}
	h.generation++
	h.state = imageProbing
	h.probe = chatimage.NewProbe(h.generation, id, time.Now())
	gen := h.generation
	return tea.Batch(
		tea.RequestTerminalVersion,
		tea.Raw(h.probe.GraphicsQuery()),
		tea.Tick(chatimage.ProbeBudget, func(time.Time) tea.Msg { return imageProbeTick(gen) }),
	)
}

// imageProbeMsg feeds probe replies and its deadline. It reports whether msg
// belonged to the probe.
func (m *model) imageProbeMsg(msg tea.Msg) bool {
	h := &m.imageHost
	switch msg := msg.(type) {
	case tea.TerminalVersionMsg:
		if h.probe != nil {
			h.probe.Version(msg.Name)
		}
	case uv.KittyGraphicsEvent:
		if h.probe != nil {
			h.probe.Graphics(msg.Options.ID, string(msg.Payload))
		}
	case imageProbeTick:
		if uint64(msg) != h.generation || h.probe == nil {
			return true
		}
		h.probe.Expire() // the tick is the budget
	default:
		return false
	}
	m.checkImageProbe(time.Now())
	return true
}

func (m *model) checkImageProbe(now time.Time) {
	h := &m.imageHost
	if h.probe == nil {
		return
	}
	measured := m.hostGeometry.width > 0 && m.hostGeometry.height > 0
	ok, done := h.probe.Result(now, measured)
	if !done {
		return
	}
	if ok {
		h.state, h.terminal = imageSupported, h.probe.Terminal()
	} else {
		h.state = imageUnsupported
	}
	h.probe = nil
}

// imagesSupported reports whether previews may be shown now: the probe
// succeeded and cell pixels are still measured.
func (m *model) imagesSupported() bool {
	return m.imageHost.state == imageSupported && m.hostGeometry.width > 0 && m.hostGeometry.height > 0
}
