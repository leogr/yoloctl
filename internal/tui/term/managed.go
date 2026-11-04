package term

import (
	"context"
	"errors"
	"image/color"
	"io"
	"os"
	"sync/atomic"

	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

type managedView struct {
	conn   *os.File
	active atomic.Bool
	done   chan struct{}
	cancel context.CancelFunc
	err    error // written before done closes
	sizes  chan terminal.Geometry
	colors chan terminal.Colors
	host   *session.HostClient
}

func (s *Session) startView(conn *os.File) {
	ctx, cancel := context.WithCancel(context.Background())
	v := &managedView{conn: conn, done: make(chan struct{}), cancel: cancel, sizes: make(chan terminal.Geometry, 1), colors: make(chan terminal.Colors, 1)}
	v.host = &session.HostClient{}
	if s.onBrowser != nil {
		v.host.OnBrowser = func(r session.BrowserRequest, done <-chan struct{}) { s.onBrowser(s, r, done) }
	}
	v.sizes <- s.LocalGeometry()
	s.managed = v
	go func() {
		defer close(v.done)
		err := session.Attach(ctx, conn, session.AttachIO{
			Resize: v.sizes, Colors: v.colors,
			Host: v.host,
			OnEvent: func(event session.ViewEvent) error {
				if err := s.applyView(ctx, event); err != nil {
					return err
				}
				if v.active.CompareAndSwap(false, true) {
					// Keep the original PTY input route: switching to the private
					// stream would strand or reorder typeahead from setup.
					s.mu.Lock()
					s.sendColors = func(value terminal.Colors) {
						select {
						case <-v.colors:
						default:
						}
						select {
						case v.colors <- value:
						default:
						}
					}
					s.publishColorsLocked()
					s.mu.Unlock()
				}
				if s.onFrame != nil {
					s.onFrame()
				}
				return nil
			},
		})
		var exit *session.ExitError
		if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) && !errors.As(err, &exit) {
			v.err = err
			// A failed private stream must not leave a CLI worker waiting forever.
			// Killing this local client only detaches; the persistent holder survives.
			if s.cmd.Process != nil {
				_ = s.cmd.Process.Kill()
			}
			s.mu.Lock()
			s.viewError = err
			s.mu.Unlock()
			if s.onFrame != nil {
				s.onFrame()
			}
		}
	}()
}

// finishView runs only after the private stream and PTY pumps have joined.
// While attached, every replica uses canonical geometry. Once complete, its
// final screen and local exit banner must fit this viewer's own viewport,
// including when a larger peer sent the last shared resize. Keep replica
// query suppression and output provenance even after local resize is allowed.
func (e *emulator) finishView() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.replica {
		return
	}
	e.replicaEnded = true
	e.sendColors = nil
	if e.err == nil {
		e.err = e.vt.ResizeGeometry(terminal.Geometry{Rows: e.localRows, Cols: e.localCols,
			CellWidth: e.localCellWidth, CellHeight: e.localCellHeight})
	}
}

func (e *emulator) applyView(ctx context.Context, event session.ViewEvent) error {
	if len(event.Snapshot) > 0 {
		vt, err := terminal.RestoreSnapshot(ctx, event.Snapshot, func([]byte) {})
		if err != nil {
			return err
		}
		if err := vt.SetHistoryViewLimit(e.historyLimit); err != nil {
			_ = vt.Close()
			return err
		}
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			_ = vt.Close()
			return io.ErrClosedPipe
		}
		e.releaseHoldLocked()
		old := e.vt
		e.vt = vt
		e.replica = true
		e.replicaEnded = false
		e.err = nil
		e.queries = nil
		e.osc52 = termio.OSC52Scanner{}
		e.kbTail = nil
		if vt.Holding() {
			e.renderHoldLocked(true)
		}
		e.focusKnown, e.focusEnabled = false, false
		vt.SetHoldHandler(e.renderHoldLocked)
		_ = old.Close()
		e.mu.Unlock()
	}
	if event.Colors != nil {
		e.mu.Lock()
		for i, c := range event.Colors.Defaults {
			if c != nil {
				e.vt.SetColor(i, c)
			}
		}
		for i, c := range event.Colors.Palette {
			if c != nil {
				_ = e.vt.SetPaletteColor(i, c)
			}
		}
		err := e.vt.Err()
		e.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if event.Size.Rows > 0 && event.Size.Cols > 0 {
		e.mu.Lock()
		e.releaseHoldLocked()
		err := e.vt.ResizeGeometry(event.Size)
		e.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if event.EndHold {
		e.EndRenderHold()
	}
	if len(event.Data) > 0 {
		_, err := e.Write(event.Data)
		return err
	}
	if event.Clipboard != nil {
		e.mu.Lock()
		handler := e.onOSC52
		e.mu.Unlock()
		if handler != nil {
			handler(*event.Clipboard)
		}
	}
	return nil
}

func (e *emulator) publishColorsLocked() {
	if e.sendColors == nil {
		return
	}
	e.sendColors(e.colorMetadataLocked())
}

func (e *emulator) colorMetadataLocked() terminal.Colors {
	var c terminal.Colors
	convert := func(c color.Color) *color.RGBA {
		if c == nil {
			return nil
		}
		r, g, b, _ := c.RGBA()
		return &color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
	}
	for i, v := range e.colors {
		c.Defaults[i] = convert(v)
	}
	for i, v := range e.palette {
		c.Palette[i] = convert(v)
	}
	return c
}
