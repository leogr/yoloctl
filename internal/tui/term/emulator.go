// Package term connects PTY children to the embedded libghostty-vt terminal.
// Every core access is serialized by the emulator mutex. Input responses enter
// the same bounded queue as user input and never wait for the child to read.
package term

import (
	"errors"
	"image/color"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

const DefaultScrollbackLines = 10000

type emulator struct {
	mu                              sync.Mutex
	vt                              *terminal.Terminal
	err                             error
	input                           *inputWriter
	colors                          [2]color.Color
	palette                         [256]color.Color
	queries                         *terminal.ColorQueries
	kbTail                          []byte
	osc52                           termio.OSC52Scanner
	onOSC52                         func(termio.OSC52)
	onBell                          func(int)
	trimFrame                       bool
	historyLimit                    int
	heldFrame                       *terminal.Frame
	holdTimer                       *time.Timer
	holdEpoch                       uint64
	onFrame                         func()
	closed                          bool
	replica                         bool
	replicaEnded                    bool // private stream drained; geometry is local again
	localRows                       int
	localCols                       int
	localCellWidth, localCellHeight int
	sendColors                      func(terminal.Colors)
	viewError                       error
	focusKnown                      bool
	focused                         bool
	focusEnabled                    bool
}

func newEmulator(rows, cols int, responses io.Writer, maxScrollback int) *emulator {
	if maxScrollback <= 0 {
		maxScrollback = DefaultScrollbackLines
	}
	e := &emulator{input: &inputWriter{w: responses}, historyLimit: maxScrollback,
		localRows: max(rows, 1), localCols: max(cols, 1)}
	e.vt, e.err = terminal.New(max(rows, 1), max(cols, 1), maxScrollback, func(p []byte) {
		e.reply(p)
	})
	e.queries = &terminal.ColorQueries{Terminal: e.vt, Send: func(p []byte) { _, _ = e.input.Write(p) }}
	if e.err == nil {
		e.vt.SetHoldHandler(e.renderHoldLocked)
	}
	return e
}

func (e *emulator) Write(p []byte) (int, error) { return e.writeOutput(p, false) }

func (e *emulator) writeOutput(p []byte, local bool) (int, error) {
	e.mu.Lock()
	if local && e.replica {
		e.mu.Unlock()
		return len(p), nil
	}
	if e.err != nil {
		err := e.err
		e.mu.Unlock()
		return 0, err
	}
	filtered := p
	var events []termio.OSC52
	if e.onOSC52 != nil {
		filtered, events = e.osc52.Feed(p)
	}
	if debugEnabled() {
		probe := append(append([]byte(nil), e.kbTail...), filtered...)
		var seqs []string
		seqs, e.kbTail = keyboardProtocolSeqs(probe)
		for _, seq := range seqs {
			debugLogf("child-keyboard-protocol seq=%s", seq)
		}
	}
	if !e.replica {
		e.queries.Observe(filtered)
	}
	before := e.vt.Bells()
	_, e.err = e.vt.Write(filtered)
	bells := int(e.vt.Bells() - before)
	handler, err := e.onOSC52, e.err
	bellHandler := e.onBell
	e.mu.Unlock()
	for _, event := range events {
		handler(event)
	}
	if bells > 0 && bellHandler != nil {
		bellHandler(bells)
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (e *emulator) SetOSC52Handler(handler func(termio.OSC52)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onOSC52 = handler
}

// SetBellHandler delivers native bell events once, outside the emulator lock.
// Rendering, scrolling and repeated frame reads cannot retrigger them.
func (e *emulator) SetBellHandler(handler func(int)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onBell = handler
}
func (e *emulator) FlushOutputFilters() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return
	}
	if e.replica {
		return
	}
	trailing := e.osc52.Flush()
	e.queries.Observe(trailing)
	_, e.err = e.vt.Write(trailing)
}
func (e *emulator) Resize(rows, cols int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.resizeGeometryLocked(terminal.Geometry{Rows: max(rows, 1), Cols: max(cols, 1),
		CellWidth: e.localCellWidth, CellHeight: e.localCellHeight})
}

func (e *emulator) LocalGeometry() terminal.Geometry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return terminal.Geometry{Rows: e.localRows, Cols: e.localCols, CellWidth: e.localCellWidth, CellHeight: e.localCellHeight}
}

func (e *emulator) ResizeGeometry(g terminal.Geometry) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resizeGeometryLocked(g)
}

func (e *emulator) resizeGeometryLocked(g terminal.Geometry) error {
	if !g.Valid() {
		return errors.New("invalid view geometry")
	}
	e.localRows, e.localCols = g.Rows, g.Cols
	e.localCellWidth, e.localCellHeight = g.CellWidth, g.CellHeight
	if e.err == nil && (!e.replica || e.replicaEnded) {
		e.err = e.vt.ResizeGeometry(g)
	}
	return e.err
}

func (e *emulator) linesLocked(history, plain bool, start, count int) []string {
	if e.err != nil {
		return []string{"terminal error: " + e.err.Error()}
	}
	lines, err := e.vt.Lines(history, plain, start, count)
	if err != nil {
		e.err = err
		return []string{"terminal error: " + err.Error()}
	}
	return lines
}
func (e *emulator) GridText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return "terminal error: " + e.err.Error() + "\n"
	}
	return strings.Join(e.linesLocked(false, true, 0, e.vt.Get(2)), "\n") + "\n"
}
func (e *emulator) RenderLine(r int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return "terminal error: " + e.err.Error()
	}
	if r < 0 || r >= e.vt.Get(2) {
		return ""
	}
	return e.linesLocked(false, false, r, 1)[0]
}

// RenderLines captures a complete styled grid in one locked core operation.
func (e *emulator) RenderLines() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return []string{"terminal error: " + e.err.Error()}
	}
	return e.linesLocked(false, false, 0, e.vt.Get(2))
}
func (e *emulator) CursorPos() (int, int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return 0, 0, false
	}
	return e.vt.Get(3), e.vt.Get(4), e.vt.Get(7) != 0
}
func (e *emulator) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return
	}
	e.vt.ScrollToBottom()
	e.vt.Reset()
	e.focusKnown, e.focusEnabled = false, false
	e.queries = &terminal.ColorQueries{Terminal: e.vt, Known: e.colorMetadataLocked(), Send: func(p []byte) { _, _ = e.input.Write(p) }}
	e.osc52 = termio.OSC52Scanner{}
	e.kbTail = nil
}
func (e *emulator) Scrollback() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return nil
	}
	return e.linesLocked(true, true, 0, e.vt.Get(9))
}
func (e *emulator) lastContentRow() (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return 0, false
	}
	lines := e.linesLocked(false, true, 0, e.vt.Get(2))
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimRight(lines[i], " \x00") != "" {
			return i, true
		}
	}
	return 0, false
}
func (e *emulator) IsAlt() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err == nil && e.vt.Get(6) == 1
}
func (e *emulator) Title() string {
	if e == nil { // a Session that never started has no screen and no title
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return ""
	}
	return e.vt.Title()
}
func (e *emulator) Rows() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return 1
	}
	return e.vt.Get(2)
}
func (e *emulator) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.releaseHoldLocked()
	if e.vt == nil {
		return e.err
	}
	return e.vt.Close()
}
func (e *emulator) scrollRegion() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.vt.Get(12), e.vt.Get(13)
}
