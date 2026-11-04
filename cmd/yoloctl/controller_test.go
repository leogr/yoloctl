package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

func TestControllerClientRefusesOfflineDeclarationsWithoutBooting(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	t.Setenv("CONTROLLER_TEST_CALLS", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// No real Lima command can escape this fixture. Any start, shell or disk
	// operation is recorded and fails rather than touching the developer's VM.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$CONTROLLER_TEST_CALLS\"\n" +
		"if [ \"$*\" = 'list --format json' ]; then\n" +
		"  printf '%s\\n' '{\"name\":\"yolo-work\",\"status\":\"Stopped\"}'\n" +
		"else exit 37; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := spaceconfig.New("work", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	legacy.AccountDiskUUID = "test-uuid"
	boot, _, err := spaceconfig.Split(legacy, 1000, 20, "test-generation")
	if err != nil {
		t.Fatal(err)
	}
	boot.Bundle, err = controller.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"box", "list"}, {"box", "show", "worker"},
		{"box", "create", "worker", "--agent", "none", "--manifest-only"},
		{"box", "start", "worker", "--no-attach"},
		{"account", "show", "personal"}, {"account", "create", "new"},
		{"port", "list"}, {"image", "list"}, {"agent", "status"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			root := newClientRootCmd()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetIn(strings.NewReader(""))
			root.SetArgs(append(append([]string{}, argv...), "--space", "work"))
			if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "space start work") {
				t.Fatalf("expected explicit-start hint, got %v", err)
			}
		})
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if call != "list --format json" {
			t.Fatalf("offline command attempted runtime work: %s", call)
		}
	}
	dir, err := layout.SpaceBundleDir("work")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"boxes", "accounts"} {
		if _, err := os.Stat(filepath.Join(dir, kind)); !os.IsNotExist(err) {
			t.Fatalf("offline command created host %s: %v", kind, err)
		}
	}
}

func TestControllerSecretCapturedLocallyWithoutArgv(t *testing.T) {
	for _, fromEnv := range []string{"", "LOCAL_TEST_SECRET", fromEnvUseName} {
		t.Run(fromEnv, func(t *testing.T) {
			t.Setenv("LOCAL_TEST_SECRET", "private\nmultiline")
			cmd := newAccountSecretSetCmd()
			cmd.SetContext(context.Background())
			cmd.SetIn(strings.NewReader("private\nignored"))
			if err := cmd.Flags().Set("from-env", fromEnv); err != nil {
				t.Fatal(err)
			}
			req := controller.Request{Args: []string{"personal", "LOCAL_TEST_SECRET"}, Flags: map[string][]string{"from-env": {fromEnv}}}
			if err := prepareControllerSecret(cmd, &req); err != nil {
				t.Fatal(err)
			}
			if _, ok := req.Flags["from-env"]; ok {
				t.Fatal("environment lookup reached worker")
			}
			if strings.Contains(strings.Join(req.Args, " "), "private") {
				t.Fatal("secret in argv")
			}
			var secret controllerSecret
			if err := json.Unmarshal(req.Payload, &secret); err != nil {
				t.Fatal(err)
			}
			want := "private"
			if fromEnv != "" {
				want += "\nmultiline"
			}
			if secret.Value != want {
				t.Fatalf("payload %q", secret.Value)
			}
			cmd.SetContext(context.WithValue(cmd.Context(), controllerSecretKey{}, secret))
			cmd.SetIn(bytes.NewReader(nil))
			t.Setenv("LOCAL_TEST_SECRET", "unrelated VM environment")
			got, err := readCredentialValue(cmd, fromEnv, "LOCAL_TEST_SECRET")
			if err != nil || got != want {
				t.Fatalf("worker value %q: %v", got, err)
			}
		})
	}
}

func TestControllerEditRevisionRejectsABAAndDependencyChanges(t *testing.T) {
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := safepath.WriteFile(root, path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("space.yaml", "defaults")
	write("boxes/worker.yaml", "original")
	get := func(generation, digest string) editDocument {
		t.Helper()
		doc, err := controllerDocumentAt(root, generation, "box", "worker", digest)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	initial := get("g1", "catalog1")
	if get("g1", "catalog1").Revision != initial.Revision {
		t.Fatal("unchanged document drifted")
	}
	if get("g2", "catalog1").Revision == initial.Revision || get("g1", "catalog2").Revision == initial.Revision {
		t.Fatal("dependency change ignored")
	}
	write("space.yaml", "new defaults")
	if get("g1", "catalog1").Revision == initial.Revision {
		t.Fatal("space defaults change ignored")
	}
	before := get("g1", "catalog1")
	write("boxes/worker.yaml", "original") // same YAML, new inode
	if get("g1", "catalog1").Revision == before.Revision {
		t.Fatal("identical replacement accepted (ABA)")
	}
	if err := os.Remove(filepath.Join(root, "boxes/worker.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "space.yaml"), filepath.Join(root, "boxes/worker.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := controllerDocumentAt(root, "g1", "box", "worker", "catalog1"); err == nil {
		t.Fatal("symlink declaration accepted")
	}
}

func TestControllerSharedFlagRoundtrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		method string
		args   []string
		dash   int
		flags  map[string][]string
	}{
		{"box.create", []string{"worker"}, -1, map[string][]string{"operator": {"true"}, "unconfined-network": {"false"}, "features": {"go", "yolo.space.operator"}, "agent-extra-args": {"--model", "a,b"}, "env": {"KEY=a,b", "OTHER=with space"}, "mount": {}, "no-attach": {"true"}, "space": {"work"}}},
		{"box.exec", []string{"worker", "sh", "-c", "printf '%s' '$literal'", "--space=other"}, 1, map[string][]string{"space": {"work"}}},
		{"box.copy", []string{"source", "copy"}, -1, map[string][]string{"port": {""}, "from-space": {"work"}, "space": {"work"}, "no-attach": {"false"}}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			root := newRootCmd()
			cmd := findByPath(root, strings.Split(tc.method, ".")...)
			req := controller.Request{ID: "test-request-0123456789", Method: tc.method, Space: "work", Args: tc.args, Dash: tc.dash, Flags: tc.flags}
			argv, err := controllerCommandArgs(root, req)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				called = true
				if !slices.Equal(args, tc.args) || cmd.ArgsLenAtDash() != tc.dash {
					t.Fatalf("args=%q dash=%d", args, cmd.ArgsLenAtDash())
				}
				for key, want := range tc.flags {
					f := cmd.Flag(key)
					if value, ok := f.Value.(interface{ GetSlice() []string }); ok {
						if !slices.Equal(value.GetSlice(), want) {
							t.Fatalf("%s=%q, want %q", key, value.GetSlice(), want)
						}
					} else if f.Value.String() != want[len(want)-1] {
						t.Fatalf("%s=%s", key, f.Value.String())
					}
				}
				return nil
			}
			root.SetArgs(argv)
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("shared command was not invoked")
			}
		})
	}
}

func TestControllerRejectsFlagInjectionAndUnknownMethods(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, req := range []controller.Request{
		{Method: "box.create", Args: []string{"worker", "--from-space=other"}, Dash: -1},
		{Method: "box.exec", Args: []string{"--space=other", "sh"}, Dash: 1},
		{Method: "box.create", Args: []string{"worker"}, Dash: -1, Flags: map[string][]string{"unknown": {"x"}}},
		{Method: "box.nonexistent", Dash: -1},
	} {
		req.ID, req.Space = "test-request-0123456789", "work"
		if _, err := controllerCommandArgs(newRootCmd(), req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
}

func TestOperatorRequiresCurrentRuntimeIncarnationAndReadonlyEndpoint(t *testing.T) {
	p := controller.Principal{Box: "operator", Incarnation: strings.Repeat("a", 32)}
	e := operator.Endpoint{Box: p.Box, Incarnation: p.Incarnation}
	base := container.Details{Name: p.Box, Running: true,
		Labels: map[string]string{agentbox.LabelAgentbox: p.Box, agentbox.LabelSpace: "work", operator.IncarnationLabel: p.Incarnation},
		Mounts: []container.DetailMount{{Source: e.Directory(), Destination: operator.Mount, ReadOnly: true}},
	}
	if err := validateOperatorRuntime("work", p, &base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*container.Details){
		func(d *container.Details) { d.Running = false },
		func(d *container.Details) { d.Name = "different" },
		func(d *container.Details) { d.Labels[operator.IncarnationLabel] = "stale" },
		func(d *container.Details) { d.Labels[agentbox.LabelSpace] = "other" },
		func(d *container.Details) { d.Mounts[0].ReadOnly = false },
		func(d *container.Details) { d.Mounts[0].Source = "/run/yoloctl/controller" },
	} {
		copy := base
		copy.Labels = map[string]string{}
		for k, v := range base.Labels {
			copy.Labels[k] = v
		}
		copy.Mounts = slices.Clone(base.Mounts)
		mutate(&copy)
		if err := validateOperatorRuntime("work", p, &copy); err == nil {
			t.Fatalf("accepted %+v", copy)
		}
	}
}

func TestOperatorPathsUseActualBindsAndHostGrants(t *testing.T) {
	grants := &spaceconfig.Config{Mounts: []spaceconfig.Mount{{Source: "/home/dev/code", Writable: true}}}
	mounts := []container.DetailMount{
		{Source: "/home/dev/code/project", Destination: "/workdir/home/dev/code/project"},
		{Source: "/home/dev/code/library", Destination: "/library"},
		{Source: "/var/lib/yoloctl/account-state/me/codex", Destination: "/home/sandbox/.codex"},
		{Source: "/var/lib/yoloctl/boxes/operator", Destination: "/workdir"},
	}
	for raw, want := range map[string]string{".": "/home/dev/code/project", "../sibling": "/home/dev/code/sibling", "/library/src": "/home/dev/code/library/src", "/home/dev/code/other": "/home/dev/code/other"} {
		got, err := mapOperatorPath(raw, "/workdir/home/dev/code/project", mounts, grants)
		// Parent navigation outside the actual project bind must use an
		// explicit granted host path; it cannot invent a caller bind.
		if raw == "../sibling" {
			if err == nil {
				t.Fatal("escaped caller project bind")
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("%q -> %q / %v", raw, got, err)
		}
	}
	for _, raw := range []string{"/home/sandbox/.codex", "/run/yoloctl", "/workdir", "/etc/passwd", "~"} {
		if _, err := mapOperatorPath(raw, "/workdir", mounts, grants); err == nil {
			t.Fatalf("mapped %q", raw)
		}
	}
}

func TestControllerBoxCommandInventoryAndSharedSurface(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	service, client := newRootCmd(), newClientRootCmd()
	box := findByPath(service, "box")
	if len(box.Commands()) != 19 {
		t.Fatalf("update controller inventory for %d box commands", len(box.Commands()))
	}
	for _, cmd := range box.Commands() {
		method := commandMethod(cmd)
		switch method {
		case "box.use":
			// Finite box use is a local driver of public attach, not another
			// controller operation or independent authority grant.
			if _, registered := controllerOperations[method]; registered {
				t.Fatal("box.use must delegate to box.attach, not register an RPC")
			}
			method = "box.attach"
		case "box.edit":
			method += ".apply"
		}
		if !controllerOperations[method].BoxAllowed {
			t.Fatalf("%s not authorized", method)
		}
		other := findByPath(client, "box", cmd.Name())
		if other == nil || !reflect.DeepEqual(cmd.Aliases, other.Aliases) || cmd.UsageString() != other.UsageString() || cmd.Long != other.Long {
			t.Fatalf("CLI surface differs for %s", cmd.Name())
		}
	}
}
