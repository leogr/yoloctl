package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/spf13/cobra"
)

func swapBinarySeams(t *testing.T, tty, active bool, prepare func(context.Context, *container.Client) error) {
	t.Helper()
	oldTTY, oldActive, oldPrepare := stdinIsTerminalFn, holderUnitActiveFn, ensureVMBinaryFn
	stdinIsTerminalFn = func(io.Reader) bool { return tty }
	holderUnitActiveFn = func(context.Context, *container.Client, string) bool { return active }
	ensureVMBinaryFn = prepare
	t.Cleanup(func() { stdinIsTerminalFn, holderUnitActiveFn, ensureVMBinaryFn = oldTTY, oldActive, oldPrepare })
}

func TestSessionBinaryPreflightGates(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		tty, active, noAttach, replacing bool
		want                             int
	}{
		{"no attach", true, false, true, true, 0},
		{"nonterminal", false, false, false, true, 0},
		{"living holder", true, true, false, false, 0},
		{"new holder", true, false, false, false, 1},
		{"replacing living holder", true, true, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			swapBinarySeams(t, tc.tty, tc.active, func(context.Context, *container.Client) error {
				calls++
				return nil
			})
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			if err := prepareAutoAttach(cmd, "work", "coder", tc.noAttach, tc.replacing); err != nil {
				t.Fatal(err)
			}
			if calls != tc.want {
				t.Fatalf("binary preparation calls = %d, want %d", calls, tc.want)
			}
			if calls != 0 {
				if err := ensureSessionBinary(cmd.Context(), container.New("work")); err != nil || calls != 1 {
					t.Fatalf("same operation repeated preparation: %v, %d", err, calls)
				}
				if err := ensureSessionBinary(cmd.Context(), container.New("other")); err != nil || calls != 2 {
					t.Fatalf("preparation leaked to another space: %v, %d", err, calls)
				}
				if err := ensureSessionBinary(t.Context(), container.New("work")); err != nil || calls != 3 {
					t.Fatalf("preparation leaked to another operation: %v, %d", err, calls)
				}
			}
		})
	}
}

func TestLiveHolderDoesNotRequireHostBundle(t *testing.T) {
	swapBinarySeams(t, true, true, func(context.Context, *container.Client) error {
		t.Fatal("joining an existing holder must not prepare a binary")
		return nil
	})
	if _, err := ensureAgentSession(t.Context(), container.New("work"), "work", "coder", io.Discard, true, nil, sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
}

// Exercise the actual command paths: binary failure must precede declaration
// creation, image builds, runtime teardown/start and package maintenance.
func TestSessionBinaryFailurePrecedesBoxMutation(t *testing.T) {
	for _, args := range [][]string{
		{"box", "create", "fresh", "--agent", "none", "--workdir-mode", "ephemeral"},
		{"box", "copy", "existing", "fresh"},
		{"box", "start", "existing"},
		{"box", "restart", "existing"},
		{"box", "recreate", "existing", "--force"},
		{"box", "attach", "existing"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			home := fakeLima(t)
			createTestSpace(t, "work")
			cfg := &agentbox.Config{
				SchemaVersion: agentbox.SchemaVersion, Name: "existing", Space: "work",
				Agent: "none", Account: "none", Image: "yoloctl/agentbox:ubuntu-24.04",
				Workdir: agentbox.NewEphemeralWorkdir("existing"),
			}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(cfg.Path())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "calls"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			mutations := filepath.Join(home, "box-mutations")
			t.Setenv("FAKE_LIMA_BOX_MUTATIONS", mutations)
			unavailable := errors.New("test: missing or mismatched session executable")
			swapBinarySeams(t, true, false, func(context.Context, *container.Client) error { return unavailable })
			err = runSpaceCommand(t, newRootCmd(), append(args, "--space", "work", "--no-autoupdate")...)
			if !errors.Is(err, unavailable) {
				t.Fatalf("expected binary error before mutation, got %v", err)
			}
			if _, err := agentbox.Load("work", "fresh"); !os.IsNotExist(err) {
				t.Fatalf("created declaration before binary preflight: %v", err)
			}
			after, err := os.ReadFile(cfg.Path())
			if err != nil || string(before) != string(after) {
				t.Fatalf("existing declaration changed: %v", err)
			}
			if calls, err := os.ReadFile(mutations); !os.IsNotExist(err) {
				t.Fatalf("runtime mutated before binary check: %s (%v)", calls, err)
			}
		})
	}
}
