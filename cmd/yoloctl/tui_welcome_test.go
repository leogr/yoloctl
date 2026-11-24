package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/tui"
)

func TestWelcomeBoxStatus(t *testing.T) {
	box := func(name, id string, isOperator bool) controllerBoxView {
		cfg := &agentbox.Config{Name: name, ID: id}
		if isOperator {
			cfg.Capabilities = []string{operator.Capability}
		}
		return controllerBoxView{State: boxstate.Box{Declaration: boxstate.DeclarationDeclared}, Config: cfg}
	}
	server := box("chat", "server", false)
	lead := box("lead", "lead-id", true)
	worker := box("worker", "", false)
	hasOperator := []controllerBoxView{lead, server}
	ready := func(member managedchat.Member) managedchat.Status {
		return managedchat.Status{ServerReady: true, Room: &managedchat.Room{
			Server:  managedchat.Box{Name: "chat", ID: "server"},
			Members: []managedchat.Member{member},
		}}
	}
	enrolled := managedchat.Member{Name: "lead", BoxID: "lead-id"}
	for _, tc := range []struct {
		name     string
		boxes    []controllerBoxView
		chat     managedchat.Status
		chatErr  error
		readChat bool
		want     tui.WelcomeStatus
	}{
		{"operator before chat", []controllerBoxView{worker}, managedchat.Status{}, nil, false, tui.WelcomeStatus{Step: tui.WelcomeNoOperator}},
		{"unreadable declaration", []controllerBoxView{{State: boxstate.Box{Declaration: boxstate.DeclarationInvalid}}}, managedchat.Status{}, nil, false, tui.WelcomeStatus{}},
		{"chat unavailable", hasOperator, managedchat.Status{}, errors.New("unavailable"), true, tui.WelcomeStatus{}},
		{"no room", hasOperator, managedchat.Status{}, nil, true, tui.WelcomeStatus{Step: tui.WelcomeNoChat}},
		{"setup required", hasOperator, managedchat.Status{Room: &managedchat.Room{}, SetupRequired: true}, nil, true, tui.WelcomeStatus{Step: tui.WelcomeNoChat}},
		{"server uncertain", hasOperator, managedchat.Status{Room: &managedchat.Room{}}, nil, true, tui.WelcomeStatus{}},
		{"server changed between reads", []controllerBoxView{lead}, ready(enrolled), nil, true, tui.WelcomeStatus{}},
		{"same name different box", hasOperator, ready(managedchat.Member{Name: "lead", BoxID: "old-lead"}), nil, true, tui.WelcomeStatus{Step: tui.WelcomeEnroll, Operator: "lead"}},
		{"revoked", hasOperator, ready(managedchat.Member{Name: "lead", BoxID: "lead-id", Revoked: true}), nil, true, tui.WelcomeStatus{Step: tui.WelcomeEnroll, Operator: "lead"}},
		{"exclude chat server", hasOperator, ready(enrolled), nil, true, tui.WelcomeStatus{Step: tui.WelcomeOnlyOperator, Operator: "lead"}},
		{"all ready", []controllerBoxView{lead, server, worker}, ready(enrolled), nil, true, tui.WelcomeStatus{Step: tui.WelcomeReady}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			got := welcomeBoxStatus(tc.boxes, func() (managedchat.Status, error) {
				called = true
				return tc.chat, tc.chatErr
			})
			if got != tc.want || tc.readChat != called {
				t.Fatalf("welcomeBoxStatus = %+v (read chat %v), want %+v (read chat %v)", got, called, tc.want, tc.readChat)
			}
		})
	}
}

func TestWelcomeMissingSelectedSpace(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	if got := readWelcomeStatus(t.Context(), "work"); got.Step != tui.WelcomeNoSpace {
		t.Fatalf("empty inventory = %+v", got)
	}
	path, err := layout.SpaceConfigPath("other")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unreadable as a bootstrap"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readWelcomeStatus(t.Context(), "work"); got.Step != tui.WelcomeMissingSpace {
		t.Errorf("missing selection beside another space = %+v", got)
	}
	if got := readWelcomeStatus(t.Context(), "other"); got.Step != tui.WelcomeUnknown {
		t.Errorf("unreadable selection = %+v", got)
	}
	// A loop makes Stat fail with a non-absence error even when tests run as root.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		t.Fatal(err)
	}
	if got := readWelcomeStatus(t.Context(), "work"); got.Step != tui.WelcomeUnknown {
		t.Errorf("unreadable inventory = %+v, want generic hint", got)
	}
}

func TestTUICreatedSpace(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"space", "create", "work", "--workdir", "/p", "--account", "a"}, "work"},
		{[]string{"space", "create", "--workdir", "/p", "--account", "a", "work"}, "work"},
		{[]string{"sp", "create", "--workdir=/p", "--account=a", "work"}, "work"},
		{[]string{"space", "create", "--memory", "8GiB", "--no-start", "lab", "--workdir", "/p", "--account", "a"}, "lab"},
		{[]string{"space", "--log-level", "debug", "create", "work", "--workdir", "/p", "--account", "a"}, "work"},
		{[]string{"space", "create", "--", "work"}, "work"},
		{[]string{"space", "create", "--workdir", "/p", "--account", "a"}, ""}, // the TUI's own space
		{[]string{"space", "create", "work", "--help"}, ""},
		{[]string{"space", "create", "work", "-h"}, ""},
		{[]string{"space", "create", "work", "--bogus"}, ""},
		{[]string{"space", "create", "work", "other"}, ""},
		{[]string{"space", "start", "work"}, ""},
		{[]string{"space", "recreate", "work"}, ""},
		{[]string{"box", "create", "work", "--agent", "claude"}, ""},
		{[]string{"space"}, ""},
		{nil, ""},
	} {
		if got := tuiCreatedSpace(tc.args); got != tc.want {
			t.Errorf("tuiCreatedSpace(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
	// Lines share no flag values, also concurrently.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			args := []string{"space", "create", "work", "--help", "--no-start"}
			want := ""
			if i%2 == 1 {
				args, want = []string{"space", "create", "work", "--no-start"}, "work"
			}
			if got := tuiCreatedSpace(args); got != want {
				t.Errorf("tuiCreatedSpace(%q) = %q, want %q", args, got, want)
			}
		}()
	}
	wg.Wait()
}
