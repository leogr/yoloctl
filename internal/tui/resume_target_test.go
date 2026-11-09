package tui

import (
	"os"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func TestLifecycleResumeChild(t *testing.T) {
	mode := os.Getenv("YOLOCTL_TEST_LIFECYCLE_TARGET")
	if mode == "" {
		t.Skip("child helper")
	}
	f, err := termio.InheritedTabRequests()
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		defer func() { _ = f.Close() }()
		if mode != "unpublished" {
			if err := termio.WriteTabRequest(f, termio.TabRequest{Op: "resume-target", Space: "resolved", Box: "copy-dst", NoAutoupdate: true, Passthrough: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if mode == "unpublished" || mode == "failed-attach" {
		os.Exit(17)
	}
}

func lifecycleTargetSession(t *testing.T, args []string, mode string) *term.Session {
	t.Helper()
	s, err := term.Start(term.Options{TrustedView: true, ResumeTargetKind: resumeTargetKind(args),
		Argv: []string{os.Args[0], "-test.run=^TestLifecycleResumeChild$"},
		Env:  append(os.Environ(), "YOLOCTL_TEST_LIFECYCLE_TARGET="+mode), Rows: 20, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle child did not finish")
	}
	return s
}

func TestLifecycleResumeInPlaceUsesResolvedTarget(t *testing.T) {
	for _, verb := range []string{"create", "copy", "start", "restart", "recreate"} {
		for _, action := range []string{"key", "mouse"} {
			t.Run(verb+"/"+action, func(t *testing.T) {
				args := []string{"box", verb, "copy-dst", "--no-autoupdate", "--passthrough"}
				if verb == "create" {
					args = append(args, "--agent", "none", "--account", "none", "--env", "EXAMPLE=value")
				}
				if verb == "recreate" {
					args = append(args, "--force")
				}
				if verb == "copy" {
					args = []string{"box", "copy", "source", "copy-dst", "--account", "old-account"}
				}
				m := newTestModel(t, 100, 30)
				s := lifecycleTargetSession(t, args, "failed-attach")
				p := &panes.Pane{Session: s, Args: args, Command: "display text is not a target"}
				tab := tabs.New(p)
				m.tabs.Add(tab)
				m.layoutTab(tab)
				m.focusPalette = false
				_, _ = m.Update(tabExitMsg{session: s})
				if !m.reattachable(tab, p) {
					t.Fatal("ended lifecycle attachment has no action")
				}
				want := []string{"box", "attach", "copy-dst", "--space", "resolved", "--no-autoupdate", "--passthrough"}
				if got, _, ok := reattachArgsForPane(p); !ok || !slices.Equal(got, want) {
					t.Fatalf("target=%q", got)
				}
				// A later context change cannot enter argv built from the retained target.
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				if err := (&config.Context{Space: "changed"}).Save(); err != nil {
					t.Fatal(err)
				}
				oldExecutable, starts := executable, 0
				t.Cleanup(func() { executable = oldExecutable })
				executable = func() (string, error) { starts++; return truePath(t), nil }
				slot := p.Rect()
				if action == "key" {
					_, _ = m.Update(key('r', 0, "r"))
				} else {
					slots := m.reattachSlots()
					if len(slots) != 1 {
						t.Fatalf("slots=%+v", slots)
					}
					x, y := slots[0].x+1, m.tabTop()+slots[0].y
					_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
					_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
				}
				next := tab.Focused()
				t.Cleanup(next.Session.Close)
				if starts != 1 || next == p || next.Rect() != slot || !slices.Equal(next.Args, want) {
					t.Fatalf("starts=%d args=%q", starts, next.Args)
				}
				// A stale exit/target belongs only to the replaced session.
				_, _ = m.Update(tabExitMsg{session: s})
				if next.Exited {
					t.Fatal("old completion ended replacement")
				}
			})
		}
	}
}

func TestLifecycleResumeRequiresPublishedTarget(t *testing.T) {
	for _, args := range [][]string{
		{"box", "create", "coder", "--no-attach"},
		{"box", "create", "coder", "--manifest-only"},
		{"box", "copy", "source", "copy-dst", "--help"},
		{"box", "restart", "coder", "--help"},
		{"box", "destroy", "coder"},
		{"box", "exec", "coder", "sh"},
	} {
		s := lifecycleTargetSession(t, args, "normal")
		if _, _, ok := reattachArgsForPane(&panes.Pane{Session: s, Args: args}); ok {
			t.Fatalf("excluded command resumed: %q", args)
		}
	}
	args := []string{"box", "start", "coder"}
	s := lifecycleTargetSession(t, args, "unpublished")
	if _, _, ok := reattachArgsForPane(&panes.Pane{Session: s, Args: args}); ok {
		t.Fatal("missing metadata invented an attachment target")
	}
	guards := []string{"attach", "chat", "--space", "guarded", "--chat-room-id", "room", "--chat-member-id", "member", "--chat-box-id", "box", "--new-session"}
	s = lifecycleTargetSession(t, args, "normal")
	if got, _, ok := reattachArgsForPane(&panes.Pane{Session: s, Args: guards}); !ok || !slices.Equal(got, guards[:len(guards)-1]) {
		t.Fatalf("metadata downgraded guarded attach: %q", got)
	}
}
