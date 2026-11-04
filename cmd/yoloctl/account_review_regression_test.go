package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestSpaceAccountsCreateMissingAccountHint(t *testing.T) {
	home := fakeLima(t)
	createTestSpace(t, "work")
	if err := os.WriteFile(filepath.Join(home, "calls"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	createErr := runSpaceCommand(t, newRootCmd(), "box", "create", "foo", "--space", "work", "--agent", "claude", "--account", "typo", "--no-attach")
	if createErr == nil || !strings.Contains(createErr.Error(), "create it first") || strings.Contains(createErr.Error(), "box edit") {
		t.Fatalf("create: %v", createErr)
	}
	calls, err := os.ReadFile(filepath.Join(home, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("missing account reached VM: %s", calls)
	}
	cfg := &agentbox.Config{Name: "foo", Space: "work", Agent: "claude", Account: "typo", Workdir: agentbox.NewEphemeralWorkdir("foo")}
	err = preflightAgentboxRuntime(t.Context(), container.New("work"), cfg, false)
	if err == nil || strings.Contains(err.Error(), "box edit foo") || !strings.Contains(err.Error(), "create it first") {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Log(err)
}

func TestSpaceAccountsAlternateAccountDiskBindRejected(t *testing.T) {
	fakeLima(t)
	sc := createTestSpace(t, "work")
	root := layout.AccountDiskMount(sc.AccountDiskID)
	for _, source := range []string{"/", "/mnt", root, root + "/account-state/other/claude"} {
		if err := rejectSpaceHostPath(source, sc); err == nil {
			t.Fatalf("accepted grant %s", source)
		}
		cfg := &agentbox.Config{Name: "foo", Space: "work", Agent: "none", Account: "none", Workdir: agentbox.NewEphemeralWorkdir("foo"), Mounts: []agentbox.Mount{{Source: source, Target: "/x"}}}
		if err := rejectAccountRootBinds(cfg); err == nil {
			t.Fatalf("accepted bind %s", source)
		}
		cfg.Mounts = nil
		cfg.Workdir = agentbox.Workdir{Mode: agentbox.WorkdirModeHost, Source: source, Canonical: source}
		if err := rejectAccountRootBinds(cfg); err == nil {
			t.Fatalf("accepted workdir %s", source)
		}
	}
	if err := rejectSpaceHostPath(root+"-project", sc); err != nil {
		t.Fatalf("rejected sibling: %v", err)
	}
	if err := runSpaceCommand(t, newSpaceMountAddCmd(), "work", "/mnt", "--yes"); err == nil {
		t.Fatal("grant command accepted /mnt")
	}
}

func TestSpaceAccountsCreateContextDefaultAccount(t *testing.T) {
	fakeLima(t)
	work := createTestSpace(t, "work")
	work.DefaultAccount = "context-account"
	if err := work.Save(); err != nil {
		t.Fatal(err)
	}
	current, err := config.LoadContext()
	if err != nil {
		t.Fatal(err)
	}
	current.Space = "work"
	if err := current.Save(); err != nil {
		t.Fatal(err)
	}
	for _, defaultExists := range []bool{false, true} {
		if defaultExists {
			def := createTestSpace(t, "default")
			def.DefaultAccount = "wrong"
			if err := def.Save(); err != nil {
				t.Fatal(err)
			}
		}
		for _, prefix := range [][]string{{"box", "create", "trial"}} {
			args := append(prefix, "--agent", "claude", "--no-autoupdate")
			err = runSpaceCommand(t, newRootCmd(), args...)
			if err == nil || !strings.Contains(err.Error(), `"context-account"`) || strings.Contains(err.Error(), `"wrong"`) || strings.Contains(err.Error(), "spaces/default") {
				t.Fatalf("defaultExists=%v args=%v: %v", defaultExists, args, err)
			}
		}
	}
}

func TestSpaceAccountsInitialStartRetryFormatsOnlyBlankDisk(t *testing.T) {
	for _, format := range []string{"raw", "qcow2"} {
		for _, failure := range []string{"create", "start"} {
			t.Run(format+"/"+failure, func(t *testing.T) {
				fakeLima(t)
				t.Setenv("FAKE_DISK_FORMAT", format)
				t.Setenv("FAKE_LIMA_FAIL", failure)
				if err := runSpaceCommand(t, newSpaceCreateCmd(), "work", "--workdir", t.TempDir(), "--account", "personal"); err == nil {
					t.Fatal("expected initial failure")
				}
				cfg, err := spaceconfig.Load("work")
				if err != nil {
					t.Fatal(err)
				}
				if !cfg.AccountDiskInitializing || cfg.AccountDiskUUID != "" {
					t.Fatalf("lost initialization authority: %+v", cfg)
				}
				t.Setenv("FAKE_LIMA_FAIL", "")
				if err := ensureSpaceRunning(t.Context(), "work"); err != nil {
					t.Fatal(err)
				}
				cfg, err = spaceconfig.Load("work")
				if err != nil {
					t.Fatal(err)
				}
				if cfg.AccountDiskInitializing || cfg.AccountDiskUUID == "" {
					t.Fatal("initialization not committed")
				}
				p, _ := vmhost.InstanceConfigPath("work")
				raw, _ := os.ReadFile(p)
				if !bytes.Contains(raw, []byte("format: false")) {
					t.Fatal("format authority not revoked")
				}
			})
		}
	}
}

func TestSpaceAccountsRunningEnsureOnlyVerifiesStorage(t *testing.T) {
	home := fakeLima(t)
	createTestSpace(t, "work")
	for _, name := range []string{"two", "three"} {
		if err := account.New("work", name).Save(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "calls"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureSpaceRunning(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "calls"))
	if strings.Count(string(raw), "shell ") != 1 {
		t.Fatalf("calls: %s", raw)
	}
	t.Logf("already-running space with 3 accounts: %s", raw)
}

func TestSpaceAccountsGenericShellDoesNotBlockAccounts(t *testing.T) {
	fakeLima(t)
	createTestSpace(t, "work")
	binary, err := exec.LookPath("limactl")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(binary)
	replacement := "elif args[0] == 'shell':\n    if 'ps' in args and 'nerdctl' in args:\n        print(json.dumps(dict(Names='shellbox', Status='Up 1 minute', Labels='yoloctl.agent=none,yoloctl.agentbox=shellbox,yoloctl.space=work')))\n        sys.exit(0)"
	raw = []byte(strings.Replace(string(raw), "elif args[0] == 'shell':", replacement, 1))
	if err := os.WriteFile(binary, raw, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{Name: "shellbox", Space: "work", Agent: "none", Account: "none", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir("shellbox")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		err := stopAccountUsers(t.Context(), "work", "personal", force, io.Discard)
		if err != nil {
			t.Fatalf("force %v: %v", force, err)
		}
		t.Logf("force=%v: %v", force, err)
	}
}

func TestSpaceAccountsPortListIgnoresSpaceLock(t *testing.T) {
	fakeLima(t)
	createTestSpace(t, "work")
	release, err := spacelock.Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	root := newRootCmd()
	root.SetContext(ctx)
	err = runSpaceCommand(t, root, "port", "list", "--space", "work")
	if err != nil {
		t.Fatalf("port list blocked: %v", err)
	}

}

func TestSpaceAccountsSpaceListOmitsAccountFields(t *testing.T) {
	var out bytes.Buffer
	if err := writeInstancesJSON(&out, []vmhost.Instance{{Name: "work", Status: "Running"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "default_account") || strings.Contains(out.String(), "account_disk") {
		t.Fatal(out.String())
	}
	t.Log(out.String())
}

func TestSpaceAccountsLegacyAndOrphanDestroyErrors(t *testing.T) {
	home := fakeLima(t)
	p, _ := layout.SpaceConfigPath("old")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("schema_version: 1\nname: old\ndefault_workdir: /work\nmounts: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runSpaceCommand(t, newSpaceDestroyCmd(), "old", "--force")
	if err == nil || !strings.Contains(err.Error(), "schema_version 1 not supported") || !strings.Contains(err.Error(), "space create") {
		t.Fatal(err)
	}
	t.Logf("old destroy: %v", err)
	err = runSpaceCommand(t, newSpaceCreateCmd(), "old", "--workdir", t.TempDir(), "--account", "personal")
	if err == nil || !strings.Contains(err.Error(), "previous yoloctl version") {
		t.Fatal(err)
	}
	t.Logf("old create: %v", err)
	dir := filepath.Join(home, "yolo-orphan")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte("Stopped"), 0600); err != nil {
		t.Fatal(err)
	}
	err = runSpaceCommand(t, newSpaceDestroyCmd(), "orphan", "--force")
	if err == nil || !strings.Contains(err.Error(), "has no declaration") {
		t.Fatal(err)
	}
	t.Logf("orphan destroy: %v", err)
}

func TestSpaceAccountsStatelessAgentSkipsNativeLeaf(t *testing.T) {
	fakeLima(t)
	createTestSpace(t, "work")
	profile := &agentprofile.Profile{Name: "stateless", Command: "true", Image: "yoloctl/agentbox:ubuntu-24.04"}
	if err := profile.Save(); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{Name: "foo", Space: "work", Agent: "stateless", Account: "personal", Workdir: agentbox.NewEphemeralWorkdir("foo")}
	var reqs []accountstate.Request
	prev := accountStateOperation
	t.Cleanup(func() { accountStateOperation = prev })
	accountStateOperation = func(_ context.Context, _ string, r accountstate.Request) (accountstate.Result, error) {
		reqs = append(reqs, r)
		return accountstate.Result{}, nil
	}
	if err := preflightAgentboxRuntime(t.Context(), container.New("work"), cfg, false); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 0 {
		t.Fatalf("requests: %+v", reqs)
	}

}

func TestSpaceAccountsTeardownCleansMissingAndStoppedWithoutStop(t *testing.T) {
	fakeLima(t)
	createTestSpace(t, "work")
	binary, _ := exec.LookPath("limactl")
	raw, _ := os.ReadFile(binary)
	replacement := "elif args[0] == 'shell':\n    if 'ps' in args and 'nerdctl' in args:\n        print(json.dumps(dict(Names='stopped', Status='Exited (0)', Labels='yoloctl.agent=none')))\n        sys.exit(0)"
	if err := os.WriteFile(binary, []byte(strings.Replace(string(raw), "elif args[0] == 'shell':", replacement, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "stopped"} {
		cfg := &agentbox.Config{Name: name, Space: "work", Agent: "none", Account: "none", Image: "img:1", Workdir: agentbox.NewEphemeralWorkdir(name)}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
	}
	stop, reap := lifecycleStopAgentboxContainer, lifecycleReapOrphanedNetns
	t.Cleanup(func() { lifecycleStopAgentboxContainer = stop; lifecycleReapOrphanedNetns = reap })
	var stopped []string
	reaps := 0
	lifecycleStopAgentboxContainer = func(_ context.Context, _ *container.Client, name string) error {
		stopped = append(stopped, name)
		if name == "missing" {
			return errors.New("No such container")
		}
		return nil
	}
	lifecycleReapOrphanedNetns = func(context.Context, string, io.Writer) error { reaps++; return nil }
	if err := stopSpaceRuntime(t.Context(), "work", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 0 || reaps != 1 {
		t.Fatalf("stops=%v reaps=%d", stopped, reaps)
	}

}

func TestSpaceAccountsUnknownRuntimeStillBlocksNativeMutation(t *testing.T) {
	fakeLima(t)
	createTestSpace(t, "work")
	binary, err := exec.LookPath("limactl")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	original := string(raw)
	for _, labels := range []string{"yoloctl.agent=claude", "yoloctl.agentbox=unknown", "yoloctl.agent=claude,yoloctl.account="} {
		replacement := "elif args[0] == 'shell':\n    if 'ps' in args and 'nerdctl' in args:\n        print(json.dumps(dict(Names='unknown', Status='Up 1 minute', Labels='" + labels + "')))\n        sys.exit(0)"
		if err := os.WriteFile(binary, []byte(strings.Replace(original, "elif args[0] == 'shell':", replacement, 1)), 0755); err != nil {
			t.Fatal(err)
		}
		for _, force := range []bool{false, true} {
			if err := stopAccountUsers(t.Context(), "work", "personal", force, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot determine runtime account") {
				t.Fatalf("labels=%s force=%v: %v", labels, force, err)
			}
		}
	}
}

func TestSpaceAccountsRunningEnsureDoesNotRewriteConfig(t *testing.T) {
	fakeLima(t)
	createTestSpace(t, "work")
	path, err := vmhost.InstanceConfigPath("work")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSpaceRunning(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("already-running ensure rewrote config")
	}
}

func TestSpaceAccountsMissingUUIDAloneNeverAuthorizesFormatting(t *testing.T) {
	fakeLima(t)
	t.Setenv("FAKE_LIMA_FAIL", "start")
	if err := runSpaceCommand(t, newSpaceCreateCmd(), "work", "--workdir", t.TempDir(), "--account", "personal"); err == nil {
		t.Fatal("expected initial failure")
	}
	cfg, err := spaceconfig.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccountDiskInitializing = false
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LIMA_FAIL", "")
	if err := ensureSpaceRunning(t.Context(), "work"); err == nil {
		t.Fatal("formatted without authority")
	}
	path, _ := vmhost.InstanceConfigPath("work")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("format: true")) {
		t.Fatal("left formatting enabled")
	}
}

func TestSpaceAccountsBrokenVMDeletionRequiresDeadProcesses(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint(live), func(t *testing.T) {
			home := fakeLima(t)
			createTestSpace(t, "work")
			oldFind, oldDelete := spaceDestroyFindInstance, spaceDestroyDelete
			t.Cleanup(func() { spaceDestroyFindInstance = oldFind; spaceDestroyDelete = oldDelete })
			exists := true
			deleted := false
			spaceDestroyFindInstance = func(context.Context, string) (vmhost.Instance, bool, error) {
				return vmhost.Instance{Status: "Broken", VMType: "qemu"}, exists, nil
			}
			spaceDestroyDelete = func(_ context.Context, _ string, force bool) error {
				if !force {
					t.Fatal("stale Broken VM needs force after proof of exit")
				}
				deleted = true
				exists = false
				return nil
			}
			if live {
				if err := os.WriteFile(filepath.Join(home, "yolo-work", "ha.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := deleteSpaceRuntime(t.Context(), "work", io.Discard)
			if live && (err == nil || deleted) {
				t.Fatalf("deleted live VM: %v", err)
			}
			if !live && (err != nil || !deleted) {
				t.Fatalf("failed to delete dead VM: %v", err)
			}
		})
	}
}

func TestSpaceAccountsNativeReadsAllowColdTransport(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := accountStateOperation
	t.Cleanup(func() { accountStateOperation = old })
	accountStateOperation = func(ctx context.Context, _ string, _ accountstate.Request) (accountstate.Result, error) {
		select {
		case <-ctx.Done():
			return accountstate.Result{}, ctx.Err()
		case <-time.After(3200 * time.Millisecond):
			return accountstate.Result{Present: true, Leaves: []string{"codex"}}, nil
		}
	}
	profile, err := agentprofile.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		files, err := accountAuthFiles(t.Context(), accountAuthTarget{space: "work", name: "personal", profile: profile})
		if err == nil && len(files) != 1 {
			err = errors.New("native auth was lost")
		}
		results <- err
	}()
	go func() {
		leaves := accountStateLeaves(t.Context(), "work", "personal")
		if len(leaves) != 1 {
			results <- errors.New("native state reported unavailable")
			return
		}
		results <- nil
	}()
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	// A shorter caller deadline still wins.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := accountAuthFiles(ctx, accountAuthTarget{space: "work", name: "personal", profile: profile}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost caller deadline: %v", err)
	}
}
