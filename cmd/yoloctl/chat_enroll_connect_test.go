package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

func enrollConnectFixture() managedchat.Status {
	member := managedchat.Member{Name: "peer", ID: managedchat.NewID(), BoxID: agentbox.NewIdentity()}
	return managedchat.Status{Room: &managedchat.Room{ID: managedchat.NewID(), Channel: "#space", Server: managedchat.Box{Name: "chat"}, Generation: 7, Members: []managedchat.Member{member}}}
}

// Keep actual public Cobra parsing, guards and command definitions; substitute
// only the typed RPC boundary. These tests do not operate a box or chat client.
func enrollConnectCLI(t *testing.T, args []string, call chatControllerCaller) (*bytes.Buffer, error) {
	t.Helper()
	root := newClientRootCmd()
	root.SilenceErrors, root.SilenceUsage = true, true
	cmd, _, err := root.Find([]string{"chat", "enroll"})
	if err != nil {
		t.Fatal(err)
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runChatEnrollCommand(cmd, args[0], call)
	}
	guardOperatorCommands(root)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"chat", "enroll"}, args...))
	err = root.ExecuteContext(t.Context())
	return &out, err
}

func TestChatEnrollConnectRejectsOptionsBeforeMutation(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{
		{"peer", "--client", "space"}, {"peer", "--connect=false", "--client", "space"},
		{"peer", "--connect", "--client="}, {"peer", "--connect", "--client", "../other"},
		{"peer", "--connect", "--client", "two names"}, {"peer", "--connect", "--client", "é"},
		{"peer", "--connect", "--client", strings.Repeat("x", 33)},
		{"../peer", "--connect"}, {"peer", "--connect", "--unknown"},
		{"peer", "--connect", "--generation", "invalid"}, {"peer", "--connect", "--recreate=invalid"},
		{"--connect"}, {"peer", "other", "--connect"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			_, err := enrollConnectCLI(t, args, func(context.Context, *cobra.Command, string, any, any) error {
				calls++
				return nil
			})
			if err == nil || calls != 0 {
				t.Fatal("invalid options reached a mutation", calls, err)
			}
		})
	}
}

func TestChatEnrollConnectSequencesExistingTypedOperations(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, client := range []string{"space", "-review"} {
		t.Run(client, func(t *testing.T) {
			var methods []string
			out, err := enrollConnectCLI(t, []string{"peer", "--connect", "--client=" + client, "--generation=7", "--recreate", "--json"}, func(_ context.Context, _ *cobra.Command, method string, input any, result any) error {
				methods = append(methods, method)
				switch method {
				case "chat.enroll":
					if input != (chatRuntimeRequest{Box: "peer", Generation: 7, Recreate: true}) {
						t.Fatal("enrollment payload changed", input)
					}
					*result.(*managedchat.Status) = enrollConnectFixture()
				case "chat.connect":
					if input != (chatParticipationRequest{Box: "peer", Client: client}) {
						t.Fatal("connect payload widened", input)
					}
					*result.(*chatParticipationResult) = chatParticipationResult{Box: "peer", Client: client, Action: "connect"}
				default:
					t.Fatal("unexpected controller method", method)
				}
				return nil
			})
			if err != nil || !reflect.DeepEqual(methods, []string{"chat.enroll", "chat.connect"}) {
				t.Fatal(methods, err)
			}
			var got chatEnrollConnectResult
			decoder := json.NewDecoder(out)
			if err := decoder.Decode(&got); err != nil || got.Enrollment.Room == nil || got.Connection == nil || got.Connection.Client != client || got.ConnectionError != "" {
				t.Fatal(got, err)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatal("--json emitted more than one result", err)
			}
		})
	}
}

func TestChatEnrollConnectFailureNeverConnectsOrReplays(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	failed := errors.New("enrollment application failed")
	for _, failure := range []error{failed, &controller.OutcomeUnknown{ID: "request", Cause: failed}} {
		calls := 0
		out, err := enrollConnectCLI(t, []string{"peer", "--connect"}, func(_ context.Context, _ *cobra.Command, method string, _, _ any) error {
			calls++
			if method != "chat.enroll" {
				t.Fatal("failed enrollment reached connect", method)
			}
			return failure
		})
		if !errors.Is(err, failure) || calls != 1 || out.Len() != 0 {
			t.Fatal("enrollment failure replayed or reported success", calls, err, out.String())
		}
	}
}

func TestChatEnrollConnectReportsPartialSuccessWithoutUndoOrRetry(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	failed := errors.New("connection refused")
	for _, failure := range []error{failed, &controller.OutcomeUnknown{ID: "connect-request", Cause: failed}} {
		for _, jsonOutput := range []bool{false, true} {
			var methods []string
			enrolled := false
			args := []string{"peer", "--connect"}
			if jsonOutput {
				args = append(args, "--json")
			}
			out, err := enrollConnectCLI(t, args, func(_ context.Context, _ *cobra.Command, method string, _ any, result any) error {
				methods = append(methods, method)
				if method == "chat.enroll" {
					enrolled = true
					*result.(*managedchat.Status) = enrollConnectFixture()
					return nil
				}
				return failure
			})
			if !errors.Is(err, failure) || !enrolled || !reflect.DeepEqual(methods, []string{"chat.enroll", "chat.connect"}) || !strings.Contains(err.Error(), "enrollment confirmed") || !strings.Contains(err.Error(), "enrollment is retained") {
				t.Fatal("partial success lost, rolled back or retried", methods, err)
			}
			if jsonOutput {
				var result chatEnrollConnectResult
				if json.Unmarshal(out.Bytes(), &result) != nil || result.Enrollment.Room == nil || result.Connection != nil || result.ConnectionError == "" {
					t.Fatal("JSON omitted known partial success", out.String())
				}
			} else if !strings.Contains(out.String(), "#space on chat") {
				t.Fatal("enrollment observation lost on connection failure", out.String())
			}
		}
	}
}

func TestChatEnrollConnectUsesOperatorRoutingAndSelfRecreateGuard(t *testing.T) {
	stubOperatorClient(t, &controller.OperatorClient{Space: "work", Generation: "current"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var methods []string
	_, err := enrollConnectCLI(t, []string{"peer", "--connect"}, func(ctx context.Context, cmd *cobra.Command, method string, _ any, result any) error {
		target, err := selectControllerTarget(ctx, cmd)
		if err != nil || !target.Operator || target.Space != "work" || !operatorChatMethod(method) {
			t.Fatal("shortcut bypassed same-space operator route", target, method, err)
		}
		methods = append(methods, method)
		if method == "chat.enroll" {
			*result.(*managedchat.Status) = enrollConnectFixture()
		} else {
			*result.(*chatParticipationResult) = chatParticipationResult{Box: "peer", Client: "space", Action: "connect"}
		}
		return nil
	})
	if err != nil || !reflect.DeepEqual(methods, []string{"chat.enroll", "chat.connect"}) {
		t.Fatal(methods, err)
	}
	calls := 0
	_, err = enrollConnectCLI(t, []string{"caller", "--connect", "--recreate"}, func(_ context.Context, _ *cobra.Command, method string, input any, _ any) error {
		calls++
		return validateOperatorChatRuntime(controller.Principal{Box: "caller", Incarnation: "live"}, method, input.(chatRuntimeRequest), nil)
	})
	if err == nil || !strings.Contains(err.Error(), "cannot recreate the calling operator") || calls != 1 {
		t.Fatal("self-recreate guard changed or reached connect", calls, err)
	}
}

func TestChatEnrollConnectRequiresActiveEnrollmentAcknowledgment(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, change := range []func(*managedchat.Status){
		func(s *managedchat.Status) { s.Room = nil },
		func(s *managedchat.Status) { s.Room.Members[0].Revoked = true },
		func(s *managedchat.Status) { s.Room.Members[0].Name = "other" },
		func(s *managedchat.Status) { s.Room.Members[0].BoxID = "" },
	} {
		calls := 0
		_, err := enrollConnectCLI(t, []string{"peer", "--connect"}, func(_ context.Context, _ *cobra.Command, _ string, _ any, result any) error {
			calls++
			s := enrollConnectFixture()
			change(&s)
			*result.(*managedchat.Status) = s
			return nil
		})
		if err == nil || calls != 1 {
			t.Fatal("invalid enrollment acknowledgment triggered connect", calls, err)
		}
	}
}

func TestChatEnrollConnectLeavesPlainEnrollmentOutputCompatible(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	calls := 0
	out, err := enrollConnectCLI(t, []string{"peer", "--json"}, func(_ context.Context, _ *cobra.Command, method string, _ any, result any) error {
		calls++
		if method != "chat.enroll" {
			t.Fatal(method)
		}
		*result.(*managedchat.Status) = enrollConnectFixture()
		return nil
	})
	var status managedchat.Status
	if err != nil || calls != 1 || json.Unmarshal(out.Bytes(), &status) != nil || status.Room == nil {
		t.Fatal("plain enroll changed shape or gained a connection", calls, err, out.String())
	}
}
