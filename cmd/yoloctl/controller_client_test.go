package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

const controllerPathTestDiskID = "00000000000000000000000000000001"

func TestControllerHostPathsRejectManagedAliases(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, _ := layout.AccountStateRoot()
	canonical, _, err := canonicalizeMountArg(root)
	if err != nil {
		t.Fatal(err)
	}
	configRoot, _ := layout.ConfigRoot()
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(configRoot, alias); err != nil {
		t.Fatal(err)
	}
	diskRoot := layout.AccountDiskMount(controllerPathTestDiskID)
	for _, source := range []string{root, canonical, diskRoot, diskRoot + "/account-state/other/claude", alias, filepath.Join(alias, "missing-child")} {
		for _, flag := range []string{"workdir", "mount"} {
			t.Run(flag+"/"+source, func(t *testing.T) {
				value := source
				if flag == "mount" {
					value += ":/exposed:ro"
				}
				req := controller.Request{Method: "box.create", Space: "fixture", Flags: map[string][]string{
					flag: {value}, "manifest-only": {"true"},
				}}
				if err := expandControllerPaths(&req, controllerPathTestDiskID); err == nil || !strings.Contains(err.Error(), "intersects") || !IsUserError(err) {
					t.Fatalf("managed host path reached forwarding: %v (flags: %v)", err, req.Flags)
				}
			})
		}
	}
}

func TestControllerHostPathsPreserveProjectPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(alias, "not-created")
	want, _, err := canonicalizeMountArg(missing)
	if err != nil {
		t.Fatal(err)
	}
	sibling := layout.AccountDiskMount(controllerPathTestDiskID) + "-project"
	req := controller.Request{Flags: map[string][]string{
		"workdir": {missing}, "mount": {alias + ":/project:ro", sibling + ":/other"},
	}}
	if err := expandControllerPaths(&req, controllerPathTestDiskID); err != nil {
		t.Fatal(err)
	}
	if req.Flags["workdir"][0] != want || req.Flags["mount"][0] != filepath.Dir(want)+":/project:ro" || req.Flags["mount"][1] != sibling+":/other" {
		t.Fatalf("unexpected forwarded paths: %v", req.Flags)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("path expansion created the source: %v", err)
	}
	// Clearing a workdir and requests without path flags need no host lookup.
	for _, flags := range []map[string][]string{nil, {"workdir": {""}}} {
		if err := expandControllerPaths(&controller.Request{Flags: flags}, controllerPathTestDiskID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControllerHostPathsRejectBeforeDispatch(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	t.Setenv("CONTROLLER_TEST_CALLS", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$CONTROLLER_TEST_CALLS\"\n" +
		"if [ \"$*\" = 'list --format json' ]; then\n" +
		"  printf '%s\\n' '{\"name\":\"yolo-fixture\",\"status\":\"Running\"}'\n" +
		"else exit 37; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := controller.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	boot := &spaceconfig.Bootstrap{
		SchemaVersion: spaceconfig.BootstrapVersion, Name: "fixture", DiskID: controllerPathTestDiskID,
		DiskUUID: "fixture-uuid", UID: 1000, GID: 20, Bundle: identity, Generation: "fixture-generation",
		VM: spaceconfig.VMSettings{CPUs: 4, Memory: "4GiB", Disk: "30GiB"},
	}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	root, _ := layout.AccountStateRoot()
	for _, source := range []string{root, layout.AccountDiskMount(boot.DiskID)} {
		for _, argv := range [][]string{
			{"box", "create", "forbidden", "--agent", "none", "--workdir-mode", "ephemeral", "--mount", source + ":/exposed", "--manifest-only"},
			{"box", "create", "forbidden", "--agent", "none", "--workdir", source, "--no-attach"},
			{"box", "copy", "source", "forbidden", "--mount", source + ":/exposed", "--manifest-only"},
			{"box", "copy", "source", "forbidden", "--workdir", source, "--manifest-only"},
		} {
			t.Run(strings.Join(argv, " "), func(t *testing.T) {
				cmd := newClientRootCmd()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(append(argv, "--space", boot.Name))
				if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "intersects") {
					t.Fatalf("expected host protection before dispatch, got %v", err)
				}
			})
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if call != "list --format json" {
			t.Fatalf("protected path reached controller transport: %s", call)
		}
	}
}

func stubOperatorClient(t *testing.T, client *controller.OperatorClient) {
	t.Helper()
	previous := loadOperatorClient
	loadOperatorClient = func() (*controller.OperatorClient, error) { return client, nil }
	t.Cleanup(func() { loadOperatorClient = previous })
}

func TestControllerOperatorFrontendGuardsWithoutRuntime(t *testing.T) {
	stubOperatorClient(t, &controller.OperatorClient{Space: "fixture", Generation: "fixture-generation"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// These requests must be rejected before starting any external runtime tool.
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"account", "list"}, "only yoloctl box commands"},
		{[]string{"space", "show", "fixture"}, "only yoloctl box commands"},
		{[]string{"port", "list"}, "only yoloctl box commands"},
		{[]string{"image", "list"}, "only yoloctl box commands"},
		{[]string{"box", "list", "--space", "elsewhere"}, "limited to its current space"},
		{[]string{"box", "ls", "--space", "elsewhere"}, "limited to its current space"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			root := newClientRootCmd()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(tc.args)
			if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q before runtime access, got %v", tc.want, err)
			}
		})
	}

	root := newClientRootCmd()
	cmd, _, err := root.Find([]string{"box", "list"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := selectControllerTarget(context.Background(), cmd)
	if err != nil || !target.Operator || target.Space != "fixture" {
		t.Fatalf("expected fixture operator routing, got %+v: %v", target, err)
	}
}
