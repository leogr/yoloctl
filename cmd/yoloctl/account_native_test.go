package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/config"
)

// This fake exercises CLI orchestration without a VM or writes to guest paths.
// The privileged implementation has its own no-follow and disk-identity tests.
func fakeNativeAccounts(t *testing.T) func(string, string) string {
	t.Helper()
	root := t.TempDir()
	previous, ensure, stop := accountStateOperation, accountEnsureSpace, accountStopUsers
	t.Cleanup(func() { accountStateOperation, accountEnsureSpace, accountStopUsers = previous, ensure, stop })
	accountEnsureSpace = func(context.Context, string) error { return nil }
	accountStopUsers = func(context.Context, string, string, bool, io.Writer) error { return nil }
	native := func(name, agent string) string { return filepath.Join(root, name, agent) }
	accountStateOperation = func(_ context.Context, _ string, req accountstate.Request) (accountstate.Result, error) {
		var result accountstate.Result
		dir := native(req.Account, req.Agent)
		switch req.Action {
		case "list":
			result.Leaves = []string{}
			entries, err := os.ReadDir(dir)
			if os.IsNotExist(err) {
				return result, nil
			}
			if err != nil {
				return result, err
			}
			for _, e := range entries {
				if e.IsDir() {
					result.Leaves = append(result.Leaves, e.Name())
				}
			}
		case "remove":
			return result, os.RemoveAll(dir)
		case "mkdir":
			return result, os.MkdirAll(dir, 0700)
		case "write":
			dst := filepath.Join(dir, req.Path)
			if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
				return result, err
			}
			return result, os.WriteFile(dst, req.Data, 0600)
		case "exists":
			_, err := os.Stat(filepath.Join(dir, req.Path))
			result.Present = err == nil
			if err != nil && !os.IsNotExist(err) {
				return result, err
			}
		default:
			t.Errorf("unexpected guest operation: %s", req.Action)
		}
		return result, nil
	}
	return native
}

func TestNativeMutationFailuresKeepManifest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeNativeAccounts(t)
	mustCreate(t, "personal")
	accountStateOperation = func(context.Context, string, accountstate.Request) (accountstate.Result, error) {
		return accountstate.Result{}, fmt.Errorf("native mount unavailable")
	}
	if _, _, err := execAccount(t, newAccountDestroyCmd(), "", "personal", "--force"); err == nil {
		t.Fatal("failed native delete reported success")
	}
	if exists, _ := account.Exists("default", "personal"); !exists {
		t.Fatal("failed native removal deleted recovery manifest")
	}
	out, _, err := execAccount(t, newAccountShowCmd(), "", "personal", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		StateAvailable bool  `json:"state_available"`
		StateLeaves    []any `json:"state_leaves"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if view.StateAvailable || view.StateLeaves == nil {
		t.Fatalf("unavailable storage misreported: %s", out)
	}
	out, _, err = execAccount(t, newAccountAuthStatusCmd(), "", "personal", "codex")
	if err == nil || !strings.Contains(out, "unavailable") {
		t.Fatalf("offline status = %q, %v", out, err)
	}
}

func TestAccountContextAndExplicitSpaceRemainIndependent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "first", "/work")
	saveSpaceForTest(t, "second", "/work")
	current, err := config.LoadContext()
	if err != nil {
		t.Fatal(err)
	}
	current.Space = "first"
	if err := current.Save(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"account", "create", "personal"},
		{"account", "create", "personal", "--space", "second"},
		{"account", "secret", "set", "personal", "KEY", "--from-env=YOLOCTL_TEST_SECRET", "--space", "second"},
	} {
		t.Setenv("YOLOCTL_TEST_SECRET", "second-only")
		if err := runSpaceCommand(t, newRootCmd(), args...); err != nil {
			t.Fatal(err)
		}
	}
	first, err := account.Load("first", "personal")
	if err != nil {
		t.Fatal(err)
	}
	second, err := account.Load("second", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Secrets) != 0 || second.Secrets["KEY"] != "second-only" {
		t.Fatal("space override leaked across accounts")
	}
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"account", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags([]string{"--space", "second"}); err != nil {
		t.Fatal(err)
	}
	names, _ := completeAccountNames(cmd, nil, "")
	if len(names) != 1 || names[0] != "personal" {
		t.Fatalf("scoped completion: %v", names)
	}
	if err := account.New("first", "first-only").Save(); err != nil {
		t.Fatal(err)
	}
	names, _ = completeAccountNames(cmd, nil, "")
	if len(names) != 1 {
		t.Fatalf("completion crossed space boundary: %v", names)
	}
}

func TestNativeImportRefusesRunningConsumersBeforeWriting(t *testing.T) {
	for _, agent := range []string{"agy", "claude", "codex", "gemini", "opencode", "pi"} {
		t.Run(agent, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			native := fakeNativeAccounts(t)
			mustCreate(t, "personal")
			accountStopUsers = func(context.Context, string, string, bool, io.Writer) error { return fmt.Errorf("running consumer") }
			source := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(source, []byte("synthetic"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", agent, "--file", source); err == nil || !strings.Contains(err.Error(), "running consumer") {
				t.Fatalf("active import: %v", err)
			}
			if _, err := os.Stat(native("personal", agent)); !os.IsNotExist(err) {
				t.Fatal("refused import wrote native state")
			}
		})
	}
}
