package tui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/term"
)

func TestFollowContextIsBoundToTrustedSourceLaunch(t *testing.T) {
	c := filelink.Context{Mode: "vm", Space: "dev", Base: "/p/docs", Root: "/p"}
	r := termio.NavigationRegistration(c)
	r.FollowPath = "/p/docs/a.md"
	r.FollowBounded = true
	args := []string{"follow", "--space", "dev", "--link-root=/p", "/p/docs/a.md"}
	if _, ok := navigationForSource(args, r); !ok {
		t.Fatal("valid source refused")
	}
	for _, mutate := range []func(*termio.TabRequest){func(r *termio.TabRequest) { r.Space = "other" }, func(r *termio.TabRequest) { r.FollowMode = "host"; r.Space = "" }, func(r *termio.TabRequest) { r.FollowRoot = "/" }, func(r *termio.TabRequest) { r.FollowPath = "/p/docs/b.md" }, func(r *termio.TabRequest) { r.Box = "box" }, func(r *termio.TabRequest) { r.ChatAllowStart = true }} {
		bad := r
		mutate(&bad)
		if _, ok := navigationForSource(args, bad); ok {
			t.Fatal("accepted changed context", bad)
		}
	}
	chat := termio.NavigationRegistration(filelink.Context{Mode: "vm", Space: "dev", Base: "/p", Root: "/p"})
	if _, ok := navigationForSource([]string{"chat", "--space", "dev"}, chat); !ok {
		t.Fatal("chat context")
	}
	if _, ok := navigationForSource([]string{"box", "attach", "a", "--space", "dev"}, chat); ok {
		t.Fatal("guest attachment received navigation authority")
	}
}
func TestFileRequestsAlwaysOpenNewTabAndReusePinnedIdentity(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	script := filepath.Join(t.TempDir(), "view")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return script, nil }
	m := newTestModel(t, 100, 26)
	source := paneSession(t, 10, 40, "sleep", "20")
	defer source.Close()
	home := testTab(source, "yoloctl chat --space dev", false)
	m.tabs.Add(home)
	m.layoutTab(home)
	if err := home.Layout.Split(true, &panes.Pane{}); err != nil {
		t.Fatal(err)
	}
	empty := home.Layout.Empty()
	if empty == nil {
		t.Fatal("need placeholder")
	}
	c := filelink.Context{Mode: "vm", Space: "dev", Base: "/p", Root: "/p"}
	target := filelink.Target{Mode: "vm", Space: "dev", Path: "/p/a.md", Root: "/p"}
	m.handleFollowRequest(termio.FollowRequest(target), &c, source)
	if len(m.tabs.Tabs) != 2 || home.Layout.Empty() != empty || empty.Session != nil {
		t.Fatal("file filled attach placeholder")
	}
	opened := m.tabs.Focused()
	defer opened.Session.Close()
	want := slices.Clone(opened.Args)
	m.handleFollowRequest(termio.FollowRequest(target), &c, source)
	if len(m.tabs.Tabs) != 2 {
		t.Fatal("identical live file was duplicated")
	}
	action, ok := viewEndAction(want)
	if !ok || !slices.Equal(action.args, want) {
		t.Fatal("reopen lost navigation boundary")
	}
	target.Root = "/"
	m.handleFollowRequest(termio.FollowRequest(target), &c, source)
	if len(m.tabs.Tabs) != 2 {
		t.Fatal("root widened")
	}
	m.handleFollowRequest(termio.FollowRequest(target), &c, new(term.Session))
	if len(m.tabs.Tabs) != 2 {
		t.Fatal("closed/unknown source opened")
	}
}
