package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/tui/tabs"
)

func TestPaneReopenKeepsActualLaunchTarget(t *testing.T) {
	for _, args := range [][]string{
		{"chat"}, {"space", "view"}, {"sp", "shell", "--root", "--"},
		{"box", "logs", "api", "-f"}, {"box", "exec", "api", "--", "/bin/bash", "-i"},
		{"attach", "api", "--new-session"},
		{"box", "attach", "api", "--chat-room-id", "r", "--chat-member-id", "m", "--chat-box-id", "b", "--chat-allow-start", "--passthrough"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("YOLOCTL_HOME", t.TempDir())
			t.Setenv(config.SpaceEnv, "")
			if err := (&config.Context{Space: "first"}).Save(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			capture := filepath.Join(dir, "args")
			script := filepath.Join(dir, "capture")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$YOLOCTL_TEST_CAPTURE_ARGV\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YOLOCTL_TEST_CAPTURE_ARGV", capture)
			oldExec := executable
			t.Cleanup(func() { executable = oldExec })
			executable = func() (string, error) { return script, nil }
			m := newTestModel(t, 100, 26)
			if err := m.initSpace(); err != nil {
				t.Fatal(err)
			}
			// Another TUI/global context change must not override this model's pin.
			if err := (&config.Context{Space: "outside"}).Save(); err != nil {
				t.Fatal(err)
			}
			p := m.startPane(args, 20, 100)
			if p == nil {
				t.Fatalf("start: %s", m.status)
			}
			t.Cleanup(p.Session.Close)
			if err := p.Session.Wait(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			recorded := slices.Clone(p.Args)
			actual, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(strings.Split(strings.TrimSuffix(string(actual), "\n"), "\n"), recorded) {
				t.Fatalf("child=%q recorded=%q", actual, recorded)
			}
			if !slices.Contains(recorded, "first") {
				t.Fatalf("implicit launch target was not pinned: %q", recorded)
			}
			tab := tabs.New(p)
			m.tabs.Add(tab)
			m.layoutTab(tab)
			m.focusPalette = false
			m.Update(tabExitMsg{session: p.Session})
			if err := (&config.Context{Space: "changed"}).Save(); err != nil {
				t.Fatal(err)
			}
			m.ctxSpace = "different-tui-selection"
			m.reattachPane(tab, p)
			next := tab.Focused()
			if next == p {
				t.Fatal("ended pane did not reopen")
			}
			t.Cleanup(next.Session.Close)
			if err := next.Session.Wait(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			want := recorded
			if attach, _, ok := reattachArgs(recorded); ok {
				want = attach
			}
			actual, err = os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(next.Args, want) || !slices.Equal(strings.Split(strings.TrimSuffix(string(actual), "\n"), "\n"), want) {
				t.Fatalf("reopen redirected or changed argv: child=%q stored=%q want=%q", actual, next.Args, want)
			}
		})
	}
}

func TestMalformedContextKeepsCLIErrorAndOffersNoUnboundReplay(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	ctx, err := config.LoadContext()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ctx.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctx.Path(), []byte("space: ["), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"chat"}, {"attach", "api"}} {
		got := pinReopenArgs(args, func() (string, error) { return reopenSpace(nil) })
		if !slices.Equal(got, args) {
			t.Fatal("context failure rewrote original error-producing argv")
		}
		p := endedAttach(t, "chat", "--space", "fixture")
		p.Args = got // initial context resolution failed before any target could be retained
		if _, ok := viewActionForPane(p); ok {
			t.Fatalf("unbound argv offered replay: %q", got)
		}
	}
}
