package tui

import (
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/leogr/yoloctl/internal/terminal"
)

func TestHostGeometryMeasuresCellsWithoutWindowPadding(t *testing.T) {
	m := newTestModel(t, 201, 47)
	s, capture := mouseCapture(t, m, "")
	// User's native ioctl capture includes 11x20 extra pixels. Neither those
	// totals nor a separate window report may invent cell dimensions.
	_, _ = m.Update(uv.PixelSizeEvent{Width: 3830, Height: 1994})
	if s.LocalGeometry().CellWidth != 0 {
		t.Fatal("window pixels were treated as a cell measurement")
	}
	_, _ = m.Update(uv.CellSizeEvent{Width: 19, Height: 42})
	want := terminal.Geometry{Rows: 45, Cols: 201, CellWidth: 19, CellHeight: 42}
	if s.LocalGeometry() != want || m.console.LocalGeometry().CellHeight != 42 {
		t.Fatalf("measured child geometry: %+v / %+v", s.LocalGeometry(), m.console.LocalGeometry())
	}
	_, _ = s.Write([]byte("\x1b[14t\x1b[16t\x1b[18t"))
	if got := capture(); got != "\x1b[4;1890;3819t\x1b[6;42;19t\x1b[8;45;201t" {
		t.Fatalf("child geometry replies: %q", got)
	}
}

func TestHostGeometryQueriesStayBoundedAndUpdateFontOnly(t *testing.T) {
	m := newTestModel(t, 80, 24)
	request := m.hostGeometry.request
	for range 20 {
		if cmd := m.measureHostGeometry(); cmd != nil {
			t.Fatal("concurrent cell-size query")
		}
	}
	_, followup := m.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	if followup == nil || m.hostGeometry.request != request+1 {
		t.Fatal("resize during measurement did not schedule one follow-up")
	}
	_, _ = m.Update(geometryTimeout(request))
	if !m.hostGeometry.pending {
		t.Fatal("old query timeout cleared a newer request")
	}
	_, _ = m.Update(uv.CellSizeEvent{Width: 11, Height: 22})
	if g := m.console.LocalGeometry(); g.CellWidth != 11 || g.CellHeight != 22 {
		t.Fatalf("font-only resize lost: %+v", g)
	}
	_, _ = m.Update(uv.CellSizeEvent{Width: -1, Height: 22})
	_, _ = m.Update(uv.CellSizeEvent{Width: 4097, Height: 22})
	if m.hostGeometry.width != 11 {
		t.Fatal("invalid measurement replaced measured geometry")
	}
	_ = m.measureHostGeometry()
	_, cmd := m.Update(geometryTimeout(m.hostGeometry.request))
	if m.hostGeometry.pending || cmd != nil {
		t.Fatal("unresponsive terminal caused a query loop")
	}
}

func TestMeasuredGeometryThroughRealHostInput(t *testing.T) {
	m := newTestModel(t, 201, 47)
	p := tea.NewProgram(paletteKeyboardModel{m}, tea.WithContext(t.Context()),
		tea.WithInput(strings.NewReader("\x1b[6;42;19t\x1b[24~")), tea.WithOutput(io.Discard),
		tea.WithWindowSize(201, 47), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if g := m.console.LocalGeometry(); g.CellWidth != 19 || g.CellHeight != 42 {
		t.Fatalf("CSI16 reply did not reach model: %+v", g)
	}
}
