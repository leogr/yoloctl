package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func fakeLima(t *testing.T) string {
	t.Helper()
	shortYoloctlHome(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	home, err := layout.LimaHome()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIMA_HOME", home)
	fixture, err := filepath.Abs("../../internal/lima/testdata/blank.qcow2.gz")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_QCOW2_FIXTURE", fixture)
	script, err := os.ReadFile("testdata/fake_lima.py")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "limactl"), script, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home
}

func runSpaceCommand(t *testing.T, cmd *cobra.Command, args ...string) error {
	t.Helper()
	var out bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err != nil {
		t.Logf("command output: %s", out.String())
	}
	return err
}

func createTestSpace(t *testing.T, name string) *spaceconfig.Config {
	t.Helper()
	if err := runSpaceCommand(t, newSpaceCreateCmd(), name, "--workdir", t.TempDir(), "--account", "personal"); err != nil {
		t.Fatal(err)
	}
	cfg, err := spaceconfig.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSpaceCreateRequiresAccountBeforeAnyWrites(t *testing.T) {
	home := fakeLima(t)
	for _, name := range []string{"", "none", "../escape"} {
		args := []string{"fresh", "--workdir", filepath.Join(t.TempDir(), "uncreated")}
		if name != "" {
			args = append(args, "--account", name)
		}
		if err := runSpaceCommand(t, newSpaceCreateCmd(), args...); err == nil {
			t.Fatalf("accepted account %q", name)
		}
		if exists, _ := spaceconfig.Exists("fresh"); exists {
			t.Fatal("saved a refused declaration")
		}
	}
	if _, err := os.Stat(filepath.Join(home, "calls")); !os.IsNotExist(err) {
		t.Fatal("invalid creation reached Lima")
	}
}

func TestSpaceRecreatePreservesAccountDiskAndManifests(t *testing.T) {
	home := fakeLima(t)
	cfg := createTestSpace(t, "first")
	other := createTestSpace(t, "second")
	if cfg.AccountDiskID == other.AccountDiskID {
		t.Fatal("spaces share a disk identity")
	}
	if cfg.DefaultAccount != "personal" || cfg.AccountDiskUUID == "" {
		t.Fatalf("initial account not initialized: %+v", cfg)
	}
	acct, err := account.Load("first", "personal")
	if err != nil {
		t.Fatal(err)
	}
	acct.Secrets = map[string]string{"KEY": "preserved"}
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	box := &agentbox.Config{Name: "coder", Space: "first", Agent: "claude", Account: "personal", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("coder")}
	if err := box.Save(); err != nil {
		t.Fatal(err)
	}
	disk, _ := layout.AccountDiskDir(cfg.AccountDiskID)
	sentinel := filepath.Join(disk, "native-data")
	if err := os.WriteFile(sentinel, []byte("history-and-auth"), 0600); err != nil {
		t.Fatal(err)
	}
	instance, _ := vmhost.InstanceDir("first")
	if err := os.WriteFile(filepath.Join(instance, "disposable"), []byte("old-vm"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceCommand(t, newSpaceRecreateCmd(), "first", "--force"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(instance, "disposable")); !os.IsNotExist(err) {
		t.Fatal("system disk was not replaced")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "history-and-auth" {
		t.Fatal("native disk lost", err)
	}
	acct, err = account.Load("first", "personal")
	if err != nil || acct.Secrets["KEY"] != "preserved" {
		t.Fatal("account manifest lost", err)
	}
	if _, err := agentbox.Load("first", "coder"); err != nil {
		t.Fatal("box manifest lost", err)
	}
	yaml, err := os.ReadFile(filepath.Join(instance, "lima.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(yaml, []byte("format: false")) || bytes.Contains(yaml, []byte("format: true")) {
		t.Fatal("replacement retained format authority")
	}
	calls, _ := os.ReadFile(filepath.Join(home, "calls"))
	if strings.Count(string(calls), "disk create") != 2 || strings.Contains(string(calls), "disk delete") {
		t.Fatalf("recreate touched disk lifecycle: %s", calls)
	}
	if err := runSpaceCommand(t, newSpaceDestroyCmd(), "first", "--force"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Fatal("destroy retained account disk")
	}
	if ok, _ := spaceconfig.Exists("first"); ok {
		t.Fatal("destroy retained manifests")
	}
	if ok, _ := account.Exists("second", "personal"); !ok {
		t.Fatal("destroy crossed space boundary")
	}
	if _, err := os.Stat(cfg.DefaultWorkdir); err != nil {
		t.Fatal("destroy removed host project", err)
	}
}

func TestSpaceReplacementFailuresRetainRecoveryState(t *testing.T) {
	for _, scenario := range []string{"missing-disk", "foreign-owner", "delete-fails", "delete-lies", "start-fails", "disk-delete-lies"} {
		t.Run(scenario, func(t *testing.T) {
			home := fakeLima(t)
			cfg := createTestSpace(t, "first")
			disk, _ := layout.AccountDiskDir(cfg.AccountDiskID)
			cmd := newSpaceRecreateCmd()
			switch scenario {
			case "missing-disk":
				if err := os.RemoveAll(disk); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				t.Setenv("FAKE_DISK_OWNER", "yolo-another")
			case "delete-fails":
				t.Setenv("FAKE_LIMA_FAIL", "delete")
			case "delete-lies":
				t.Setenv("FAKE_KEEP_VM", "1")
			case "start-fails":
				t.Setenv("FAKE_LIMA_FAIL", "start")
			case "disk-delete-lies":
				t.Setenv("FAKE_KEEP_DISK", "1")
				cmd = newSpaceDestroyCmd()
			}
			if err := runSpaceCommand(t, cmd, "first", "--force"); err == nil {
				t.Fatal("failure was ignored")
			}
			if ok, _ := account.Exists("first", "personal"); !ok {
				t.Fatal("failure deleted account manifest")
			}
			if ok, _ := spaceconfig.Exists("first"); !ok {
				t.Fatal("failure deleted recovery declaration")
			}
			calls, _ := os.ReadFile(filepath.Join(home, "calls"))
			if scenario == "missing-disk" || scenario == "foreign-owner" {
				if strings.Contains(string(calls), "delete ") {
					t.Fatal("invalid disk was detected after VM deletion")
				}
			}
			if scenario != "missing-disk" {
				if _, err := os.Stat(disk); err != nil {
					t.Fatal("failure deleted native disk", err)
				}
			}
			if scenario == "start-fails" {
				t.Setenv("FAKE_LIMA_FAIL", "")
				if err := runSpaceCommand(t, newSpaceStartCmd(), "first"); err != nil {
					t.Fatal("replacement is not retryable", err)
				}
			}
		})
	}
}

func TestSpaceOperationLockPrecedesAccountAndVMWork(t *testing.T) {
	fakeLima(t)
	createTestSpace(t, "first")
	unlock, err := spacelock.Acquire(t.Context(), "first", true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	root := newRootCmd()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	root.SetContext(ctx)
	if err := runSpaceCommand(t, root, "account", "create", "blocked", "--space", "first"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("guard did not block mutation: %v", err)
	}
	if ok, _ := account.Exists("first", "blocked"); ok {
		t.Fatal("account written during replacement")
	}
	unlock()
	root = newRootCmd()
	if err := runSpaceCommand(t, root, "account", "create", "allowed", "--space", "first"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := account.Exists("first", "allowed"); !ok {
		t.Fatal("scoped account not written after release")
	}
}

func TestInitialGuestSetupFailureIsRecoverable(t *testing.T) {
	fakeLima(t)
	previous := accountStateOperation
	accountStateOperation = func(context.Context, string, accountstate.Request) (accountstate.Result, error) {
		return accountstate.Result{}, errors.New("initialization interrupted")
	}
	t.Cleanup(func() { accountStateOperation = previous })
	if err := runSpaceCommand(t, newSpaceCreateCmd(), "first", "--workdir", t.TempDir(), "--account", "personal"); err == nil {
		t.Fatal("incomplete creation succeeded")
	}
	cfg, err := spaceconfig.Load("first")
	if err != nil {
		t.Fatal("recovery declaration lost", err)
	}
	if cfg.AccountDiskUUID != "" {
		t.Fatal("recorded successful initialization after failure")
	}
	if ok, _ := account.Exists("first", "personal"); !ok {
		t.Fatal("initial account declaration lost")
	}
	accountStateOperation = previous
	if err := runSpaceCommand(t, newSpaceStartCmd(), "first"); err != nil {
		t.Fatal("retry failed", err)
	}
	cfg, err = spaceconfig.Load("first")
	if err != nil || cfg.AccountDiskUUID == "" {
		t.Fatal("retry did not record disk identity", err)
	}
	if newSpaceDestroyCmd().Flag("purge") != nil {
		t.Fatal("obsolete purge flag remains")
	}
}

func TestDiskAllocationFailureDoesNotCreateAnOwner(t *testing.T) {
	fakeLima(t)
	t.Setenv("FAKE_LIMA_FAIL", "disk create")
	if err := runSpaceCommand(t, newSpaceCreateCmd(), "first", "--workdir", t.TempDir(), "--account", "personal"); err == nil {
		t.Fatal("disk allocation failure ignored")
	}
	if ok, _ := spaceconfig.Exists("first"); ok {
		t.Fatal("failed allocation left a declaration owning an unknown disk")
	}
	if ok, _ := account.Exists("first", "personal"); ok {
		t.Fatal("failed allocation persisted an account")
	}
}
