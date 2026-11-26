package followview

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/markdownview"
)

// viewDriver runs Tea commands concurrently and applies their messages on the
// test goroutine, as the real event loop does.
type viewDriver struct {
	model   *Model
	in      <-chan tea.Msg
	out     chan<- tea.Msg
	pending int
}

func newViewDriver(m *Model) *viewDriver {
	messages := make(chan tea.Msg, 32)
	return &viewDriver{model: m, in: messages, out: messages}
}
func (d *viewDriver) start(cmd tea.Cmd) {
	if cmd != nil {
		d.pending++
		go func() { d.out <- cmd() }()
	}
}
func (d *viewDriver) accept(msg tea.Msg) {
	d.pending--
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, cmd := range msg {
			d.start(cmd)
		}
	default:
		_, next := d.model.Update(msg)
		d.start(next)
	}
}
func (d *viewDriver) ready() {
	for {
		synctest.Wait()
		select {
		case msg := <-d.in:
			d.accept(msg)
		default:
			return
		}
	}
}
func drive(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	d := newViewDriver(m)
	d.start(cmd)
	for d.pending != 0 {
		d.accept(<-d.in)
	}
}

// A slow diagram and a slow check of a visible link together still show the
// document at the publish point: the source with "drawing…" and the link
// inert. The link becomes active when its check ends, the drawing when its
// render ends.
func TestFollowPublishesByItsBudgetWithSlowLinksAndDrawings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &fakeRenderer{cost: 8 * time.Second}
		path := fixtureDir(t) + "/doc.md"
		writeFixture(t, path, []byte("[file](a.md)\n\n"+fence(1)))
		m := New(context.Background(), Options{Reader: HostReader{Path: path}, Path: path, Format: Markdown, Mermaid: r})
		t.Cleanup(m.Close)
		m.opts.Navigation = &filelink.Navigator{Context: filelink.Context{Mode: "host", Base: "/p", Root: "/p"}, Probe: func(ctx context.Context, targets []filelink.Target) map[filelink.Target]error {
			select {
			// Past the publish point, inside the navigator's own probe bound.
			case <-time.After(6 * time.Second):
			case <-ctx.Done():
			}
			out := map[filelink.Target]error{}
			for _, target := range targets {
				out[target] = nil
			}
			return out
		}}
		_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
		start := time.Now()
		msg, ok := cmd().(resultMsg)
		if !ok || time.Since(start) > drawBudget || msg.admitting == nil {
			t.Fatalf("first view after %s (budget %s), admissions pending %v", time.Since(start), drawBudget, ok && msg.admitting != nil)
		}
		_, next := m.Update(msg)
		if _, admitted := m.admissions["a.md"]; admitted {
			t.Fatal("an unchecked link is active")
		}
		if rows, notes := drawn(m); len(rows) != 0 || strings.Join(notes, "|") != noteDrawing {
			t.Fatalf("first view: rows %q notes %q", rows, notes)
		}
		drive(t, m, next)
		if _, admitted := m.admissions["a.md"]; !admitted {
			t.Fatal("the link was not admitted after its check")
		}
		if rows, notes := drawn(m); len(rows) != 1 || len(notes) != 0 {
			t.Fatalf("final view: rows %q notes %q", rows, notes)
		}
	})
}

// The first probe takes longer than publication and succeeds. A second probe
// returns no admission, so an old result cannot make rewritten text clickable.
func followWithLateAdmission(t *testing.T) (*Model, string, chan struct{}, tea.Cmd) {
	t.Helper()
	path := fixtureDir(t) + "/doc.md"
	writeFixture(t, path, []byte("[old](target.md)\n"))
	release := make(chan struct{})
	var probes atomic.Int32
	navigator := &filelink.Navigator{Context: filelink.Context{Mode: "host", Base: "/p", Root: "/p"}, Probe: func(ctx context.Context, targets []filelink.Target) map[filelink.Target]error {
		if probes.Add(1) != 1 {
			return nil
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil
		}
		return map[filelink.Target]error{targets[0]: nil}
	}}
	m := New(t.Context(), Options{Reader: HostReader{Path: path}, Path: path, Format: Markdown, Navigation: navigator})
	t.Cleanup(m.Close)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	first, ok := cmd().(resultMsg)
	if !ok || first.admitting == nil {
		t.Fatal("refresh did not publish with a pending check")
	}
	_, late := m.Update(first)
	if late == nil || !m.checking["target.md"] || m.observed["target.md"] || followHasActiveLink(m) {
		t.Fatal("unfinished check was treated as completed")
	}
	return m, path, release, late
}

func followHasActiveLink(m *Model) bool {
	for _, row := range m.view.rows {
		for _, run := range row.Runs {
			if run.Act.Kind == markdownview.IntentLocalFile && run.Act.Value == "target.md" {
				return true
			}
		}
	}
	return false
}

type heldFollowReader struct {
	Reader
	entered, resume chan struct{}
}

func (r heldFollowReader) Read(ctx context.Context, q Query) (Snapshot, error) {
	close(r.entered)
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-r.resume:
		return r.Reader.Read(ctx, q)
	}
}

func TestFollowLateAdmissionSurvivesOverlappingRefresh(t *testing.T) {
	for _, running := range []bool{true, false} {
		name := "refresh already computed"
		if running {
			name = "refresh worker still running"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, _, release, late := followWithLateAdmission(t)
				var held heldFollowReader
				if running {
					held = heldFollowReader{Reader: m.opts.Reader, entered: make(chan struct{}), resume: make(chan struct{})}
					m.opts.Reader = held
				}
				refresh := m.work(true, false)
				finished := make(chan tea.Msg, 1)
				go func() { finished <- refresh() }()
				var next tea.Msg
				if running {
					<-held.entered
				} else {
					next = <-finished
				}
				close(release)
				_, followup := m.Update(late())
				if followup != nil {
					t.Fatal("late reply started new work beside the refresh")
				}
				if running {
					close(held.resume)
					next = <-finished
					m.opts.Reader = held.Reader // subsequent renders need no held read
				}
				_, render := m.Update(next)
				drive(t, m, render)
				if !m.observed["target.md"] || m.checking["target.md"] || !followHasActiveLink(m) {
					t.Fatal("refresh lost the late admission")
				}
			})
		})
	}
}

func TestFollowResizeDoesNotDiscardPendingAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _, release, late := followWithLateAdmission(t)
		_, resize := m.Update(tea.WindowSizeMsg{Width: 48, Height: 12})
		drive(t, m, resize)
		if followHasActiveLink(m) || !m.checking["target.md"] {
			t.Fatal("resize changed the still-pending check")
		}
		close(release)
		drive(t, m, late)
		if !followHasActiveLink(m) {
			t.Fatal("successful reply was lost across resize")
		}
	})
}

func TestFollowIgnoresAdmissionFromRewrittenSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, path, release, late := followWithLateAdmission(t)
		oldKey := m.snapshot.Key
		writeFixture(t, path, []byte("[new](target.md)\n"))
		drive(t, m, m.work(true, false))
		if m.snapshot.Key == oldKey || followHasActiveLink(m) {
			t.Fatal("rewritten snapshot did not replace its checks")
		}
		close(release)
		drive(t, m, late)
		if followHasActiveLink(m) || len(m.admissions) != 0 {
			t.Fatal("old reply affected the rewritten snapshot")
		}
	})
}
