package tui

import (
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// Bounds of the preview broker's queues. Image control frames from chat
// panes wait in imageInbox until the event loop drains them; frames to chat
// panes wait in the outbox for one sender goroutine that writes them in
// order with a per-write deadline. Overflowing frames are dropped: a child
// gets no answer and keeps its chip.
const (
	imageInboxCap  = 16
	imageOutboxCap = 64
	// Preview requests held while the host terminal is probed.
	imagePendingCap = 8
	maxPreviewCols  = 60
	maxPreviewRows  = 12
)

type imageMsg struct {
	session *term.Session
	frame   chatimage.Frame
}

type imageBroker struct {
	mu      sync.Mutex
	inbox   []imageMsg
	pending []imageMsg // preview requests waiting for the probe
	outbox  chan imageMsg
	// advertised is the current capability; told is the generation each
	// chat pane was last sent.
	advertised struct {
		known, supported bool
		generation       uint64
	}
	told map[*term.Session]uint64
}

// imageHandler is OnImage for native chat panes: it only queues the frame
// and wakes the event loop; it never blocks.
func (m *model) imageHandler() func(*term.Session, chatimage.Frame) {
	return func(s *term.Session, f chatimage.Frame) {
		b := &m.images
		b.mu.Lock()
		if len(b.inbox) < imageInboxCap {
			b.inbox = append(b.inbox, imageMsg{s, f})
		}
		b.mu.Unlock()
		m.wake()
	}
}

// sendImage queues a host frame for a chat pane without blocking the event
// loop. The single sender keeps per-pane order.
func (m *model) sendImage(s *term.Session, f chatimage.Frame) {
	b := &m.images
	if b.outbox == nil {
		b.outbox = make(chan imageMsg, imageOutboxCap)
		go func(out <-chan imageMsg) {
			for msg := range out {
				_ = msg.session.SendImage(msg.frame)
			}
		}(b.outbox)
	}
	select {
	case b.outbox <- imageMsg{s, f}:
	default: // full: the pane keeps its chip
	}
}

func (m *model) capabilityFrame() chatimage.Frame {
	a := m.images.advertised
	f := chatimage.Frame{Op: chatimage.OpCapability, Generation: a.generation, Supported: a.supported}
	if a.supported {
		f.MaxCols, f.MaxRows = maxPreviewCols, maxPreviewRows
	}
	return f
}

// syncImages runs after every update: it drains queued frames, advertises a
// changed capability to every chat pane and answers held requests once the
// probe has an outcome.
func (m *model) syncImages() tea.Cmd {
	b := &m.images
	b.mu.Lock()
	inbox := b.inbox
	b.inbox = nil
	b.mu.Unlock()

	var cmd tea.Cmd
	if known := m.imageHost.state == imageSupported || m.imageHost.state == imageUnsupported; known {
		supported := m.imagesSupported()
		if !b.advertised.known || b.advertised.supported != supported {
			b.advertised.known, b.advertised.supported = true, supported
			b.advertised.generation++
		}
	}
	// The first chat pane starts the probe; every live chat pane learns the
	// current capability once per generation.
	live := map[*term.Session]bool{}
	m.eachSession(func(p *panes.Pane) {
		if !p.Session.HasImageChannel() || p.Exited {
			return
		}
		live[p.Session] = true
		if c := m.startImageProbe(); c != nil {
			cmd = c
		}
		if b.advertised.known && b.told[p.Session] != b.advertised.generation {
			if b.told == nil {
				b.told = map[*term.Session]uint64{}
			}
			b.told[p.Session] = b.advertised.generation
			m.sendImage(p.Session, m.capabilityFrame())
		}
	})
	for s := range b.told {
		if !live[s] {
			delete(b.told, s)
		}
	}
	for _, msg := range inbox {
		switch msg.frame.Op {
		case chatimage.OpRelease:
			m.imageRegistry.ReleaseToken(msg.session, msg.frame.Token)
		case chatimage.OpPreview:
			if !b.advertised.known {
				if len(b.pending) < imagePendingCap {
					b.pending = append(b.pending, msg)
				}
				if c := m.startImageProbe(); c != nil {
					cmd = c
				}
				continue
			}
			m.answerPreview(msg)
		}
	}
	if b.advertised.known && len(b.pending) > 0 {
		for _, msg := range b.pending {
			m.answerPreview(msg)
		}
		b.pending = nil
	}
	return cmd
}

// answerPreview replies to one preview request. Fetching and drawing are not
// implemented yet, so a supported terminal answers "unavailable"; the pane
// keeps its chip either way.
func (m *model) answerPreview(msg imageMsg) {
	a := m.images.advertised
	code := "unavailable"
	switch {
	case msg.frame.Generation != a.generation:
		code = "stale"
	case !a.supported:
		code = "unsupported"
	}
	m.sendImage(msg.session, chatimage.Frame{Op: chatimage.OpError, Generation: a.generation, Request: msg.frame.Request, Code: code})
}

// chatImageHandler gives only native chat panes the image channel.
func (m *model) chatImageHandler(args []string) func(*term.Session, chatimage.Frame) {
	if !isChatView(args) {
		return nil
	}
	return m.imageHandler()
}
