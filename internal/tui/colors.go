package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/panes"
)

// A bounded, read-only startup probe supplies OSC 4 query answers to children.
// Rendering keeps unmodified indices, so it also works when a host never
// answers. Individual queries avoid relying on multi-pair OSC support.
func requestHostPalette() tea.Msg {
	var out strings.Builder
	for i := range 256 {
		_, _ = fmt.Fprintf(&out, "\x1b]4;%d;?\x1b\\", i)
	}
	return tea.RawMsg{Msg: out.String()}
}

// A host answers the probe with one input event per index, and Bubble Tea runs
// Update and View for every event, so input behind the burst waited for 256
// renders. The program filter collects reports instead and admits one batch
// once every index has answered, or at a fixed deadline after the first report
// (hosts may answer partially, out of order or not at all). Further reports
// join without extending it; reports after a flush open the next batch.
var hostPaletteWindow = 100 * time.Millisecond

type hostPaletteBatch struct {
	colors [256]color.Color
	count  int // distinct indices in colors
	epoch  uint64
	open   bool
}

type (
	hostPaletteStart uint64
	hostPaletteFlush uint64
)

// programOptions is shared by both TUI entry points. Bubble Tea invokes the
// filter on its event-loop goroutine, like Update, so the batch needs no lock.
func (m *model) programOptions() []tea.ProgramOption {
	return []tea.ProgramOption{tea.WithoutSignalHandler(), tea.WithFilter(m.filterHostPalette)}
}

// filterHostPalette consumes only valid palette reports. Other events,
// including malformed or unrelated OSC, pass through unchanged and in order.
func (m *model) filterHostPalette(_ tea.Model, msg tea.Msg) tea.Msg {
	sequence, ok := msg.(uv.UnknownOscEvent)
	if !ok {
		return msg
	}
	entries, ok := termio.ParsePaletteReport(string(sequence))
	if !ok {
		return msg
	}
	b := &m.paletteBatch
	start := !b.open
	if start {
		b.open = true
		b.epoch++
	}
	for _, entry := range entries {
		if b.colors[entry.Index] == nil {
			b.count++
		}
		b.colors[entry.Index] = entry.Color
	}
	switch {
	case b.count == len(b.colors):
		return hostPaletteFlush(b.epoch)
	case start:
		return hostPaletteStart(b.epoch)
	}
	return nil
}

func hostPaletteDeadline(epoch uint64) tea.Cmd {
	return tea.Tick(hostPaletteWindow, func(time.Time) tea.Msg { return hostPaletteFlush(epoch) })
}

// flushHostPalette applies the open batch. A deadline for a batch that already
// completed, or for an earlier one, is ignored.
func (m *model) flushHostPalette(epoch uint64) {
	b := &m.paletteBatch
	if !b.open || epoch != b.epoch {
		return
	}
	colors := b.colors
	*b = hostPaletteBatch{epoch: b.epoch}
	m.applyHostPalette(colors)
}

// updateHostPalette applies a report that reached Update without the filter.
func (m *model) updateHostPalette(sequence string) {
	entries, ok := termio.ParsePaletteReport(sequence)
	if !ok {
		return
	}
	var colors [256]color.Color
	for _, entry := range entries {
		colors[entry.Index] = entry.Color
	}
	m.applyHostPalette(colors)
}

// applyHostPalette re-applies host colors once per surface, not once per entry.
func (m *model) applyHostPalette(colors [256]color.Color) {
	for i, c := range colors {
		if c != nil {
			m.hostPalette[i] = c
		}
	}
	m.console.SetPalette(colors)
	m.eachSession(func(p *panes.Pane) { p.Session.SetPalette(colors) })
}
