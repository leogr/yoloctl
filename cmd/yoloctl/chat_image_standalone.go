package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

// Standalone human chat owns its host terminal, so it is its own image host:
// it probes the terminal once, reads, prepares and draws previews itself and
// filters every frame so that only its one preview's placeholder cells
// display, as the outer TUI does for a chat pane. It shares the TUI's probe,
// preparation, ID, cache and filter code (internal/chatimage).

// standaloneImageTimeout bounds one read and decode, as in the TUI.
const standaloneImageTimeout = 30 * time.Second

const (
	standaloneUnknown = iota
	standaloneProbing
	standaloneSupported
	standaloneUnsupported
)

type standaloneImages struct {
	space string
	read  func(context.Context, string, chatimage.Frame) ([]byte, error)
	send  func(tea.Msg) // the program's Send, for finished jobs

	state        int
	probe        *chatimage.Probe
	generation   uint64 // advertised capability; 0 before the first
	ids          *chatimage.IDs
	cellW, cellH int // CSI 16t; zero while unmeasured

	job    *standaloneJob
	cache  chatimage.Cache
	active chatimage.Placement
	shown  bool
}

type standaloneJob struct {
	req          chatui.PreviewRequest
	frame        chatimage.Frame
	generation   uint64
	cellW, cellH int
	cancel       context.CancelFunc
	prepared     chatimage.Prepared
	err          error
}

type (
	standaloneProbeTick  uint64
	standalonePreviewMsg struct {
		req   chatui.PreviewRequest
		frame chatimage.Frame
		err   error
	}
	standaloneReleaseMsg string
	standaloneJobDone    struct{ job *standaloneJob }
)

// ownsTerminalImages reports whether this chat view is its own image host: it
// has no TUI image channel, is not inside a TUI (whose emulated terminal it
// must never probe) and seams.images is on.
func ownsTerminalImages(previews *chatPreviews, yoloctlTUI string, enabled bool) bool {
	return previews.conn == nil && yoloctlTUI != "1" && enabled
}

func newStandaloneImages(space string, read func(context.Context, string, chatimage.Frame) ([]byte, error)) *standaloneImages {
	var seed [4]byte
	_, _ = rand.Read(seed[:])
	return &standaloneImages{space: space, read: read, ids: chatimage.NewIDs(binary.BigEndian.Uint32(seed[:]))}
}

// configure installs the preview callbacks. Identities are captured when the
// person asks, as for the TUI channel.
func (img *standaloneImages) configure(s *chatSession, opts *chatui.Options) {
	opts.PreviewImage = func(req chatui.PreviewRequest) tea.Cmd {
		s.mu.Lock()
		header := s.header
		s.mu.Unlock()
		return func() tea.Msg { return standalonePreviewRequest(header, req) }
	}
	opts.ReleasePreview = func(token string) tea.Cmd {
		return func() tea.Msg { return standaloneReleaseMsg(token) }
	}
}

func standalonePreviewRequest(header chatstream.Header, req chatui.PreviewRequest) standalonePreviewMsg {
	frame, err := chatPreviewFrame(header, req)
	return standalonePreviewMsg{req: req, frame: frame, err: err}
}

// standaloneChat wraps the chat model: image messages stay here, everything
// else reaches chat, and its frames are filtered.
type standaloneChat struct {
	inner tea.Model
	img   *standaloneImages
}

func (img *standaloneImages) wrap(inner tea.Model) tea.Model {
	return &standaloneChat{inner: inner, img: img}
}

func (c *standaloneChat) Init() tea.Cmd { return tea.Batch(c.inner.Init(), c.img.start()) }

func (c *standaloneChat) View() tea.View {
	v := c.inner.View()
	v.Content = c.img.filter(v.Content)
	return v
}

func (c *standaloneChat) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	imgCmd, handled := c.img.update(msg)
	if handled {
		return c, imgCmd
	}
	var cmd tea.Cmd
	c.inner, cmd = c.inner.Update(msg)
	cmd = tea.Batch(imgCmd, cmd)
	if _, focus := msg.(tea.FocusMsg); focus {
		// A font change shows up as a new cell size; focus is when to ask.
		cmd = tea.Batch(cmd, tea.Raw("\x1b[16t"))
	}
	return c, cmd
}

// start asks the host terminal for its version, a non-storing graphics query
// and its cell size, once.
func (img *standaloneImages) start() tea.Cmd {
	id, ok := img.ids.Next()
	if !ok {
		img.state = standaloneUnsupported
		return nil
	}
	img.state = standaloneProbing
	img.probe = chatimage.NewProbe(1, id, time.Now())
	return tea.Batch(
		tea.RequestTerminalVersion,
		tea.Raw(img.probe.GraphicsQuery()+"\x1b[16t"),
		tea.Tick(chatimage.ProbeBudget, func(time.Time) tea.Msg { return standaloneProbeTick(1) }),
	)
}

// update handles the owner's messages and reports whether msg was one of
// them; the terminal version reply also reaches chat.
func (img *standaloneImages) update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.TerminalVersionMsg:
		if img.probe != nil {
			img.probe.Version(msg.Name)
			return img.checkProbe(), false
		}
		return nil, false
	case uv.KittyGraphicsEvent:
		if img.probe != nil {
			img.probe.Graphics(msg.Options.ID, string(msg.Payload))
		}
		return img.checkProbe(), true
	case uv.CellSizeEvent:
		return img.acceptCellSize(msg.Width, msg.Height), true
	case standaloneProbeTick:
		if img.probe != nil {
			img.probe.Expire()
		}
		return img.checkProbe(), true
	case standalonePreviewMsg:
		return img.answer(msg), true
	case standaloneReleaseMsg:
		return img.release(string(msg)), true
	case standaloneJobDone:
		return img.finish(msg.job), true
	}
	return nil, false
}

func (img *standaloneImages) supported() bool {
	return img.state == standaloneSupported && img.cellW > 0 && img.cellH > 0
}

func (img *standaloneImages) checkProbe() tea.Cmd {
	if img.probe == nil {
		return nil
	}
	ok, done := img.probe.Result(time.Now(), img.cellW > 0 && img.cellH > 0)
	if !done {
		return nil
	}
	img.probe = nil
	img.state = standaloneUnsupported
	if ok {
		img.state = standaloneSupported
	}
	return img.advertise()
}

// advertise starts a new capability generation: chat collapses a preview of
// an older one, and the cache is cleared.
func (img *standaloneImages) advertise() tea.Cmd {
	img.generation++
	img.cache.Clear()
	capability := chatui.PreviewCapabilityMsg{Supported: img.supported(), Generation: img.generation,
		MaxCols: chatimage.PreviewCols, MaxRows: chatimage.PreviewRows}
	return func() tea.Msg { return capability }
}

// acceptCellSize records a measured cell size; a change after the probe is a
// new generation, since shown previews were scaled for the old size.
func (img *standaloneImages) acceptCellSize(w, h int) tea.Cmd {
	if w < 1 || h < 1 || w > 1<<12 || h > 1<<12 || (w == img.cellW && h == img.cellH) {
		return nil
	}
	img.cellW, img.cellH = w, h
	if img.probe != nil {
		return img.checkProbe()
	}
	if img.state == standaloneSupported {
		return img.advertise()
	}
	return nil
}

func previewError(req chatui.PreviewRequest, code string) tea.Cmd {
	return func() tea.Msg { return chatui.PreviewErrorMsg{Request: req.Request, Code: code} }
}

// answer replies to one preview request: stale, unsupported or busy at once,
// a cached preview without another read, otherwise the single read/decode
// job of this process starts.
func (img *standaloneImages) answer(msg standalonePreviewMsg) tea.Cmd {
	req := msg.req
	switch {
	case msg.err != nil:
		return previewError(req, "failed")
	case req.Generation != img.generation:
		return previewError(req, "stale")
	case !img.supported():
		return previewError(req, "unsupported")
	}
	if p, ok := img.cache.Get(chatimage.CacheKey(msg.frame, img.cellW, img.cellH)); ok {
		return img.show(req, p)
	}
	if img.read == nil {
		return previewError(req, "unavailable")
	}
	if img.job != nil {
		return previewError(req, "busy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), standaloneImageTimeout)
	job := &standaloneJob{req: req, frame: msg.frame, generation: img.generation, cellW: img.cellW, cellH: img.cellH, cancel: cancel}
	img.job = job
	read, space, send := img.read, img.space, img.send
	go func() {
		defer cancel()
		data, err := read(ctx, space, job.frame)
		if err == nil {
			job.prepared, err = chatimage.Prepare(data, job.cellW, job.cellH, chatimage.PreviewCols, chatimage.PreviewRows)
		}
		job.err = err
		if send != nil {
			send(standaloneJobDone{job: job})
		}
	}()
	return nil
}

// finish consumes the finished job: a result for an older generation, a
// withdrawn capability or another cell size is stale; otherwise it is cached
// and shown.
func (img *standaloneImages) finish(job *standaloneJob) tea.Cmd {
	if img.job != job {
		return nil // cancelled
	}
	img.job = nil
	switch {
	case job.generation != img.generation || !img.supported() || job.cellW != img.cellW || job.cellH != img.cellH:
		return previewError(job.req, "stale")
	case errors.Is(job.err, chatimage.ErrNotPNG):
		return previewError(job.req, "not_png")
	case errors.Is(job.err, chatimage.ErrTooLarge):
		return previewError(job.req, "too_large")
	case job.err != nil:
		return previewError(job.req, "failed")
	}
	img.cache.Put(chatimage.CacheKey(job.frame, job.cellW, job.cellH), job.prepared)
	return img.show(job.req, job.prepared)
}

// show deletes the previous image, transmits p under a new ID and answers
// ready only after the transmit is written.
func (img *standaloneImages) show(req chatui.PreviewRequest, p chatimage.Prepared) tea.Cmd {
	id, ok := img.ids.Next()
	if !ok {
		return previewError(req, "failed")
	}
	var seq strings.Builder
	if img.shown {
		seq.WriteString(chatimage.DeleteSequence(img.active.ID))
	}
	seq.WriteString(chatimage.TransmitSequence(id, p))
	img.active = chatimage.Placement{ID: id, Rows: p.Rows, Cols: p.Cols, Token: rand.Text()}
	img.shown = true
	ready := chatui.PreviewReadyMsg{Request: req.Request, Generation: img.generation, Token: img.active.Token,
		ID: id, Cols: p.Cols, Rows: p.Rows}
	return tea.Sequence(tea.Raw(seq.String()), func() tea.Msg { return ready })
}

// release deletes the shown image if token names it.
func (img *standaloneImages) release(token string) tea.Cmd {
	if !img.shown || token == "" || img.active.Token != token {
		return nil
	}
	img.shown = false
	return tea.Raw(chatimage.DeleteSequence(img.active.ID))
}

// filter neutralizes every placeholder cell that is not one of the shown
// preview's cells.
func (img *standaloneImages) filter(content string) string {
	if !strings.ContainsRune(content, chatimage.Placeholder) {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = chatimage.FilterLine(line, img.active, img.shown)
	}
	return strings.Join(lines, "\n")
}

// close cancels a running job and deletes the shown image once the program
// has exited.
func (img *standaloneImages) close(w io.Writer) {
	if img.job != nil {
		img.job.cancel()
		img.job = nil
	}
	if img.shown {
		img.shown = false
		_, _ = io.WriteString(w, chatimage.DeleteSequence(img.active.ID))
	}
}
