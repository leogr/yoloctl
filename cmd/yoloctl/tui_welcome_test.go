package main

import (
	"errors"
	"os"
	"path/filepath"
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
	for _, name := range []string{"work", "other"} {
		if got := readWelcomeStatus(t.Context(), name); got.Step != tui.WelcomeUnknown {
			t.Errorf("uncertain selection %q = %+v", name, got)
		}
	}
}
