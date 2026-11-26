package followview

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/mermaid"
	"github.com/leogr/yoloctl/internal/mermaid/admit"
)

// Renderer draws a fence at the card's inner width (mermaid.Client).
type Renderer interface {
	Lookup(source string, width int, method ansi.Method) (mermaid.Result, bool)
	Render(ctx context.Context, source string, width int, method ansi.Method) mermaid.Result
}

const (
	drawBudget  = 5 * time.Second // show a pending fence's source by this point
	maxDiagrams = 20              // count admitted fence occurrences, including repeats

	noteDrawing = "mermaid: drawing…"
	noteTooMany = "mermaid: not drawn (too many diagrams)"
	notePrefix  = "mermaid: not drawn ("
	noteSuffix  = ")"
)

type renderKey struct {
	source string
	width  int
	method ansi.Method
}

type diagramAttempt struct {
	fallback time.Time
	result   mermaid.Result
}

type diagramJob struct {
	key    renderKey
	cancel context.CancelFunc
}

type diagramDoneMsg struct {
	job    *diagramJob
	result mermaid.Result
}

type diagramWake struct {
	at     time.Time
	cancel context.CancelFunc
}

type diagramWakeMsg struct{ wake *diagramWake }

// drawing is an immutable diagram snapshot for the layout job. Layout can
// consult the renderer cache, but only the model dispatches work.
type drawing struct {
	renderer Renderer
	attempts map[renderKey]diagramAttempt
	now      func() time.Time
	fallback time.Time
}

type drawResult struct {
	diagrams    []markdownview.Diagram // source occurrence order
	attempts    map[renderKey]diagramAttempt
	order       []renderKey // distinct, eligible keys in source order
	hiddenUntil time.Time   // earliest fallback still hidden by this layout
}

func (d drawing) draw(sources []string, width int, method ansi.Method) drawResult {
	out := drawResult{
		diagrams: make([]markdownview.Diagram, len(sources)),
		attempts: make(map[renderKey]diagramAttempt),
	}
	admitted := 0
	for i, source := range sources {
		if decision := admit.Check(source); !decision.Admitted() {
			out.diagrams[i].Note = notePrefix + decision.Reason + noteSuffix
			continue
		}
		if admitted++; admitted > maxDiagrams {
			out.diagrams[i].Note = noteTooMany
			continue
		}
		key := renderKey{source, markdownview.DiagramWidth(width), method}
		attempt, seen := out.attempts[key]
		if !seen {
			attempt = d.attempts[key]
			if attempt.fallback.IsZero() {
				attempt.fallback = d.fallback
			}
			if attempt.result.Status == "" {
				if cached, ok := d.renderer.Lookup(key.source, key.width, key.method); ok {
					attempt.result = cached
				}
			}
			out.attempts[key] = attempt
			out.order = append(out.order, key)
		}
		switch attempt.result.Status {
		case "":
			pending := d.now().Before(attempt.fallback)
			out.diagrams[i] = markdownview.Diagram{Note: noteDrawing, Pending: pending}
			if pending && (out.hiddenUntil.IsZero() || attempt.fallback.Before(out.hiddenUntil)) {
				out.hiddenUntil = attempt.fallback
			}
		case mermaid.OK:
			out.diagrams[i].Lines = attempt.result.Lines
		default:
			out.diagrams[i].Note = notePrefix + attempt.result.Note() + noteSuffix
		}
	}
	return out
}

func (m *Model) resetDiagrams() {
	m.cancelDiagram()
	m.waitForDiagram(time.Time{})
	m.diagramAttempts, m.diagramOrder = nil, nil
}

func (m *Model) cancelDiagram() {
	if m.diagramActive != nil {
		m.diagramActive.cancel()
		m.diagramActive = nil
	}
}

// acceptDiagrams keeps a still-wanted attempt across file snapshots. This
// keeps both the original five-second point and a result that arrived while
// the layout job was in progress.
func (m *Model) acceptDiagrams(d drawResult) {
	for key, attempt := range d.attempts {
		if old, exists := m.diagramAttempts[key]; exists {
			attempt.fallback = old.fallback
			if old.result.Status != "" {
				attempt.result = old.result
			}
			d.attempts[key] = attempt
		}
	}
	m.diagramAttempts, m.diagramOrder = d.attempts, d.order
	if m.diagramActive != nil {
		if _, wanted := d.attempts[m.diagramActive.key]; !wanted {
			m.cancelDiagram()
		}
	}
}

func (m *Model) retryDiagrams() bool {
	retry := false
	fallback := m.now().Add(drawBudget)
	for key, attempt := range m.diagramAttempts {
		if attempt.result.Status != "" && attempt.result.Transient() {
			m.diagramAttempts[key] = diagramAttempt{fallback: fallback}
			retry = true
		}
	}
	return retry
}

func (m *Model) diagramCommands() tea.Cmd {
	var next renderKey
	for _, key := range m.diagramOrder {
		attempt := m.diagramAttempts[key]
		if attempt.result.Status != "" {
			continue
		}
		if next.source == "" || len(key.source) < len(next.source) {
			next = key
		}
	}
	var fallback time.Time
	if m.view != nil {
		fallback = m.view.drawn.hiddenUntil
	}
	wake := m.waitForDiagram(fallback)
	if m.diagramActive != nil || next.source == "" {
		return wake
	}
	ctx, cancel := context.WithCancel(m.jobs.ctx)
	job := &diagramJob{next, cancel}
	m.diagramActive = job
	jobs, renderer := m.jobs, m.opts.Mermaid
	return tea.Batch(wake, func() tea.Msg {
		return jobs.run(func(context.Context) tea.Msg {
			if ctx.Err() != nil {
				return nil
			}
			return diagramDoneMsg{job, renderer.Render(ctx, next.source, next.width, next.method)}
		})
	})
}

// One owned timer wakes the view at the earliest pending source deadline.
func (m *Model) waitForDiagram(at time.Time) tea.Cmd {
	if wake := m.diagramWake; wake != nil {
		if wake.at.Equal(at) {
			return nil
		}
		wake.cancel()
		m.diagramWake = nil
	}
	if at.IsZero() {
		return nil
	}
	ctx, cancel := context.WithCancel(m.jobs.ctx)
	wake := &diagramWake{at, cancel}
	m.diagramWake = wake
	jobs, now := m.jobs, m.now
	return func() tea.Msg {
		return jobs.run(func(context.Context) tea.Msg {
			timer := time.NewTimer(max(0, at.Sub(now())))
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil
			case <-timer.C:
				return diagramWakeMsg{wake}
			}
		})
	}
}
