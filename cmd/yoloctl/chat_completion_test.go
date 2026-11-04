package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

// This uses the actual client Cobra __complete command and native completion
// worker with private declarations. Only controller transport/live principal
// verification are replaced; it never contacts this session's controller.
func TestChatCompletionThroughClientAndNativeWorker(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_CHAT_COMPLETION_NATIVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChatCompletionThroughClientAndNativeWorker$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_CHAT_COMPLETION_NATIVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("private native completion fixture: %v\n%s", err, out)
		}
		return
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	store, inventory := roomRuntimeFixture(t)
	if store.UID == 0 {
		store.UID = 1000
	}
	if store.GID == 0 {
		store.GID = 1000
	}
	for _, rel := range []string{"config/boxes", "config/accounts", ".controller"} {
		if err := os.MkdirAll(filepath.Join(store.Disk, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	room, err := ensureManagedRoom(store, chatRuntimeRequest{Human: "person"}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opbox", "other", "removed", "revoked", "newbox"} {
		inventory.Declarations[name] = agentbox.NewIdentity()
		if name != "newbox" {
			room, err = store.Enroll(managedchat.Box{Name: name, ID: inventory.Declarations[name]}, room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var revocable []string
	var removedID string
	for _, member := range room.Members {
		if member.Name == "revoked" {
			room, err = store.Revoke(member.ID, room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			// Revoke offers names; a replaced box's membership stays revocable.
			revocable = append(revocable, member.Name+"\t"+member.ID)
		}
		if member.Name == "removed" {
			removedID = member.ID
		}
	}
	slices.Sort(revocable)
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: store.DiskID, DiskUUID: "fixture", DiskMount: store.Disk,
		ConfigDir: filepath.Join(store.Disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(store.Disk, "host"),
		UID: store.UID, GID: store.GID, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	inventory.Declarations[room.Server.Name] = room.Server.ID
	inventory.Declarations["removed"] = agentbox.NewIdentity() // replacement cannot inherit participation
	for name, id := range inventory.Declarations {
		cfg := &agentbox.Config{Name: name, ID: id, Space: "work", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir(name)}
		if name == room.Server.Name {
			cfg.Features, cfg.SessionCommand = []string{"yolo.chat.skill"}, managedServerArgv()
		}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
	}
	previous := selectCompletionTarget
	t.Cleanup(func() { selectCompletionTarget = previous })
	for _, operator := range []bool{false, true} {
		t.Run(fmt.Sprintf("operator=%v", operator), func(t *testing.T) {
			var op *controller.OperatorClient
			principal := controller.Principal{}
			if operator {
				op = &controller.OperatorClient{Space: "work", Generation: "fixture"}
				principal = controller.Principal{Box: "caller", Incarnation: "fixture"}
			}
			stubOperatorClient(t, op)
			calls := 0
			selectCompletionTarget = func(ctx context.Context, cmd *cobra.Command) (controllerTarget, error) {
				space, err := selectedSpace(cmd)
				if operator {
					// Real operator target selection enforces fixed-space scope;
					// replace its Call before any socket can be dialed.
					target, selectErr := selectControllerTarget(ctx, cmd)
					space, err = target.Space, selectErr
				}
				if err != nil {
					return controllerTarget{}, err
				}
				return controllerTarget{Space: space, Operator: operator, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
					calls++
					if req.Method != "control.complete" || req.Space != "work" {
						t.Fatalf("completion escaped read-only selected scope: %+v", req)
					}
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 2*time.Second {
						t.Fatal("completion lost its bounded deadline")
					}
					return runControllerCompletionInto(ctx, principal, req, streams.Out)
				}}, nil
			}
			for _, test := range []struct {
				args []string
				want []string
			}{
				{[]string{"connect", "--space", "work", ""}, []string{"opbox", "other"}},
				{[]string{"connect", "--space", "work", "o"}, []string{"opbox", "other"}},
				{[]string{"connect", "--space", "work", "op"}, []string{"opbox"}},
				{[]string{"disconnect", "--space", "work", "o"}, []string{"opbox", "other"}},
				{[]string{"connect", "--space", "work", "removed"}, nil},
				{[]string{"connect", "--space", "work", "revoked"}, nil},
				{[]string{"connect", "--space", "work", "opbox", ""}, nil},
				{[]string{"enroll", "--space", "work", "new"}, []string{"newbox"}},
				{[]string{"enroll", "--space", "work", "chat"}, nil},
				{[]string{"revoke", "--space", "work", ""}, revocable},
				{[]string{"revoke", "--space", "work", "rev"}, nil},
				{[]string{"revoke", "--space", "work", removedID[:12]}, []string{removedID + "\tremoved"}},
				{[]string{"nickname", "--space", "work", ""}, nil},
				{[]string{"channel", "create", "--space", "work", ""}, nil},
				{[]string{"status", "--space", "work", ""}, nil},
				{[]string{"ensure", "--space", "work", ""}, nil},
			} {
				root := newClientRootCmd()
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(io.Discard)
				root.SetArgs(append([]string{"__complete", "chat"}, test.args...))
				if err := root.ExecuteContext(t.Context()); err != nil {
					t.Fatal(test.args, err)
				}
				want := strings.Join(append(append([]string{}, test.want...), fmt.Sprintf(":%d", cobra.ShellCompDirectiveNoFileComp)), "\n") + "\n"
				if out.String() != want {
					t.Fatalf("%v completion got %q, want %q", test.args, out.String(), want)
				}
			}
			if calls == 0 {
				t.Fatal("chat completion never reached its authorized worker")
			}
			if operator {
				root := newClientRootCmd()
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(io.Discard)
				root.SetArgs([]string{"__complete", "chat", "connect", "--space", "foreign", ""})
				before := calls
				if err := root.ExecuteContext(t.Context()); err != nil || calls != before || out.String() != ":4\n" {
					t.Fatalf("operator completed foreign space: %q %v", out.String(), err)
				}
			}
		})
	}
	for _, command := range []string{"chat nickname", "chat future-admin"} {
		payload, err := json.Marshal(completionRequest{Command: command})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runControllerCompletionInto(t.Context(), controller.Principal{Box: "caller", Incarnation: "fixture"}, controller.Request{Space: "work", Payload: payload}, &out); err == nil || out.Len() != 0 {
			t.Fatalf("native worker accepted unauthorized completion %s: %v", command, err)
		}
	}
	if after, err := store.ReadManifestSnapshot(); err != nil || after.Generation != room.Generation {
		t.Fatal("completion mutated the managed room", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := newChatCmd()
	cmd.SetContext(ctx)
	if got, directive := completeChatParticipantBoxes(cmd, nil, ""); len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatal("canceled native completion still returned candidates")
	}
	selectCompletionTarget = func(context.Context, *cobra.Command) (controllerTarget, error) {
		return controllerTarget{}, errors.New("controller unavailable")
	}
	stubOperatorClient(t, nil)
	root := newClientRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"__complete", "chat", "connect", "--space", "work", "o"})
	if err := root.ExecuteContext(t.Context()); err != nil || out.String() != ":4\n" {
		t.Fatal("offline completion used fallback declarations", out.String(), err)
	}
}
