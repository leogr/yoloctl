package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/followview"
	"github.com/leogr/yoloctl/internal/termio"
)

func TestReviewManualFollowReopenPreservesReaderMode(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.md")
	path := filepath.Join(root, "link.md")
	if err := os.WriteFile(real, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	if _, err := (followview.HostReader{Path: path}).Read(context.Background(), followview.Query{}); err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, 100, 26)
	s := paneSession(t, 10, 40, "sleep", "20")
	defer s.Close()
	tab := testTab(s, "yoloctl follow", false)
	tab.Focused().Args = []string{"follow", "--host", "--", path}
	m.tabs.Add(tab)
	c := filelink.Context{Mode: "host", Base: root, Root: root}
	r := termio.NavigationRegistration(c)
	r.FollowPath = path
	m.pinFollowSource(tabRequestMsg{from: s, navigation: &c, req: r})
	v, ok := parseReopenView(tab.Focused().Args)
	if !ok {
		t.Fatal(tab.Focused().Args)
	}
	if _, err := (followview.HostReader{Path: v.path, Root: v.root}).Read(context.Background(), followview.Query{}); err != nil {
		t.Fatalf("reopen changed manual reader: args=%v error=%v", tab.Focused().Args, err)
	}
}

func TestReviewLinkedFollowDoesNotReuseUnboundedManualReader(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	script := filepath.Join(t.TempDir(), "view")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return script, nil }
	m := newTestModel(t, 100, 26)
	s := paneSession(t, 10, 40, "sleep", "20")
	defer s.Close()
	tab := testTab(s, "yoloctl follow", false)
	tab.Focused().Args = []string{"follow", "--host", "--", "/p/a.md"}
	m.tabs.Add(tab)
	m.layoutTab(tab)
	c := filelink.Context{Mode: "host", Base: "/p", Root: "/p"}
	r := termio.NavigationRegistration(c)
	r.FollowPath = "/p/a.md"
	m.pinFollowSource(tabRequestMsg{from: s, navigation: &c, req: r})
	target := filelink.Target{Mode: "host", Path: "/p/a.md", Root: "/p"}
	m.handleFollowRequest(termio.FollowRequest(target), &c, s)
	if len(m.tabs.Tabs) != 2 {
		t.Fatalf("linked target reused manual reader without no-follow root; tabs=%d args=%v", len(m.tabs.Tabs), tab.Focused().Args)
	}
	defer m.tabs.Focused().Session.Close()
}
