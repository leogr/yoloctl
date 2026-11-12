package auditui

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/logging/logtheme"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestAuditModelRepeatDetailsStayWithFirstDisplayedOwner(t *testing.T) {
	m := New(t.Context(), "test", nil)
	t.Cleanup(func() { _ = m.Close() })
	m, _ = apply(m, tea.WindowSizeMsg{Width: 160, Height: 40})
	m, _ = apply(m, loaded{Batch: Batch{Events: []audit.Event{
		streamEvent("Yolo audit test", "alice"), streamEvent("Yolo audit test", "bob"), streamEvent("B", "carol"),
	}}})
	rows := m.visible()
	if len(rows) != 3 || rows[1].kind != prettylog.EmissionRepeatSummary {
		t.Fatal("repeat did not become a separate logical row", rows)
	}
	m, _ = apply(m, key(tea.KeyUp))
	m, _ = apply(m, key(tea.KeyEnter))
	view := m.View().Content
	if !strings.Contains(view, "First displayed event") || !strings.Contains(view, `"alice"`) || strings.Contains(view, `"bob"`) || strings.Contains(view, `"carol"`) {
		t.Fatal("summary details retargeted", view)
	}
	owner := m.selectedOwner
	// A pending summary wake may run after the user selected it. It must not
	// move selection to the next unrelated event.
	m, _ = apply(m, rowsReady{m.sink})
	if m.selectedOwner != owner || m.selectedKind != prettylog.EmissionRepeatSummary {
		t.Fatal("emission wake changed the selected owner")
	}
	oldSink, oldStream := m.sink, m.stream
	m, _ = apply(m, key('b'))
	m, _ = apply(m, key('x'))
	m, _ = apply(m, key(tea.KeyEnter))
	if len(m.visible()) != 0 || m.sink == oldSink {
		t.Fatal("box filter reused old rows")
	}
	if len(oldSink.snapshot()) != 0 {
		t.Fatal("replaced value-model still retains another event history")
	}
	if err := oldStream.add(t.Context(), streamEvent("late", "old")); err == nil {
		t.Fatal("old source still accepts events")
	}
	m, _ = apply(m, rowsReady{oldSink})
	if len(m.visible()) != 0 {
		t.Fatal("old timer wake applied to a new filter")
	}
}

func TestAuditModelTimerWakeAndCloseNeedNoProgramSend(t *testing.T) {
	m := New(t.Context(), "test", nil)
	if batch, ok := m.Init()().(tea.BatchMsg); !ok || len(batch) != 3 {
		t.Fatal("Init must schedule background, load and emission listener")
	}
	_ = m.stream.add(t.Context(), streamEvent("A", "first"))
	_ = m.stream.add(t.Context(), streamEvent("A", "second"))
	_ = m.stream.flush()
	ready := m.waitRows()()
	m, _ = apply(m, ready)
	if len(m.visible()) != 2 {
		t.Fatal("pending summary was not delivered")
	}
	closed := make(chan struct{})
	go func() { m, _ = apply(m, key('q')); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("view exit needs an event-loop callback")
	}
	if _, cmd := apply(m, rowsReady{m.sink}); cmd != nil {
		t.Fatal("closed sink was rearmed")
	}
}

func TestAuditInitialValueClosesTheCurrentFilterStream(t *testing.T) {
	initial := New(t.Context(), "test", nil)
	m := initial
	m, _ = apply(m, key('b'))
	m, _ = apply(m, key('x'))
	m, _ = apply(m, key(tea.KeyEnter))
	if m.stream == initial.stream {
		t.Fatal("filter did not replace the stream")
	}
	_ = m.stream.add(t.Context(), streamEvent("A", "first"))
	_ = m.stream.add(t.Context(), streamEvent("A", "repeat"))
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.stream.closedDone:
	default:
		t.Fatal("caller retained only the original value; current timer leaked")
	}
	rows := m.visible()
	if len(rows) != 2 || rows[1].kind != prettylog.EmissionRepeatSummary {
		t.Fatal("current filter did not drain")
	}
}

func TestAuditHealthFeedbackSurvivesErrorOnlyEventFiltering(t *testing.T) {
	m := New(t.Context(), "test", nil).WithVerbosity(prettylog.Errors)
	t.Cleanup(func() { _ = m.Close() })
	drops := uint64(7)
	m, _ = apply(m, loaded{Batch: Batch{Status: audit.Status{State: "running", DriverDrops: &drops}, Events: []audit.Event{streamEvent("hidden notice", "user")}}})
	view := m.View().Content
	if strings.Contains(view, "hidden notice") || !strings.Contains(view, "! collector drops=0  driver drops=7") {
		t.Fatal("loss feedback was hidden or misclassified", view)
	}
	m, _ = apply(m, loaded{Err: errors.New("read refused")})
	if !strings.Contains(m.View().Content, "✗ Unavailable: read refused") {
		t.Fatal("error-only view swallowed its refusal")
	}
}

func TestAuditThemeIsUnknownUntilOwnedBackgroundReply(t *testing.T) {
	t.Cleanup(func() { logging.SetPalette(prettylog.ANSIPalette) })
	e := streamEvent("Yolo audit test", "alice")
	e.Severity = "Warning"
	m := New(t.Context(), "test", nil).WithColor(true)
	t.Cleanup(func() { _ = m.Close() })
	unknown := m.renderer.heading(e)
	if !strings.Contains(unknown, "38;5;3m") {
		t.Fatal("unknown background guessed a theme", unknown)
	}
	for _, bg := range []color.Color{color.Black, color.White} {
		m, _ = apply(m, tea.BackgroundColorMsg{Color: bg})
		want := newEventRenderer(EventOptions{Color: true, Palette: logtheme.Palette(bg == color.Black, bg)}).heading(e)
		if m.renderer.heading(e) != want || ansi.Strip(want) != ansi.Strip(unknown) {
			t.Fatal("owned background did not supply shared roles")
		}
	}
}

func TestAuditPrinterUsesSingleConsecutiveEngineAndFinalDrain(t *testing.T) {
	for _, rules := range [][]string{{"A", "A", "B"}, {"A", "B", "A"}} {
		var out bytes.Buffer
		p := NewLogPrinter(t.Context(), &out, prettylog.Normal, EventOptions{})
		for i, rule := range rules {
			if err := p.Add(t.Context(), streamEvent(rule, "user")); err != nil {
				t.Fatal(err)
			}
			if i == 0 && out.Len() == 0 {
				t.Fatal("first event waited for the window")
			}
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		folded := rules[0] == rules[1]
		if strings.Contains(out.String(), "repeated 1 more time") != folded {
			t.Fatal("changed consecutive-only semantics", out.String())
		}
	}
	var out bytes.Buffer
	p := NewLogPrinter(t.Context(), &out, prettylog.Errors, EventOptions{})
	for _, severity := range []string{"Notice", "Warning", "Error"} {
		e := streamEvent(severity, "user")
		e.Severity = severity
		_ = p.Add(t.Context(), e)
	}
	_ = p.Close()
	if strings.Contains(out.String(), "Notice") || strings.Contains(out.String(), "Warning") || !strings.Contains(out.String(), "Error") {
		t.Fatal("event verbosity was ignored", out.String())
	}
}

type failedAuditWriter struct{ err error }

func (w failedAuditWriter) Write([]byte) (int, error) { return 0, w.err }

func TestAuditPrinterRetainsWriterFailureWithoutRetry(t *testing.T) {
	want := errors.New("display closed")
	p := NewLogPrinter(context.Background(), failedAuditWriter{want}, prettylog.Normal, EventOptions{})
	if err := p.Add(t.Context(), streamEvent("A", "user")); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := p.Close(); !errors.Is(err, want) {
		t.Fatal(err)
	}
}
