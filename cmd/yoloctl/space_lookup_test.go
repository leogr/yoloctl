package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/followview"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func saveSpaceLookupFixture(t *testing.T, name string) *spaceconfig.Bootstrap {
	t.Helper()
	cfg, err := spaceconfig.New(name, t.TempDir(), "personal")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccountDiskUUID = "space-lookup-fixture"
	boot, _, err := spaceconfig.Split(cfg, 1000, 1000, "space-lookup-generation")
	if err != nil {
		t.Fatal(err)
	}
	boot.VM.Backend = "lima"
	boot.Bundle, err = controller.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	return boot
}

func executeSpaceLookupCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	root := newClientRootCmd()
	root.SetArgs(args)
	root.SetOut(&stdout)
	root.SetErr(io.Discard)
	root.SetIn(strings.NewReader(""))
	err := root.ExecuteContext(t.Context())
	return stdout.String(), err
}

func requireAbsentSpaceError(t *testing.T, name string, err error) {
	t.Helper()
	var absent absentSpaceError
	if !errors.As(err, &absent) || absent.name != name || classifyError(err) != exitUser {
		t.Fatalf("wanted absent space %q with user exit status, got %v", name, err)
	}
}

func TestNamedSpaceCommandsReportMissingSpace(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	t.Setenv("YOLOCTL_SPACE", "")
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(bindCLITestLogger(t, io.Discard))
	saveSpaceLookupFixture(t, "work")
	if err := purgeSpaceBundle("work"); err != nil {
		t.Fatal(err)
	}
	if err := (&config.Context{Space: "work"}).Save(); err != nil {
		t.Fatal(err)
	}
	lookup := controllerSpaceLookup
	t.Cleanup(func() { controllerSpaceLookup = lookup })
	controllerSpaceLookup = func(context.Context, string) (vmhost.Instance, bool, error) {
		t.Fatal("absent space reached VM lookup")
		return vmhost.Instance{}, false, nil
	}
	for _, args := range [][]string{
		{"box", "list", "--space", "work"},
		{"box", "list"},
		{"account", "list", "--space", "work"},
		{"box", "start", "coder", "--space", "work"},
		{"port", "list", "--space", "work"},
		{"agent", "status", "--space", "work"},
		{"image", "prune", "--space", "work"},
		{"chat", "status", "--space", "work"},
		{"space", "operation", "work"},
		{"space", "storage", "work"},
		{"space", "show", "work"},
		{"space", "start", "work"},
		{"space", "stop", "work"},
		{"space", "recreate", "work"},
		{"space", "destroy", "work"},
		{"space", "upgrade", "work"},
		{"space", "set", "work", "--cpus", "6"},
		{"space", "edit", "work"},
		{"space", "export", "work", filepath.Join(t.TempDir(), "out.tar")},
		{"space", "mount", "list", "work"},
		{"space", "mount", "remove", "work", "/project"},
		{"space", "ssh", "work", "--", "true"},
		{"space", "ssh", "--start", "work", "--", "true"},
		{"space", "logs", "work"},
		{"space", "logs", "work", "--unit", "yoloctl-controller.service"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			out, err := executeSpaceLookupCLI(t, args...)
			requireAbsentSpaceError(t, "work", err)
			if err.Error() != `space "work" does not exist` || out != "" {
				t.Fatalf("unexpected absent-space result: %v, stdout %q", err, out)
			}
		})
	}

	t.Setenv("YOLOCTL_SPACE", "never-created")
	_, err := executeSpaceLookupCLI(t, "box", "list")
	requireAbsentSpaceError(t, "never-created", err)
	t.Setenv("YOLOCTL_SPACE", "")
	if err := (&config.Context{}).Save(); err != nil {
		t.Fatal(err)
	}
	_, err = executeSpaceLookupCLI(t, "box", "list")
	requireAbsentSpaceError(t, "default", err)
}

func TestBoxCopyReportsMissingSourceSpace(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	t.Cleanup(bindCLITestLogger(t, io.Discard))
	selectTarget := controllerTargetForCommand
	t.Cleanup(func() { controllerTargetForCommand = selectTarget })
	controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
		return controllerTarget{Space: "destination", Call: func(context.Context, controller.Request, controller.ClientIO) error {
			t.Fatal("attempted to copy from an absent space")
			return nil
		}}, nil
	}
	_, err := executeSpaceLookupCLI(t, "box", "copy", "source-box", "new-box", "--from-space", "missing", "--space", "destination", "--manifest-only")
	requireAbsentSpaceError(t, "missing", err)
}

func TestRequiredSpaceLookupPreservesRetainedBundle(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	saveSpaceLookupFixture(t, "work")
	lookup := controllerSpaceLookup
	t.Cleanup(func() { controllerSpaceLookup = lookup })
	controllerSpaceLookup = func(context.Context, string) (vmhost.Instance, bool, error) {
		return vmhost.Instance{Name: "work", Status: "Stopped"}, true, nil
	}
	_, err := executeSpaceLookupCLI(t, "box", "list", "--space", "work")
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("stopped space lost its remedy: %v", err)
	}

	dir, err := layout.SpaceBundleDir("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.RemoveFile(dir, "space.yaml"); err != nil {
		t.Fatal(err)
	}
	_, err = executeSpaceLookupCLI(t, "box", "list", "--space", "work")
	var absent absentSpaceError
	if err == nil || errors.As(err, &absent) || !strings.Contains(err.Error(), "restore its bundle") {
		t.Fatalf("retained bundle lost its recovery error: %v", err)
	}
}

func TestSpaceBootLogsUseLocalPathWithoutLimactl(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	tail, err := exec.LookPath("tail")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(tail, filepath.Join(bin, "tail")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	home, err := layout.LimaHome()
	if err != nil {
		t.Fatal(err)
	}
	saveSpaceLookupFixture(t, "declared")
	for _, name := range []string{"declared", "orphan"} {
		dir, err := layout.LimaInstanceDir(home, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ha.stdout.log"), []byte("retained VM boot log\n"), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := executeSpaceLookupCLI(t, "space", "logs", name)
		if err != nil || !strings.Contains(out, "retained VM boot log") {
			t.Fatalf("%s boot log: %q, %v", name, out, err)
		}
	}
}

func TestSpaceRuntimeRefusesFirecrackerWithoutBootstrap(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	tail, err := exec.LookPath("tail")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(tail, filepath.Join(bin, "tail")); err != nil {
		t.Fatal(err)
	}
	const script = `#!/bin/sh
printf '%s\n' 'limactl called' > "$LIMA_CALLS"
`
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(bin, "lima-calls")
	t.Setenv("PATH", bin)
	t.Setenv("LIMA_CALLS", calls)
	firecracker, err := layout.FirecrackerInstanceDir("fc-orphan")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(firecracker, 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"firecracker-only", "both-backends"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "both-backends" {
				dir, err := layout.LimaInstanceDir("", "fc-orphan")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "ha.stdout.log"), []byte("log from the wrong VM\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{
				{"space", "logs", "fc-orphan"},
				{"space", "logs", "fc-orphan", "--unit", "yoloctl-session-coder.service"},
				{"space", "ssh", "fc-orphan", "--", "true"},
			} {
				out, err := executeSpaceLookupCLI(t, args...)
				if err == nil || !strings.Contains(err.Error(), "conflicting firecracker VM metadata; restore its bootstrap") || out != "" {
					t.Fatalf("%v used an unverified runtime: output %q, error %v", args, out, err)
				}
			}
			if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("runtime lookup attempted limactl: %v", err)
			}
		})
	}
}

func TestSpaceSSHReportsOrphanInRecordedLimaHome(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	peer := saveSpaceLookupFixture(t, "peer")
	peer.VM.LimaHome = t.TempDir()
	if err := peer.Save(); err != nil {
		t.Fatal(err)
	}
	instance, err := layout.LimaInstanceDir(peer.VM.LimaHome, "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(instance, 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	const script = `#!/bin/sh
[ "$LIMA_HOME" = "$ORPHAN_HOME" ] || exit 0
printf '%s\n' '{"name":"yolo-work","status":"Stopped"}'
`
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("ORPHAN_HOME", peer.VM.LimaHome)
	_, err = executeSpaceLookupCLI(t, "space", "ssh", "work", "--", "true")
	if err == nil || !strings.Contains(err.Error(), `space "work" has an orphan Lima instance`) || !strings.Contains(err.Error(), "restore its bootstrap") {
		t.Fatalf("SSH lost the recorded-home recovery message: %v", err)
	}
}

func TestSpaceEditReportsDestroyedSpaceAndKeepsEdits(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", "")
	_, applied := resourceSpaceFixture(t)
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	const contents = "cpus: 6\nmemory: 8GiB\n"
	var edits string
	err := editSpaceResources(cmd, "dev", func(_ context.Context, path string) error {
		edits = path
		t.Cleanup(func() { _ = os.Remove(path) })
		if err := purgeSpaceBundle("dev"); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(contents), 0600)
	})
	requireAbsentSpaceError(t, "dev", err)
	data, readErr := os.ReadFile(edits)
	if readErr != nil || string(data) != contents || len(*applied) != 0 || !strings.Contains(err.Error(), "edits kept at") {
		t.Fatalf("edit recovery changed: %q, %v, effects %v, command error %v", data, readErr, *applied, err)
	}
}

func TestAbsoluteVMFollowDistinguishesMissingSpaceFromUnavailableSettings(t *testing.T) {
	followSeams(t)
	saveSpaceLookupFixture(t, "declared")
	lookup := controllerSpaceLookup
	t.Cleanup(func() { controllerSpaceLookup = lookup })
	controllerSpaceLookup = func(context.Context, string) (vmhost.Instance, bool, error) {
		return vmhost.Instance{}, false, errors.New("settings temporarily unavailable")
	}
	const target = "/project/notes.md"
	var selected followview.Options
	followRun = func(_ context.Context, opts followview.Options) error {
		selected = opts
		return nil
	}
	err := executeFollow(t, "--space", "missing", target)
	requireAbsentSpaceError(t, "missing", err)
	if selected.Path != "" {
		t.Fatal("opened follow for an absent space")
	}
	if err := executeFollow(t, "--space", "declared", target); err != nil {
		t.Fatal(err)
	}
	if selected.Path != target || selected.Space != "declared" || selected.Workdir != "" {
		t.Fatalf("absolute-path recovery changed: %+v", selected)
	}
}
