package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func participationFixture() (*managedchat.Room, []boxstate.Box) {
	m := managedchat.Member{ID: managedchat.NewID(), BoxID: agentbox.NewIdentity(), Name: "peer"}
	r := &managedchat.Room{ID: managedchat.NewID(), Prepared: true, RuntimeEnabled: true, Server: managedchat.Box{Name: "chat"}, Members: []managedchat.Member{m}}
	boxes := []boxstate.Box{{Name: m.Name, Declaration: boxstate.DeclarationDeclared, Runtime: boxstate.RuntimeRunning,
		Config: &agentbox.Config{Name: m.Name, ID: m.BoxID, Space: "work"}, Container: &container.Container{Name: m.Name}}}
	return r, boxes
}

func TestChatParticipationRequiresCurrentEnrolledRuntime(t *testing.T) {
	r, boxes := participationFixture()
	if cfg, member, err := resolveChatParticipant(r, boxes, "peer"); err != nil || cfg.ID != member.BoxID || member.ID != r.Members[0].ID {
		t.Fatal(cfg, member, err)
	}
	for name, change := range map[string]func(*managedchat.Room, []boxstate.Box){
		"unprepared":  func(r *managedchat.Room, _ []boxstate.Box) { r.Prepared = false },
		"dormant":     func(r *managedchat.Room, _ []boxstate.Box) { r.RuntimeEnabled = false },
		"revoked":     func(r *managedchat.Room, _ []boxstate.Box) { r.Members[0].Revoked = true },
		"reused-name": func(_ *managedchat.Room, b []boxstate.Box) { b[0].Config.ID = agentbox.NewIdentity() },
		"unknown":     func(_ *managedchat.Room, b []boxstate.Box) { b[0].Runtime = boxstate.RuntimeUnknown },
		"stopped":     func(_ *managedchat.Room, b []boxstate.Box) { b[0].Runtime = boxstate.RuntimeStopped },
		"missing": func(_ *managedchat.Room, b []boxstate.Box) {
			b[0].Runtime = boxstate.RuntimeMissing
			b[0].Container = nil
		},
		"invalid":      func(_ *managedchat.Room, b []boxstate.Box) { b[0].Declaration = boxstate.DeclarationInvalid },
		"no-container": func(_ *managedchat.Room, b []boxstate.Box) { b[0].Container = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r, boxes := participationFixture()
			change(r, boxes)
			if _, _, err := resolveChatParticipant(r, boxes, "peer"); err == nil {
				t.Fatal("unsafe participant accepted")
			}
		})
	}
	for _, name := range []string{"chat", "absent"} {
		if _, _, err := resolveChatParticipant(r, boxes, name); err == nil {
			t.Fatal("server or absent participant accepted", name)
		}
	}
}

func TestChatParticipationConfinedArgumentsAndSeparateDiagnostics(t *testing.T) {
	for _, action := range []string{"connect", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			cfg := &agentbox.Config{Name: "peer"}
			result := chatParticipationResult{Box: cfg.Name, Client: "-space", Action: action}
			var diagnostics bytes.Buffer
			var calls [][]string
			exec := func(_ context.Context, got *agentbox.Config, argv []string, in *os.File, out, notice io.Writer) error {
				if got != cfg || in != nil {
					t.Fatal("participant or stdin substituted")
				}
				calls = append(calls, argv)
				if argv[1] == "features" {
					_, err := io.WriteString(out, `{"protocol":1,"features":["client-unix","named-multiline","client-presence"]}`)
					return err
				}
				if _, err := io.WriteString(out, "AIRC acknowledgment\n"); err != nil {
					return err
				}
				_, err := io.WriteString(notice, "AIRC diagnostic\n")
				return err
			}
			if err := executeChatParticipation(t.Context(), cfg, result, exec, &diagnostics); err != nil {
				t.Fatal(err)
			}
			want := []string{"/usr/local/bin/airc", action}
			if action == "connect" {
				want = append(want, "--config", managedchat.IdentityMount+"/client.toml")
			}
			want = append(want, "--", result.Client)
			if len(calls) != 2 || !reflect.DeepEqual(calls[1], want) || !strings.Contains(diagnostics.String(), "acknowledgment") || !strings.Contains(diagnostics.String(), "diagnostic") {
				t.Fatal(calls, diagnostics.String())
			}
		})
	}
}

func TestChatParticipationFailuresDoNotReplay(t *testing.T) {
	for _, failure := range []string{"probe", "probe-notice", "unsupported", "action", "oversize", "notice"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			exec := func(_ context.Context, _ *agentbox.Config, argv []string, _ *os.File, out, notice io.Writer) error {
				calls++
				if argv[1] == "features" {
					if failure == "probe" {
						return errors.New("probe failed")
					}
					if failure == "probe-notice" {
						_, err := io.WriteString(notice, strings.Repeat("x", managedchat.ControlLimit+1))
						return err
					}
					if failure == "unsupported" {
						_, err := io.WriteString(out, `{"protocol":1,"features":[]}`)
						return err
					}
					_, err := io.WriteString(out, `{"protocol":1,"features":["client-unix","named-multiline","client-presence"]}`)
					return err
				}
				if failure == "oversize" {
					_, err := io.WriteString(out, strings.Repeat("x", managedchat.ControlLimit+1))
					return err
				}
				if failure == "notice" {
					_, err := io.WriteString(notice, strings.Repeat("x", managedchat.ControlLimit+1))
					return err
				}
				return errors.New("reply lost")
			}
			result := chatParticipationResult{Box: "peer", Client: "space", Action: "connect"}
			if err := executeChatParticipation(t.Context(), &agentbox.Config{Name: "peer"}, result, exec, io.Discard); err == nil {
				t.Fatal("failure claimed success")
			}
			want := 2
			if failure == "probe" || failure == "probe-notice" || failure == "unsupported" {
				want = 1
			}
			if calls != want {
				t.Fatal("action was replayed", calls)
			}
		})
	}
}

func TestChatParticipationRejectsUntypedOrUnsafeRequestsBeforeRuntime(t *testing.T) {
	for _, name := range []string{"", "../peer", "space/name", "two names", "$(read-secret)", strings.Repeat("x", 33), "é"} {
		if validateChatParticipationRequest(chatParticipationRequest{Box: "peer", Client: name}) == nil {
			t.Fatal("invalid named client accepted", name)
		}
	}
	for _, req := range []controller.Request{
		{Args: []string{"peer"}}, {Flags: map[string][]string{"config": {"/private"}}}, {Terminal: true}, {View: true},
		{Payload: []byte(`{"box":"peer","client":"space","config":"/private"}`)},
		{Payload: []byte(`{"box":"peer","client":"space"} {}`)},
	} {
		req.Method = "chat.connect"
		var out bytes.Buffer
		if handled, err := runControllerChatParticipation(t.Context(), req, &out, io.Discard); !handled || err == nil || out.Len() != 0 {
			t.Fatal("unsafe request reached runtime or emitted success", handled, err, out.String())
		}
	}
}
