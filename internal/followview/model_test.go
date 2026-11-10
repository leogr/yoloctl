package followview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func applyWork(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing work command")
	}
	message := cmd()
	if _, ok := message.(resultMsg); !ok {
		t.Fatalf("unexpected work result %T", message)
	}
	_, next := m.Update(message)
	if next != nil {
		applyWork(t, m, next)
	}
}
func fileModel(t *testing.T, text string, format Format) (*Model, string) {
	t.Helper()
	path := filepath.Join(fixtureDir(t), "document")
	writeFixture(t, path, []byte(text))
	m := New(context.Background(), Options{Reader: HostReader{Path: path}, Path: path, Format: format})
	t.Cleanup(m.Close)
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	applyWork(t, m, cmd)
	return m, path
}
func key(m *Model, code rune, mod tea.KeyMod) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: code, Mod: mod}))
	return cmd
}

func TestFollowPauseRefreshRenameAndStaleRecovery(t *testing.T) {
	text := strings.Repeat("first line\n", 50) + "old tail"
	m, path := fileModel(t, text, Text)
	if !m.following || m.top != m.maxTop() {
		t.Fatal("not initially following end")
	}
	key(m, tea.KeyUp, 0)
	if m.following {
		t.Fatal("scroll did not pause follow")
	}
	top := m.top
	writeFixture(t, path, []byte(text+"\nnew tail"))
	_, cmd := m.Update(pollMsg{}) // Poll batches its next tick and work; invoke only its queued work via r below.
	if cmd == nil {
		t.Fatal("poll did not schedule")
	}
	// A work request returned from a poll is a batch with the timer first.
	batch := cmd().(tea.BatchMsg)
	for _, work := range batch[1:] {
		applyWork(t, m, work)
	}
	if m.top != top || m.following {
		t.Fatal("refresh moved a paused viewport", m.top, top)
	}
	applyWork(t, m, key(m, tea.KeyEnd, 0))
	if !m.following || m.top != m.maxTop() {
		t.Fatal("End did not resume")
	}
	saved := m.view
	replacement := filepath.Join(filepath.Dir(path), "save")
	writeFixture(t, replacement, []byte("replacement"))
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	applyWork(t, m, key(m, 'r', 0))
	if m.view == saved || m.view.text != "replacement" {
		t.Fatal("rename did not refresh display")
	}
	saved = m.view
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	applyWork(t, m, key(m, 'r', 0))
	if m.view != saved || m.stale == "" {
		t.Fatal("failed read erased good display")
	}
	writeFixture(t, path, nil)
	applyWork(t, m, key(m, 'r', 0))
	if m.stale != "" || m.snapshot.Size != 0 || len(m.raw) != 0 {
		t.Fatal("reappeared empty file not displayed")
	}
}

func TestCompleteMarkdownPastChatLimitAndLargeFallbackRestoration(t *testing.T) {
	line := "A Markdown document line with **strong** text and enough content for a realistic fixture.\n"
	text := strings.Repeat(line, WholeBytes/len(line)-1) + "\nLAST CONTENT MUST BE REACHABLE"
	m, path := fileModel(t, text, Markdown)
	if m.snapshot.Large || !m.view.markdown || len(m.view.rows) <= 16384 {
		t.Fatal("fixture did not exceed chat row bound", len(m.view.rows))
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "LAST CONTENT MUST BE REACHABLE") {
		t.Fatal("file remainder was discarded")
	}
	applyWork(t, m, key(m, 's', tea.ModCtrl))
	if !strings.Contains(m.view.doc.Lines[0].Text, "**strong**") {
		t.Fatal("source toggle did not restore literal markup")
	}
	writeFixture(t, path, []byte(strings.Repeat("line **literal**\n", WholeBytes/17+1000)))
	applyWork(t, m, key(m, 'r', 0))
	if !m.snapshot.Large || m.view.markdown || len(m.raw) > WindowBytes || !strings.Contains(ansi.Strip(m.View().Content), "large file") {
		t.Fatal("large Markdown did not visibly fall back", m.snapshot.Size)
	}
	writeFixture(t, path, []byte("# Small again"))
	applyWork(t, m, key(m, 'r', 0))
	if m.snapshot.Large || !m.view.markdown {
		t.Fatal("Markdown mode did not restore")
	}
}

func TestOlderWindowBrowsingRefreshesItsOwnBytes(t *testing.T) {
	text := strings.Repeat("0123456789 abcdefghijklmnopqrstuvwxyz\n", WholeBytes/36+10000)
	m, path := fileModel(t, text, Text)
	if !m.snapshot.Large || m.snapshot.Start == 0 {
		t.Fatal("not a window fixture")
	}
	oldStart := m.snapshot.Start
	applyWork(t, m, key(m, tea.KeyHome, 0))
	if m.snapshot.Start != 0 || m.following {
		t.Fatal("Home did not load first window")
	}
	data := []byte(text)
	copy(data[:6], "EDITED")
	writeFixture(t, path, data)
	applyWork(t, m, key(m, 'r', 0))
	if string(m.raw[:6]) != "EDITED" || m.snapshot.Start != 0 {
		t.Fatal("reading window went back to tail")
	}
	m.top = m.maxTop()
	cmd := m.scroll(1)
	applyWork(t, m, cmd)
	if m.snapshot.Start <= 0 {
		t.Fatal("next window not loaded")
	}
	m.top = 0
	applyWork(t, m, m.scroll(-1))
	if m.snapshot.Start != 0 {
		t.Fatal("previous window not loaded")
	}
	applyWork(t, m, key(m, tea.KeyEnd, 0))
	if m.snapshot.Start != oldStart || !m.following {
		t.Fatal("return to tail did not restore original region", m.snapshot.Start, oldStart)
	}
}

type readerFunc func(context.Context, Query) (Snapshot, error)

func (f readerFunc) Read(ctx context.Context, q Query) (Snapshot, error) { return f(ctx, q) }
func TestSlowReadDoesNotBlockInputAndCloseJoinsIt(t *testing.T) {
	entered := make(chan struct{})
	stopped := make(chan struct{})
	var reads atomic.Int32
	r := readerFunc(func(ctx context.Context, q Query) (Snapshot, error) {
		reads.Add(1)
		close(entered)
		<-ctx.Done()
		close(stopped)
		return Snapshot{}, ctx.Err()
	})
	m := New(context.Background(), Options{Reader: r, Path: "/pinned", Format: Text})
	defer m.Close()
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-entered
	// Both actions complete while the source still waits. They only coalesce
	// work; neither performs I/O on the input loop.
	if next := key(m, 'r', 0); next != nil {
		t.Fatal("second read launched")
	}
	key(m, 's', tea.ModCtrl)
	if reads.Load() != 1 {
		t.Fatal("parallel reads")
	}
	quit := key(m, 'q', 0)
	if quit == nil {
		t.Fatal("quit did not complete")
	}
	m.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("owned read not joined")
	}
	select {
	case result := <-done:
		if _, cmd := m.Update(result); cmd != nil {
			t.Fatal("closed view launched stale work")
		}
	case <-time.After(time.Second):
		t.Fatal("result goroutine survived")
	}
	if reads.Load() != 1 {
		t.Fatal("close replayed source request")
	}
}

func TestSelectionIsExplicitCopyAndControlsStayInert(t *testing.T) {
	m, _ := fileModel(t, "one\ttwo\n\x1b]52;c;ZXZpbA==\a\n# Heading", Markdown)
	var copied []string
	m.opts.Copy = func(text string) tea.Cmd { copied = append(copied, text); return nil }
	m.top = 0
	m.following = false
	m.Update(tea.MouseClickMsg(tea.Mouse{X: 0, Y: 1, Button: tea.MouseLeft}))
	m.Update(tea.MouseMotionMsg(tea.Mouse{X: 7, Y: 1, Button: tea.MouseLeft}))
	m.Update(tea.MouseReleaseMsg(tea.Mouse{X: 7, Y: 1, Button: tea.MouseLeft}))
	if len(copied) != 0 {
		t.Fatal("mouse release copied without an explicit action")
	}
	key(m, 'y', 0)
	if len(copied) != 1 || copied[0] != "one two" {
		t.Fatal("display copy changed", copied)
	}
	if strings.Contains(m.View().Content, "\x1b]52;") {
		t.Fatal("file executed a clipboard sequence")
	}
	m.selection = nil
	applyWork(t, m, key(m, 's', tea.ModCtrl))
	if !strings.Contains(m.view.doc.Lines[2].Text, "# Heading") {
		t.Fatal("literal source unavailable")
	}
}

func TestTinyViewsAndResizePreserveReadingAnchor(t *testing.T) {
	m, _ := fileModel(t, strings.Repeat("long source line **with formatting** to wrap on resize\n", 100), Markdown)
	key(m, tea.KeyUp, 0)
	m.top = 10
	m.captureAnchor()
	anchor := m.anchor
	_, oldWork := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	prior := m.view
	_, queued := m.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	if queued != nil {
		t.Fatal("parallel resize job")
	}
	_, currentWork := m.Update(oldWork())
	if m.view != prior {
		t.Fatal("stale resize replaced the current view")
	}
	applyWork(t, m, currentWork)
	if m.following || m.anchor != anchor {
		t.Fatal("resize lost reading anchor")
	}

	for _, size := range [][2]int{{1, 1}, {2, 2}, {20, 5}} {
		fresh := New(context.Background(), Options{Reader: HostReader{Path: m.opts.Path}, Path: m.opts.Path, Format: Markdown})
		_, work := fresh.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		applyWork(t, fresh, work)
		lines := strings.Split(fresh.View().Content, "\n")
		if len(lines) != size[1] {
			t.Fatal("view height", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) != size[0] {
				t.Fatal("view width", size, ansi.StringWidth(line))
			}
		}
		fresh.Close()
	}
}
