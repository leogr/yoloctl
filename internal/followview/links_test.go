package followview

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/linkview"
	"github.com/leogr/yoloctl/internal/markdownview"
)

func TestFollowBrowserPromptRequiresDeliberateVisibleApproval(t *testing.T) {
	m, _ := fileModel(t, "[web](https://example.org)", Markdown)
	opened := ""
	m.opts.OpenLink = func(u string) tea.Cmd { return func() tea.Msg { opened = u; return nil } }
	m.opts.ConfirmLinks = true
	if cmd := m.activateLink(markdownview.Action{Kind: markdownview.IntentURL, Value: "https://example.org"}); cmd != nil || m.linkPrompt == nil {
		t.Fatal("missing prompt")
	}
	for _, msg := range []tea.Msg{tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl, IsRepeat: true}), tea.KeyPressMsg(tea.Key{Text: "ctrl+o"}), tea.PasteStartMsg{}, tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}), tea.PasteEndMsg{}} {
		_, c := m.Update(msg)
		if c != nil || m.linkPrompt == nil || opened != "" {
			t.Fatalf("approved %#v", msg)
		}
	}
	m.width, m.height = 19, 6
	if c := key(m, 'o', tea.ModCtrl); c != nil {
		t.Fatal("approved hidden destination")
	}
	m.width, m.height = 80, 12
	c := key(m, 'o', tea.ModCtrl)
	if c == nil {
		t.Fatal("no deliberate approval")
	}
	c()
	if opened != "https://example.org" {
		t.Fatal(opened)
	}
	m.opts.OpenLink = nil
	m.activateLink(markdownview.Action{Kind: markdownview.IntentURL, Value: "https://example.org"})
	if !strings.Contains(m.notice, "seams.browser") {
		t.Fatal(m.notice)
	}
}
func TestFollowVisibleLocalLinksBeyondFirstBatchAndStandaloneRefusal(t *testing.T) {
	var text strings.Builder
	for i := range 100 {
		fmt.Fprintf(&text, "[file%d](file%d.md)\n", i, i)
	}
	m, _ := fileModel(t, text.String(), Markdown)
	calls := 0
	n := &filelink.Navigator{Context: filelink.Context{Mode: "host", Base: "/p", Root: "/p"}, Probe: func(_ context.Context, ts []filelink.Target) map[filelink.Target]error {
		calls++
		if len(ts) > filelink.MaxTargets {
			t.Fatal("unbounded batch")
		}
		out := map[filelink.Target]error{}
		for _, v := range ts {
			out[v] = nil
		}
		return out
	}}
	m.opts.Navigation = n
	applyWork(t, m, m.work(false, true))
	if _, ok := m.admissions["file99.md"]; !ok {
		t.Fatal("visible tail link was not admitted")
	}
	act := markdownview.Action{Kind: markdownview.IntentLocalFile, Value: "file99.md"}
	cmd := m.activateLink(act)
	if cmd == nil {
		t.Fatal("no open check")
	}
	if m.activateLink(act) != nil {
		t.Fatal("unbounded concurrent checks")
	}
	result := cmd()
	m.Update(result)
	if !strings.Contains(m.notice, "yoloctl TUI") {
		t.Fatal("standalone did not refuse", m.notice)
	}
	if calls < 2 {
		t.Fatal("opening failed to recheck")
	}
	opened := 0
	m.opts.OpenFile = func(filelink.Target) tea.Cmd { return func() tea.Msg { opened++; return nil } }
	cmd = m.activateLink(act)
	result = cmd()
	m.gen++
	_, next := m.Update(result)
	if next != nil || opened != 0 {
		t.Fatal("stale open activated")
	}
}
func TestFollowClickAndDragRemainSeparateOnRowZero(t *testing.T) {
	m, _ := fileModel(t, "[web](https://example.org)", Markdown)
	opened, copied := 0, 0
	m.opts.OpenLink = func(string) tea.Cmd { return func() tea.Msg { opened++; return nil } }
	m.opts.Copy = func(string) tea.Cmd { return func() tea.Msg { copied++; return nil } }
	applyWork(t, m, m.work(false, true))
	m.mouse(tea.MouseClickMsg(tea.Mouse{X: 0, Y: 0, Button: tea.MouseLeft}))
	cmd := m.mouse(tea.MouseReleaseMsg(tea.Mouse{X: 0, Y: 0, Button: tea.MouseLeft}))
	if cmd == nil {
		t.Fatal("row zero link not clickable")
	}
	cmd()
	if opened != 1 {
		t.Fatal(opened)
	}
	m.mouse(tea.MouseClickMsg(tea.Mouse{X: 0, Y: 0, Button: tea.MouseLeft}))
	m.mouse(tea.MouseMotionMsg(tea.Mouse{X: 2, Y: 0, Button: tea.MouseLeft}))
	cmd = m.mouse(tea.MouseReleaseMsg(tea.Mouse{X: 2, Y: 0, Button: tea.MouseLeft}))
	if cmd != nil {
		cmd()
	}
	if opened != 1 || copied != 1 {
		t.Fatal(opened, copied)
	}
	m.mouse(tea.MouseClickMsg(tea.Mouse{X: 0, Y: 0, Button: tea.MouseLeft}))
	m.gen++
	if c := m.mouse(tea.MouseReleaseMsg(tea.Mouse{X: 0, Y: 0, Button: tea.MouseLeft})); c != nil {
		t.Fatal("stale geometry activated")
	}
}

func TestFollowAdmissionContinuesBeyondRefusedFirstBatch(t *testing.T) {
	var raw strings.Builder
	for i := range 100 {
		fmt.Fprintf(&raw, "[file%d](file%d.md)\n", i, i)
	}
	m, _ := fileModel(t, raw.String(), Markdown)
	calls := 0
	m.opts.Navigation = &filelink.Navigator{Context: filelink.Context{Mode: "host", Base: "/p", Root: "/p"}, Probe: func(_ context.Context, targets []filelink.Target) map[filelink.Target]error {
		calls++
		if len(targets) > filelink.MaxTargets {
			t.Fatal("unbounded batch")
		}
		out := map[filelink.Target]error{}
		for _, target := range targets {
			out[target] = nil
			if strings.HasSuffix(target.Path, "file0.md") {
				out[target] = fmt.Errorf("unreadable")
			}
		}
		return out
	}}
	m.width, m.height = 80, 120
	m.gen++
	applyWork(t, m, m.work(false, true))
	if _, ok := m.admissions["file99.md"]; !ok || calls != 2 {
		t.Fatalf("later visible target starved: calls=%d admitted=%d", calls, len(m.admissions))
	}
	if _, ok := m.admissions["file0.md"]; ok {
		t.Fatal("unreadable admitted")
	}
}

func TestLocalAdmissionRelayoutReusesMermaidBudget(t *testing.T) {
	m, _ := fileModel(t, "[file](a.md)\n```mermaid\nflowchart TD\n A-->B\n```", Markdown)
	renderer := &fakeRenderer{}
	m.opts.Mermaid = renderer
	m.gen++ // changed render inputs after fileModel's initial plain-fence view
	m.opts.Navigation = &filelink.Navigator{Context: filelink.Context{Mode: "host", Base: "/p", Root: "/p"}, Probe: func(_ context.Context, targets []filelink.Target) map[filelink.Target]error {
		out := map[filelink.Target]error{}
		for _, target := range targets {
			out[target] = nil
		}
		return out
	}}
	applyWork(t, m, m.work(false, true))
	if len(renderer.calls) != 1 {
		t.Fatalf("one refresh spent Mermaid budget %d times", len(renderer.calls))
	}
}

// A refresh shows one layout of its bytes with the admissions it ends with,
// whether or not its probe admitted a new target.
func TestFollowRefreshViewIsOneLayoutWithFinalAdmissions(t *testing.T) {
	m, _ := fileModel(t, "[file](a.md) [web](https://example.org)\n\n| A | B |\n| --- | --- |\n| [c](c.md) | d |\n\n```mermaid\nflowchart TD\n A-->B\n```\n", Markdown)
	m.opts.Mermaid = &fakeRenderer{}
	readable := false
	probes := 0
	m.opts.Navigation = &filelink.Navigator{Context: filelink.Context{Mode: "host", Base: "/p", Root: "/p"}, Probe: func(_ context.Context, targets []filelink.Target) map[filelink.Target]error {
		probes++
		out := map[filelink.Target]error{}
		for _, target := range targets {
			out[target] = errors.New("unreadable")
			if readable {
				out[target] = nil
			}
		}
		return out
	}}
	oneLayout := func() *rendered {
		draw := drawing{renderer: m.opts.Mermaid, attempts: m.diagramAttempts, now: m.now}
		view := renderFileTables(m.raw, false, false, Markdown, m.width, m.method, m.theme, draw, nil, linkview.Policy(m.admissions, string(m.raw)))
		return view
	}
	local := func(destination string) bool {
		for _, row := range m.view.rows {
			for _, run := range row.Runs {
				if run.Act == (markdownview.Action{Kind: markdownview.IntentLocalFile, Value: destination}) {
					return true
				}
			}
		}
		return false
	}
	m.gen++
	applyWork(t, m, m.work(false, true))
	if probes != 1 || len(m.admissions) != 0 || local("a.md") || !reflect.DeepEqual(m.view, oneLayout()) {
		t.Fatalf("refused refresh: probes=%d admissions=%d", probes, len(m.admissions))
	}
	readable = true
	applyWork(t, m, key(m, 'r', 0))
	if probes != 2 || len(m.admissions) != 2 || !local("a.md") || !local("c.md") || !reflect.DeepEqual(m.view, oneLayout()) {
		t.Fatalf("admitting refresh: probes=%d admissions=%d", probes, len(m.admissions))
	}
}
