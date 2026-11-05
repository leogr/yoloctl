package auditui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/audit"
)

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: code}) }
func apply(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func TestAuditViewIsBoundedAndReadOnly(t *testing.T) {
	calls := 0
	fetch := func(context.Context, audit.Query) (Batch, error) { calls++; return Batch{}, nil }
	m := New(t.Context(), "test", fetch)
	if calls != 0 {
		t.Fatal("constructor performed work")
	}
	for i := 0; i < 500; i++ {
		m.events = append(m.events, audit.Event{Cursor: uint64(i + 1), Time: time.Now(), Rule: strings.Repeat("long rule ", 30), Severity: "Notice"})
	}
	m.selected = 499
	m.details = true
	for _, size := range [][2]int{{100, 32}, {80, 24}, {40, 8}, {1, 1}} {
		m, _ = apply(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > size[1] {
			t.Fatal("height overflow", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("width overflow", size, line)
			}
		}
	}
	if calls != 0 {
		t.Fatal("rendering contacted the controller")
	}
}

func TestAuditViewPauseAndFilterDoNotApplyStaleResponses(t *testing.T) {
	var queries []audit.Query
	m := New(t.Context(), "test", func(_ context.Context, q audit.Query) (Batch, error) {
		queries = append(queries, q)
		return Batch{}, nil
	})
	m, _ = apply(m, key('b'))
	m, _ = apply(m, key('x'))
	if m.filter != "" {
		t.Fatal("draft changed the active filter")
	}
	m, cmd := apply(m, key(tea.KeyEnter))
	if cmd != nil || m.filter != "x" {
		t.Fatal("parallel query launched while old request remained active")
	}
	stale := loaded{Batch: Batch{Events: []audit.Event{{Cursor: 1, Box: "old"}}, Summary: audit.Summary{Last: 1}}, Generation: 0}
	m, cmd = apply(m, stale)
	if len(m.events) != 0 || cmd == nil {
		t.Fatal("applied old filter response")
	}
	_ = cmd()
	if len(queries) != 1 || queries[0].Box != "x" {
		t.Fatal(queries)
	}
	m, _ = apply(m, key(tea.KeySpace))
	if !m.paused {
		t.Fatal("pause did not pause")
	}
	m, _ = apply(m, loaded{Generation: m.generation, Batch: Batch{Events: []audit.Event{{Cursor: 2}}}})
	if len(m.events) != 0 {
		t.Fatal("paused view changed")
	}
	m, _ = apply(m, key(tea.KeySpace))
	m, _ = apply(m, key('b'))
	m, _ = apply(m, key('y'))
	m, _ = apply(m, key(tea.KeyEscape))
	if m.filter != "x" {
		t.Fatal("cancelled draft changed filter")
	}
}
