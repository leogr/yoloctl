package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi/kitty"
)

func graphicsReply(id int, payload string) uv.KittyGraphicsEvent {
	return uv.KittyGraphicsEvent{Options: kitty.Options{ID: id}, Payload: []byte(payload)}
}

// probeQueryID reads the query ID back from the raw graphics query the
// probe wrote.
func probeQueryID(t *testing.T, m *model) int {
	t.Helper()
	q := m.imageHost.probe.GraphicsQuery()
	var id int
	if _, err := fmt.Sscanf(strings.TrimPrefix(q, "\x1b_Gi="), "%d,", &id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestImageProbeSucceedsOnlyWithEverySignal(t *testing.T) {
	m := newTestModel(t, 40, 8)
	m.hostGeometry.width, m.hostGeometry.height = 9, 18
	if m.startImageProbe() == nil || m.imageHost.state != imageProbing {
		t.Fatal("probe did not start")
	}
	if m.startImageProbe() != nil {
		t.Fatal("second probe started")
	}
	id := probeQueryID(t, m)
	_, _ = m.Update(tea.TerminalVersionMsg{Name: "ghostty 1.3.1"})
	_, _ = m.Update(graphicsReply(id+1, "OK")) // not this probe's query
	if m.imageHost.state != imageProbing {
		t.Fatal("finished on an uncorrelated reply")
	}
	_, _ = m.Update(graphicsReply(id, "OK"))
	if !m.imagesSupported() || m.imageHost.terminal.Name != "ghostty" {
		t.Fatalf("state %d", m.imageHost.state)
	}
	// Losing the cell measurement withdraws support without a new probe.
	m.hostGeometry.width = 0
	if m.imagesSupported() {
		t.Fatal("supported without measured cells")
	}
}

func TestImageProbeFailsClosed(t *testing.T) {
	for name, feed := range map[string]func(*model, int){
		"nested libghostty": func(m *model, _ int) { _, _ = m.Update(tea.TerminalVersionMsg{Name: "libghostty"}) },
		"old kitty":         func(m *model, _ int) { _, _ = m.Update(tea.TerminalVersionMsg{Name: "kitty(0.27.0)"}) },
		"graphics refused":  func(m *model, id int) { _, _ = m.Update(graphicsReply(id, "EINVAL:bad")) },
		"silence": func(m *model, _ int) {
			_, _ = m.Update(imageProbeTick(m.imageHost.generation))
		},
	} {
		m := newTestModel(t, 40, 8)
		m.hostGeometry.width, m.hostGeometry.height = 9, 18
		m.startImageProbe()
		feed(m, probeQueryID(t, m))
		if m.imageHost.state != imageUnsupported || m.imagesSupported() {
			t.Errorf("%s: state %d", name, m.imageHost.state)
		}
		if m.startImageProbe() != nil {
			t.Errorf("%s: probe retried", name)
		}
	}
	// A stale tick from another generation changes nothing.
	m := newTestModel(t, 40, 8)
	m.startImageProbe()
	_, _ = m.Update(imageProbeTick(m.imageHost.generation + 1))
	if m.imageHost.state != imageProbing {
		t.Fatal("stale tick ended the probe")
	}
}

// Every order of timely signals succeeds at once, the cell measurement
// included, without waiting for the budget tick (review step 5 F2); signals
// after the tick do not.
func TestImageProbeOrdersAndLateSignals(t *testing.T) {
	version := func(m *model) { _, _ = m.Update(tea.TerminalVersionMsg{Name: "kitty(0.35.2)"}) }
	graphics := func(m *model) { _, _ = m.Update(graphicsReply(probeQueryID(t, m), "OK")) }
	cells := func(m *model) { _, _ = m.Update(uv.CellSizeEvent{Width: 9, Height: 18}) }
	for name, order := range map[string][]func(*model){
		"version graphics cells": {version, graphics, cells},
		"cells version graphics": {cells, version, graphics},
		"graphics cells version": {graphics, cells, version},
	} {
		m := newTestModel(t, 40, 8)
		m.startImageProbe()
		for _, step := range order {
			step(m)
		}
		if !m.imagesSupported() {
			t.Errorf("%s: state %d", name, m.imageHost.state)
		}
	}
	m := newTestModel(t, 40, 8)
	m.startImageProbe()
	id := probeQueryID(t, m)
	_, _ = m.Update(imageProbeTick(m.imageHost.generation))
	version(m)
	_, _ = m.Update(graphicsReply(id, "OK"))
	cells(m)
	if m.imagesSupported() {
		t.Fatal("signals after the budget counted")
	}
}
