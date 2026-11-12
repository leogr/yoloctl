package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// mutationGates classifies every registered mutation by where its durable
// effect gate sits. A new mutating method fails this test until it is
// explicitly classified and instrumented.
var mutationGates = map[string]string{
	"audit.enable": "handler", "audit.disable": "handler",
	"audit.rules.import": "handler", "audit.rules.remove": "handler", "audit.rules.edit": "handler",
	// The space-operation wrapper gates after its native locks, before the
	// command and its deferred role synchronization.
	"box.create": "wrapper", "box.copy": "wrapper", "box.start": "wrapper", "box.stop": "wrapper",
	"box.restart": "wrapper", "box.destroy": "wrapper", "box.recreate": "wrapper", "box.reconcile": "wrapper",
	"box.set": "wrapper", "box.attach": "wrapper",
	"account.create": "wrapper", "account.destroy": "wrapper", "account.secret.set": "wrapper",
	"account.secret.unset": "wrapper", "account.observe": "wrapper",
	"port.forward": "wrapper", "port.unforward": "wrapper",
	"image.build": "wrapper", "image.pull": "wrapper", "image.refresh": "wrapper", "image.prune": "wrapper",
	"agent.update": "wrapper",
	// Special dispatchers gate after their own lock and validation.
	"box.edit.apply": "document", "account.edit.apply": "document", "account.auth.import": "document",
	"chat.state.ensure": "chat-state", "chat.state.enroll": "chat-state", "chat.state.revoke": "chat-state",
	// Handlers that already gated keep their placement, now with a footprint.
	"box.exec": "handler", "chat.artifact.upload": "handler", "chat.channel.create": "handler",
	"chat.channel.add": "handler", "chat.channel.remove": "handler", "chat.channel.delete": "handler",
	"chat.stream": "handler", "chat.ensure": "handler", "chat.enroll": "handler", "chat.revoke": "handler",
	"chat.connect": "handler", "chat.disconnect": "handler", "chat.nickname": "handler",
}

func TestEveryMutationIsGatedAndClassified(t *testing.T) {
	mutations := 0
	for method, op := range controllerOperations {
		site, classified := mutationGates[method]
		if !op.Mutates {
			if classified {
				t.Errorf("%s is classified but does not mutate", method)
			}
			continue
		}
		mutations++
		if !classified {
			t.Errorf("%s mutates but has no classified effect gate", method)
		}
		if !op.EffectGated || op.Footprint == 0 {
			t.Errorf("%s policy %+v lacks a gate or footprint", method, op)
		}
		// Dispatched methods never reach the Cobra wrapper.
		if site == "wrapper" && !wrapperGatesEffects(strings.ReplaceAll(method, ".", " ")) {
			t.Errorf("%s is classified as wrapper-gated but the wrapper does not gate it", method)
		}
	}
	if mutations != 46 || len(mutationGates) != 46 {
		t.Fatalf("%d mutations, %d classified", mutations, len(mutationGates))
	}
}

// Every wrapped mutation asks permission before its command or role
// synchronization runs; a refused gate leaves both untouched.
func TestWrappedMutationsGateBeforeAnyEffect(t *testing.T) {
	for method, site := range mutationGates {
		if site != "wrapper" {
			continue
		}
		t.Run(method, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			previousSync, previousResolver := syncNativeChatRolesFn, newBoxStateResolver
			t.Cleanup(func() { syncNativeChatRolesFn, newBoxStateResolver = previousSync, previousResolver })
			// No inventory: footprints that need one become the whole space.
			newBoxStateResolver = func() boxstate.Resolver {
				return boxstate.Resolver{ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
					return nil, errors.New("no runtime inventory in this test")
				}}
			}
			words := strings.Split(method, ".")
			for _, deny := range []bool{true, false} {
				var events []string
				syncNativeChatRolesFn = func(context.Context, string) error {
					events = append(events, "role-sync")
					return nil
				}
				root := &cobra.Command{Use: "yoloctl", SilenceUsage: true, SilenceErrors: true}
				parent := root
				for _, word := range words[:len(words)-1] {
					child := &cobra.Command{Use: word}
					parent.AddCommand(child)
					parent = child
				}
				leaf := &cobra.Command{Use: words[len(words)-1], RunE: func(*cobra.Command, []string) error {
					events = append(events, "effect")
					return nil
				}}
				leaf.Flags().String("space", "work", "")
				leaf.Flags().Bool(newSessionFlag, false, "")
				parent.AddCommand(leaf)
				registerSpaceOperations(root)
				refused := errors.New("refused before permission")
				ctx := context.WithValue(t.Context(), controllerRequestKey{}, "request-0123456789abcdef")
				ctx = controller.WithEffectGate(ctx, func(_ context.Context, intent controller.EffectIntent) error {
					events = append(events, "gate")
					if intent.Scope == nil {
						t.Error("wrapped mutation declared no footprint")
					}
					if deny {
						return refused
					}
					return nil
				})
				args := append(append([]string{}, words...), "target", "--space", "work")
				if method == "box.copy" || strings.HasPrefix(method, "port.") || method == "account.observe" || method == "account.secret.set" || method == "account.secret.unset" {
					args = append(append([]string{}, words...), "target", "second", "--space", "work")
				}
				root.SetArgs(args)
				err := root.ExecuteContext(ctx)
				if deny {
					if !errors.Is(err, refused) || !slices.Equal(events, []string{"gate"}) {
						t.Fatalf("refused gate: err=%v events=%v", err, events)
					}
					continue
				}
				if err != nil || len(events) < 2 || events[0] != "gate" || events[1] != "effect" {
					t.Fatalf("permitted gate: err=%v events=%v", err, events)
				}
			}
		})
	}
}

func TestFootprintsFailClosed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousRoles := chatRoleBoxes
	t.Cleanup(func() { chatRoleBoxes = previousRoles })
	chatRoleBoxes = func() (map[string]bool, error) { return map[string]bool{"chat-server": true}, nil }
	cmd := &cobra.Command{Use: "start"}
	cmd.Flags().String("account", "", "")
	cmd.SetContext(t.Context())
	for name, tc := range map[string]struct {
		path string
		args []string
		want []controller.Resource
	}{
		"missing positional box": {"box start", nil, nil},
		"invalid account":        {"account create", []string{"none"}, nil},
		"unknown path":           {"space start", []string{"work"}, nil},
		"image domain":           {"image pull", []string{"ref"}, []controller.Resource{imagesDomain}},
		"verified account":       {"account secret set", []string{"personal", "KEY"}, []controller.Resource{accountResource("personal")}},
	} {
		t.Run(name, func(t *testing.T) {
			intent := spaceOperationIntent(cmd, tc.path, "work", tc.args)
			if tc.want == nil {
				if intent.Scope == nil || !intent.Scope.Global {
					t.Fatalf("unresolvable footprint was bounded: %+v", intent.Scope)
				}
				return
			}
			if intent.Scope == nil || intent.Scope.Global || !slices.Equal(intent.Scope.Writes, tc.want) {
				t.Fatalf("footprint %+v", intent.Scope)
			}
		})
	}
	if got := spaceOperationIntent(cmd, "account secret set", "work", []string{"personal", "KEY"}); got.Verifier != controller.VerifierAccountDocument {
		t.Fatal("verified account write has no verifier", got)
	}
	// A chat role box changes the shared room itself.
	if got := chatIntent("", "chat-server"); !slices.Contains(got.Scope.Writes, chatDomain) || !slices.Contains(got.Scope.Writes, boxResource("chat-server")) {
		t.Fatal(got.Scope)
	}
	if got := chatIntent("", "../escape"); !got.Scope.Global {
		t.Fatal("invalid chat box name bounded the footprint", got.Scope)
	}
}

// documentRecorder captures registered postimages.
type documentRecorder struct {
	documents []controller.DocumentIntent
	refuse    error
}

func (r *documentRecorder) ExpandEffects(context.Context, []controller.Resource) error { return nil }
func (r *documentRecorder) CompleteEffects(context.Context, int) error                 { return nil }
func (r *documentRecorder) RecordDocument(_ context.Context, d controller.DocumentIntent) error {
	if r.refuse != nil {
		return r.refuse
	}
	r.documents = append(r.documents, d)
	return nil
}

func TestVerifiedAccountWriteRegistersItsExactPostimage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	recorder := &documentRecorder{}
	ctx := controller.WithEffectRecorder(t.Context(), recorder)
	c := account.New("work", "personal")
	c.SetSecret("API_KEY", "fixture-secret")
	if err := saveVerifiedAccount(ctx, c); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.documents) != 1 || string(recorder.documents[0].Data) != string(written) || recorder.documents[0].Name != "personal" || recorder.documents[0].Mode != 0o600 {
		t.Fatalf("registered %+v for %q", recorder.documents, written)
	}
	recorder.refuse = errors.New("receipt storage failed")
	c.SetSecret("API_KEY", "changed")
	if err := saveVerifiedAccount(ctx, c); !errors.Is(err, recorder.refuse) {
		t.Fatal("refused registration did not abort the write", err)
	}
	if after, _ := os.ReadFile(c.Path()); string(after) != string(written) {
		t.Fatal("a write ran without its registered postimage")
	}
}
