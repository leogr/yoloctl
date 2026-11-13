package followview

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

func followCompactFixture(t *testing.T) *Model {
	t.Helper()
	m, _ := fileModel(t, "|Name|Detail|\n|---|---|\n|same|"+(strings.Repeat("first words ", 25)+strings.Repeat("x", 200))+"|\n|same|"+(strings.Repeat("second words ", 25)+strings.Repeat("x", 200))+"|", Markdown)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 60})
	applyWork(t, m, cmd)
	m.top, m.following = 0, false
	return m
}
func followControl(t *testing.T, m *Model, value string) (int, int) {
	t.Helper()
	for i, row := range m.view.rows {
		x := 0
		for _, run := range row.Runs {
			if run.Act.Kind == markdownview.IntentToggleTableRow && run.Act.Value == value {
				return x, i - m.top + 1
			}
			x += m.method.StringWidth(run.Text)
		}
	}
	t.Fatal("missing table control", value)
	return 0, 0
}
func toggleFollowCell(t *testing.T, m *Model, value string) {
	t.Helper()
	x, y := followControl(t, m, value)
	m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	_, cmd := m.Update(tea.MouseReleaseMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	applyWork(t, m, cmd)
}
func TestFollowCompactToggleAndWholeCellCopy(t *testing.T) {
	m := followCompactFixture(t)
	if len(m.view.compact) != 2 {
		t.Fatal("not closed by default")
	}
	dl := m.view.doc.Lines[2]
	first, last := dl.Cells[0], dl.Cells[1]
	m.selection = &selection{anchor: point{2, first[0] + 1}, focus: point{2, last[0] + 3}}
	if got := m.selectedText(); got != dl.CopyText(first[0], last[1]) {
		t.Fatalf("cross-cell copy=%q", got)
	}
	m.selection = &selection{anchor: point{2, last[0] + 1}, focus: point{2, last[0] + 4}}
	if got := m.selectedText(); got != dl.CopyText(last[0]+1, last[0]+4) {
		t.Fatalf("partial cell copy=%q", got)
	}
	copied := ""
	m.opts.Copy = func(s string) tea.Cmd { copied = s; return nil }
	toggleFollowCell(t, m, "0:0")
	if m.view.compact[2] || !m.view.compact[3] || m.selection != nil || copied != "" {
		t.Fatal("toggle copied or failed to clear selection")
	}
	toggleFollowCell(t, m, "0:0")
	if !m.view.compact[2] {
		t.Fatal("row did not close")
	}
	applyWork(t, m, key(m, 'w', 0))
	if len(m.view.compact) != 0 {
		t.Fatal("w did not expand all")
	}
	applyWork(t, m, key(m, 'w', 0))
	if len(m.view.compact) != 2 {
		t.Fatal("w did not collapse all")
	}
}
func TestFollowCompactRefreshDuplicateKeysAndColumnReset(t *testing.T) {
	m := followCompactFixture(t)
	toggleFollowCell(t, m, "0:1")
	update := func(source string) {
		writeFixture(t, m.opts.Path, []byte(source))
		_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'r'}))
		applyWork(t, m, cmd)
	}
	update("|Name|Detail|\n|---|---|\n|same|" + (strings.Repeat("changed first ", 25) + strings.Repeat("x", 200)) + "|\n|same|" + (strings.Repeat("changed second ", 25) + strings.Repeat("x", 200)) + "|")
	if !m.view.compact[2] || m.view.compact[3] {
		t.Fatal("duplicate first cells shared or lost open state")
	}
	update("|Name|Detail|\n|---|---|\n|same|" + (strings.Repeat("only occurrence ", 25) + strings.Repeat("x", 200)) + "|")
	if len(m.openTables) != 0 || !m.view.compact[2] {
		t.Fatal("missing occurrence kept stale expansion")
	}
	toggleFollowCell(t, m, "0:0")
	update("|Name|Detail|Third|\n|---|---|---|\n|same|" + (strings.Repeat("changed schema ", 25) + strings.Repeat("x", 200)) + "|x|")
	if len(m.openTables) != 0 {
		t.Fatal("column change retained old key")
	}
}
func TestFollowTableToggleCancelsDragAndSourceStaysLiteral(t *testing.T) {
	m := followCompactFixture(t)
	x, y := followControl(t, m, "0:0")
	copied := ""
	m.opts.Copy = func(s string) tea.Cmd { copied = s; return nil }
	m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	applyWork(t, m, key(m, 'w', 0))
	m.Update(tea.MouseReleaseMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	if copied != "" || m.drag || m.selection != nil {
		t.Fatal("stale marker gesture copied")
	}
	applyWork(t, m, key(m, 's', tea.ModCtrl))
	if len(m.view.records) != 0 {
		t.Fatal("source acquired table controls")
	}
	if cmd := key(m, 'w', 0); cmd != nil {
		t.Fatal("source w changed layout")
	}
	applyWork(t, m, key(m, 's', tea.ModCtrl))
	if len(m.view.compact) != 0 {
		t.Fatal("source round trip lost open rows")
	}
}

func TestFollowWCollapsesPartiallyRefusedExpansion(t *testing.T) {
	source := "|Name|Stage|Owner|Updated|Note|\n|---|---|---|---|---|\n"
	for i := 0; i < 12; i++ {
		source += fmt.Sprintf("|row-%02d-abcdefghijkl|abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ|abcdefghijklmnop|abcdefghijklmnop|%s|\n", i, strings.Repeat("long note words ", 4000))
	}
	m, _ := fileModel(t, source, Markdown)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
	applyWork(t, m, cmd)
	applyWork(t, m, key(m, 'w', 0))
	if len(m.openTables) == 0 || len(m.view.refused) == 0 {
		t.Fatalf("fixture needs partial admission: open=%d refused=%d", len(m.openTables), len(m.view.refused))
	}
	if !strings.Contains(m.notice, "kept closed") {
		t.Fatal("missing refusal notice", m.notice)
	}
	applyWork(t, m, key(m, 'w', 0))
	if len(m.openTables) != 0 || len(m.view.compact) != 12 {
		t.Fatal("w did not close partially expanded table", len(m.openTables), len(m.view.compact))
	}
	applyWork(t, m, key(m, 'w', 0))
	if len(m.openTables) == 0 || len(m.view.refused) == 0 {
		t.Fatal("w could not expand again after collapse")
	}
}

func TestFollowBChoicesSurviveWrappedAResize(t *testing.T) {
	m := followCompactFixture(t)
	toggleFollowCell(t, m, "0:0")
	for _, open := range []bool{true, false} {
		if !open {
			toggleFollowCell(t, m, "0:0")
		}
		_, cmd := m.Update(tea.WindowSizeMsg{Width: 260, Height: 60})
		applyWork(t, m, cmd)
		if len(m.view.controls) != 0 || len(m.view.compact) != 0 {
			t.Fatal("A has controls/closed rows")
		}
		before := len(m.openTables)
		if cmd := m.toggleTableRows("", true); cmd != nil || len(m.openTables) != before {
			t.Fatal("A w changed dormant B choices")
		}
		_, cmd = m.Update(tea.WindowSizeMsg{Width: 4, Height: 60})
		applyWork(t, m, cmd)
		_, cmd = m.Update(tea.WindowSizeMsg{Width: 60, Height: 60})
		applyWork(t, m, cmd)
		if m.view.compact[2] == open {
			t.Fatal("B choice lost after A resize", open)
		}
	}
}
