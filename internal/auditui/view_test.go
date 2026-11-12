package auditui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
		if err := m.stream.add(t.Context(), audit.Event{Cursor: uint64(i + 1), Time: time.Now(), Rule: fmt.Sprint(i) + strings.Repeat("long rule ", 30), Severity: "Notice"}); err != nil {
			t.Fatal(err)
		}
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
	if len(m.sink.snapshot()) != 0 || cmd == nil {
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
	if len(m.sink.snapshot()) != 0 {
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

func TestAuditViewFollowsAnInitiallyEmptyHistoryWithoutRepeatingTail(t *testing.T) {
	var queries []audit.Query
	m := New(t.Context(), "test", func(_ context.Context, q audit.Query) (Batch, error) {
		queries = append(queries, q)
		return Batch{}, nil
	})
	m, _ = apply(m, m.load()())
	m, cmd := apply(m, tick{})
	m, _ = apply(m, cmd())
	if len(queries) != 2 || !queries[0].Tail || queries[1].Tail || queries[1].After != 0 {
		t.Fatal("idle poll repeated the initial tail", queries)
	}
	m, _ = apply(m, key('b'))
	m, _ = apply(m, key('x'))
	m, cmd = apply(m, key(tea.KeyEnter))
	_, _ = apply(m, cmd())
	if !queries[2].Tail || queries[2].After != 0 || queries[2].Box != "x" {
		t.Fatal("new filter did not start its own history", queries)
	}
}

func TestAuditViewIdlePollPreservesSelectionAndOnlyAppendsNewEvents(t *testing.T) {
	m := New(t.Context(), "test", nil)
	status := audit.Status{State: "running", Driver: "modern_ebpf"}
	summary := audit.Summary{Last: 2, Upper: 2, Count: 2, Until: time.Now()}
	events := []audit.Event{{Cursor: 1, Rule: "one"}, {Cursor: 2, Rule: "two"}}
	m, _ = apply(m, loaded{Batch: Batch{Events: events, Summary: summary, Status: status}})
	m, _ = apply(m, key(tea.KeyUp))
	before := m.View().Content
	summary.Count = 0
	summary.Until = summary.Until.Add(time.Second)
	m, _ = apply(m, loaded{Batch: Batch{Summary: summary, Status: status}})
	if m.selected != 0 || m.after != 2 || len(m.sink.snapshot()) != 2 || m.View().Content != before {
		t.Fatal("idle poll churned the view", m.selected, m.View().Content)
	}
	summary.Last, summary.Upper, summary.Count = 3, 3, 1
	m, _ = apply(m, loaded{Batch: Batch{Events: []audit.Event{{Cursor: 3, Rule: "three"}}, Summary: summary, Status: status}})
	if len(m.sink.snapshot()) != 3 || m.selected != 2 || m.after != 3 {
		t.Fatal(m.sink.snapshot(), m.selected, m.after)
	}
	summary.Drops, summary.MetadataGaps, summary.Gap = 2, 3, true
	m, _ = apply(m, loaded{Batch: Batch{Summary: summary, Status: status}})
	if !strings.Contains(m.View().Content, "collector drops=2") || !strings.Contains(m.View().Content, "metadata-gaps=3") || !strings.Contains(m.View().Content, "retention-gap=true") {
		t.Fatal("loss changes hidden", m.View().Content)
	}
}

func TestAuditPrettyMessageFieldsAndColor(t *testing.T) {
	event := audit.Event{Rule: "Yolo audit test", Severity: "Warning", Executable: "/tmp/probe\x1b[31m", Container: "host", Result: "SUCCESS", OutputFields: map[string]string{"proc.name": "probe", "user.name": "root", "user.uid": "0", "container.name": "<NA>", "fs.path.target": "/etc/target", "proc.args": "SECRET"}}
	plain := EventText(event, EventOptions{})
	for _, required := range []string{"(vm)", "Yolo audit test executable=", "process=probe", "user=root", "uid=0"} {
		if !strings.Contains(plain, required) {
			t.Fatal("missing field", required, plain)
		}
	}
	if strings.ContainsAny(plain, "\x1b\r") || strings.Contains(plain, "<NA>") || strings.Contains(plain, "SECRET") {
		t.Fatal(plain)
	}
	if strings.Contains(plain, "fs.path.target=") || strings.Contains(plain, "proc.name=") {
		t.Fatal("default output repeated raw fields", plain)
	}
	verbose := EventText(event, EventOptions{Verbose: true, Width: 48})
	if !strings.Contains(verbose, "        fs.path.target=") || !strings.Contains(verbose, "        proc.name=") {
		t.Fatal(verbose)
	}
	for _, line := range strings.Split(verbose, "\n") {
		if ansi.StringWidth(line) > 48 {
			t.Fatal("wrapped output exceeds width", line)
		}
	}
	colored := EventText(event, EventOptions{Color: true})
	if ansi.Strip(colored) != plain || !strings.Contains(colored, "\x1b[1;38;5;3m") || !strings.Contains(colored, "\x1b[1mYolo audit test") || !strings.Contains(colored, "\x1b[2mprocess=") {
		t.Fatal("styles changed content or omitted severity/key/rule emphasis", colored)
	}
	m := New(t.Context(), "test", nil).WithColor(true)
	m, _ = apply(m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m, _ = apply(m, loaded{Batch: Batch{Events: []audit.Event{event}, Summary: audit.Summary{Last: 1}}})
	if !strings.Contains(ansi.Strip(m.View().Content), "Yolo audit test executable=") {
		t.Fatal("selected message hidden", m.View().Content)
	}
	m, _ = apply(m, key(tea.KeyEnter))
	if !strings.Contains(ansi.Strip(m.View().Content), "fs.path.target=") {
		t.Fatal("selected fields hidden", m.View().Content)
	}
}

func TestAuditOriginLabelsDoNotInventBoxIdentity(t *testing.T) {
	for _, fixture := range []struct {
		event audit.Event
		want  string
	}{
		{audit.Event{Container: "host"}, "(vm)"},
		{audit.Event{}, "(unknown)"},
		{audit.Event{Container: "abcdef123456"}, "(no box)"},
		{audit.Event{Container: "abcdef123456", Box: "worker", MetadataAvailable: true}, "worker"},
	} {
		got := eventLine(fixture.event, false)
		if !strings.Contains(got, "  "+fmt.Sprintf("%-14s", fixture.want)+"  ") {
			t.Fatal(got, fixture.want)
		}
	}
}

func TestAuditLongPathsWrapWithIndentedContinuations(t *testing.T) {
	event := audit.Event{Rule: "Yolo persistence change", Severity: "Warning", Container: "host", OutputFields: map[string]string{
		"fs.path.source": "/var/lib/containerd/" + strings.Repeat("层/", 120) + "getty.service",
		"fs.path.target": "/usr/lib/systemd/system/getty@.service",
	}}
	for _, verbose := range []bool{false, true} {
		plain := EventText(event, EventOptions{Verbose: verbose, Width: 48})
		colored := EventText(event, EventOptions{Verbose: verbose, Width: 48, Color: true})
		if !utf8.ValidString(plain) || ansi.Strip(colored) != plain || !strings.Contains(plain, "层/") {
			t.Fatal("wrapping lost text or changed styled content", plain)
		}
		for i, line := range strings.Split(plain, "\n") {
			if ansi.StringWidth(line) > 48 || (i > 0 && !strings.HasPrefix(line, "    ")) {
				t.Fatal("long path lost width or indentation", line)
			}
		}
	}
}
