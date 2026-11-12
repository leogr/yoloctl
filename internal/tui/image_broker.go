package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

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

// ReadChatArtifact reads one previewed artifact for a chat pane of space
// ("" means the default space). cmd/yoloctl sets it; nil means previews are
// unavailable. It must accept only the frame's exact size and hash.
var ReadChatArtifact func(ctx context.Context, space string, f chatimage.Frame) ([]byte, error)

type imageMsg struct {
	session *term.Session
	frame   chatimage.Frame
	space   string // the pane's space ("" = default), fixed at its launch
}

// imageJob is the one fetch/decode in flight for the whole TUI.
type imageJob struct {
	msg        imageMsg
	generation uint64
	cellW      int
	cellH      int
	cancel     context.CancelFunc
	// set by the worker when done
	prepared chatimage.Prepared
	err      error
	done     bool
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
	// job is the single fetch/decode slot; deletes are image IDs to free.
	job     *imageJob
	deletes []uint32
	cache   previewCache
}

// imageHandler is OnImage for native chat panes: it only queues the frame
// and wakes the event loop; it never blocks.
func (m *model) imageHandler() func(*term.Session, chatimage.Frame) {
	return m.imageHandlerFor("")
}

func (m *model) imageHandlerFor(space string) func(*term.Session, chatimage.Frame) {
	return func(s *term.Session, f chatimage.Frame) {
		b := &m.images
		b.mu.Lock()
		if len(b.inbox) < imageInboxCap {
			b.inbox = append(b.inbox, imageMsg{s, f, space})
		}
		b.mu.Unlock()
		m.wake()
	}
}

// sendImage queues a host frame for a chat pane without blocking the event
// loop. The single sender keeps per-pane order.
func (m *model) sendImage(s *term.Session, f chatimage.Frame) (queued bool) {
	b := &m.images
	if b.outbox == nil {
		b.outbox = make(chan imageMsg, imageOutboxCap)
		go func(out <-chan imageMsg) {
			for msg := range out {
				if err := msg.session.SendImage(msg.frame); err != nil {
					// A failed or timed-out write may have left half a frame:
					// the channel is unusable, and later frames to this pane
					// must not wait in front of the others.
					msg.session.CloseImageChannel()
				}
			}
		}(b.outbox)
	}
	select {
	case b.outbox <- imageMsg{session: s, frame: f}:
		return true
	default: // full: the pane keeps its chip
		return false
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
	var shown []tea.Cmd
	if known := m.imageHost.state == imageSupported || m.imageHost.state == imageUnsupported; known {
		supported := m.imagesSupported()
		if !b.advertised.known || b.advertised.supported != supported {
			b.advertised.known, b.advertised.supported = true, supported
			b.advertised.generation++
			b.cache = previewCache{} // a host change clears the cache
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
			// Only a queued frame counts: a dropped one is sent again on a
			// later update.
			if m.sendImage(p.Session, m.capabilityFrame()) {
				b.told[p.Session] = b.advertised.generation
			}
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
			if p, ok := m.imageRegistry.ReleaseToken(msg.session, msg.frame.Token); ok {
				b.deletes = append(b.deletes, p.ID)
			}
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
			shown = append(shown, m.answerPreview(msg))
		}
	}
	if b.advertised.known && len(b.pending) > 0 {
		for _, msg := range b.pending {
			shown = append(shown, m.answerPreview(msg))
		}
		b.pending = nil
	}
	shown = append(shown, cmd, m.finishImageJob(live), m.flushImageDeletes())
	return tea.Batch(shown...)
}

// flushImageDeletes frees released images on the host terminal, by ID only.
func (m *model) flushImageDeletes() tea.Cmd {
	b := &m.images
	if len(b.deletes) == 0 {
		return nil
	}
	var seq strings.Builder
	for _, id := range b.deletes {
		seq.WriteString(chatimage.DeleteSequence(id))
	}
	b.deletes = nil
	return tea.Raw(seq.String())
}

// answerPreview replies to one preview request: stale, unsupported or busy
// at once, otherwise it starts the single fetch/decode job.
func (m *model) answerPreview(msg imageMsg) tea.Cmd {
	b := &m.images
	a := b.advertised
	key := previewKey(msg.frame, m.hostGeometry.width, m.hostGeometry.height)
	code := ""
	switch {
	case msg.frame.Generation != a.generation:
		code = "stale"
	case !a.supported || !m.imagesSupported():
		code = "unsupported"
	}
	if code == "" {
		// A preview shown before, at this cell size and under the same
		// identities, is shown again without another read.
		if p, ok := b.cache.get(key); ok {
			return m.showPrepared(msg.session, msg.frame, a.generation, p)
		}
	}
	switch {
	case code != "":
	case ReadChatArtifact == nil:
		code = "unavailable"
	case b.job != nil:
		code = "busy"
	}
	if code != "" {
		m.sendImage(msg.session, chatimage.Frame{Op: chatimage.OpError, Generation: a.generation, Request: msg.frame.Request, Code: code})
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageJobTimeout)
	job := &imageJob{msg: msg, generation: a.generation, cellW: m.hostGeometry.width, cellH: m.hostGeometry.height, cancel: cancel}
	b.job = job
	read := ReadChatArtifact
	go func() {
		defer cancel()
		data, err := read(ctx, msg.space, msg.frame)
		var prepared chatimage.Prepared
		if err == nil {
			prepared, err = chatimage.Prepare(data, job.cellW, job.cellH, maxPreviewCols, maxPreviewRows)
		}
		b.mu.Lock()
		job.prepared, job.err, job.done = prepared, err, true
		b.mu.Unlock()
		m.wake()
	}()
	return nil
}

// imageJobTimeout bounds one fetch and decode on the host.
const imageJobTimeout = 30 * time.Second

// finishImageJob consumes a finished job: a result for a pane that is gone,
// an old generation or a withdrawn capability is discarded; otherwise the
// image is transmitted, registered for that pane and announced as ready.
func (m *model) finishImageJob(live map[*term.Session]bool) tea.Cmd {
	b := &m.images
	b.mu.Lock()
	job := b.job
	done := job != nil && job.done
	b.mu.Unlock()
	if !done {
		return nil
	}
	b.job = nil
	s, f := job.msg.session, job.msg.frame
	reply := func(code string) {
		m.sendImage(s, chatimage.Frame{Op: chatimage.OpError, Generation: b.advertised.generation, Request: f.Request, Code: code})
	}
	switch {
	case !live[s]:
		return nil
	case job.generation != b.advertised.generation || !m.imagesSupported() ||
		job.cellW != m.hostGeometry.width || job.cellH != m.hostGeometry.height:
		reply("stale")
		return nil
	case errors.Is(job.err, chatimage.ErrNotPNG):
		reply("not_png")
		return nil
	case errors.Is(job.err, chatimage.ErrTooLarge):
		reply("too_large")
		return nil
	case job.err != nil:
		reply("failed")
		return nil
	}
	b.cache.put(previewKey(f, job.cellW, job.cellH), job.prepared)
	return m.showPrepared(s, f, job.generation, job.prepared)
}

// showPrepared transmits a prepared preview under a new ID, registers it for
// the pane and announces it as ready after the transmit is written.
func (m *model) showPrepared(s *term.Session, f chatimage.Frame, generation uint64, prepared chatimage.Prepared) tea.Cmd {
	b := &m.images
	id, ok := m.nextImageID()
	if !ok {
		m.sendImage(s, chatimage.Frame{Op: chatimage.OpError, Generation: generation, Request: f.Request, Code: "failed"})
		return nil
	}
	token := rand.Text()[:20]
	placement := chatimage.Placement{ID: id, Rows: prepared.Rows, Cols: prepared.Cols, Token: token}
	if old, replaced := m.imageRegistry.Activate(s, placement, f.Chip); replaced {
		b.deletes = append(b.deletes, old.ID)
	}
	ready := imageReadyMsg{session: s, frame: chatimage.Frame{Op: chatimage.OpReady, Generation: generation, Request: f.Request,
		Token: token, ID: id, Cols: placement.Cols, Rows: placement.Rows}}
	// The pane learns "ready" only after the transmit is written, so it
	// never draws placeholder cells for an image the terminal lacks.
	return tea.Sequence(tea.Raw(chatimage.TransmitSequence(id, prepared)), func() tea.Msg { return ready })
}

// imageReadyMsg follows a preview's transmit write; the ready frame goes out
// only if the pane still holds that preview.
type imageReadyMsg struct {
	session *term.Session
	frame   chatimage.Frame
}

func (m *model) imageReady(msg imageReadyMsg) {
	if p, ok := m.imageRegistry.Active(msg.session); ok && p.Token == msg.frame.Token {
		m.sendImage(msg.session, msg.frame)
	}
}

// endImagePane handles a pane that ended: its in-flight read stops promptly
// and frees the single slot, its result is dropped, and an ended chat
// connection clears the preview cache.
func (m *model) endImagePane(s *term.Session) {
	if job := m.images.job; job != nil && job.msg.session == s {
		job.cancel()
	}
	if s.HasImageChannel() {
		m.images.cache = previewCache{}
	}
}

// deleteOwnedImages frees every image this TUI still owns on the host
// terminal when it exits, by ID only.
func (m *model) deleteOwnedImages(w io.Writer) {
	var seq strings.Builder
	for _, p := range m.imageRegistry.All() {
		seq.WriteString(chatimage.DeleteSequence(p.ID))
	}
	if seq.Len() > 0 {
		_, _ = io.WriteString(w, seq.String())
	}
}

// chatImageHandler gives only native chat panes the image channel.
func (m *model) chatImageHandler(args []string) func(*term.Session, chatimage.Frame) {
	if !isChatView(args) || !m.cfg.ImagesEnabled() {
		return nil // seams.images off: no channel, so no probe, download or graphics
	}
	return m.imageHandlerFor(chatPaneSpace(args, m.ctxSpace))
}

// chatPaneSpace is the space a chat pane was launched for: its --space flag,
// else the TUI's pinned space, else "" (the default).
func chatPaneSpace(args []string, pinned string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--space" && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(a, "--space="):
			return strings.TrimPrefix(a, "--space=")
		}
	}
	return pinned
}

// Cache bounds (plan §3C): prepared previews kept in memory per host.
const (
	previewCacheEntries = 8
	previewCacheBytes   = 16 << 20
)

// previewKey identifies a prepared preview: the artifact, the identities it
// was read under and the cell size it was scaled for.
func previewKey(f chatimage.Frame, cellW, cellH int) string {
	return strings.Join([]string{f.Artifact, f.SHA256, f.RoomID, f.DiskID, f.HumanID, f.ChannelID, strconv.Itoa(cellW), strconv.Itoa(cellH)}, "|")
}

// previewCache is an in-memory LRU of host-made preview PNGs, bounded by
// entries and bytes. It lives only as long as the TUI.
type previewCache struct {
	keys  []string // least recently used first
	items map[string]chatimage.Prepared
	bytes int
}

func (c *previewCache) get(key string) (chatimage.Prepared, bool) {
	p, ok := c.items[key]
	if ok {
		c.touch(key)
	}
	return p, ok
}

func (c *previewCache) put(key string, p chatimage.Prepared) {
	if len(p.PNG) > previewCacheBytes {
		return
	}
	if c.items == nil {
		c.items = map[string]chatimage.Prepared{}
	}
	if old, ok := c.items[key]; ok {
		c.bytes -= len(old.PNG)
	}
	c.items[key] = p
	c.bytes += len(p.PNG)
	c.touch(key)
	for len(c.keys) > previewCacheEntries || c.bytes > previewCacheBytes {
		oldest := c.keys[0]
		c.keys = c.keys[1:]
		c.bytes -= len(c.items[oldest].PNG)
		delete(c.items, oldest)
	}
}

func (c *previewCache) touch(key string) {
	for i, k := range c.keys {
		if k == key {
			c.keys = append(c.keys[:i], c.keys[i+1:]...)
			break
		}
	}
	c.keys = append(c.keys, key)
}
