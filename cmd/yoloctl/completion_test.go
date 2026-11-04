package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func TestBackendCompletionOnHostCommandsIsLocalAndPlatformAware(t *testing.T) {
	stubOperatorClient(t, nil)
	configRoot := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("PATH", t.TempDir()) // No hypervisor, SSH client or controller needed.
	want := []string{"lima"}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		want = []string{"firecracker", "lima"}
	}
	for _, group := range []string{"space", "sp"} {
		for _, action := range []string{"create", "import"} {
			for _, prefix := range []string{"", "f", "l", "unknown"} {
				t.Run(group+"/"+action+"/"+prefix, func(t *testing.T) {
					root := newClientRootCmd()
					var out bytes.Buffer
					root.SetOut(&out)
					root.SetErr(io.Discard)
					root.SetArgs([]string{"__complete", group, action, "new-space", "--backend", prefix})
					if err := root.ExecuteContext(t.Context()); err != nil {
						t.Fatal(err)
					}
					var expected strings.Builder
					for _, backend := range want {
						if strings.HasPrefix(backend, prefix) {
							fmt.Fprintln(&expected, backend)
						}
					}
					fmt.Fprintf(&expected, ":%d\n", cobra.ShellCompDirectiveNoFileComp)
					if out.String() != expected.String() {
						t.Fatalf("completion protocol: got %q, want %q", out.String(), expected.String())
					}
				})
			}
		}
	}
	if _, err := os.Stat(configRoot); !os.IsNotExist(err) {
		t.Fatalf("completion created host state: %v", err)
	}
}

func TestImageDirectoryCompletionIncludesLateHostCommands(t *testing.T) {
	stubOperatorClient(t, nil)
	dir := t.TempDir()
	image := filepath.Join(dir, "image")
	if err := os.Mkdir(image, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-directory"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"create", "import"} {
		root := newClientRootCmd()
		cmd := findByPath(root, "space", action)
		cmd.SetContext(t.Context())
		fn, ok := cmd.GetFlagCompletionFunc("vm-image")
		if !ok {
			t.Fatalf("space %s has no image completion", action)
		}
		values, directive := fn(cmd, nil, dir+string(filepath.Separator))
		if len(values) != 1 || strings.TrimSuffix(values[0], "/") != image || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
			t.Fatalf("space %s image candidates: %v, directive %v", action, values, directive)
		}
	}
}

// runCompleter resolves a command by path on a freshly wired root and invokes
// its positional completer with the given preceding args. Fails if the command
// or its completer is missing.
func runCompleter(t *testing.T, args []string, path ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	cmd, _, err := newRootCmd().Find(path) // newRootCmd wires completions
	if err != nil {
		t.Fatalf("find %v: %v", path, err)
	}
	if cmd.ValidArgsFunction == nil {
		t.Fatalf("%v: no positional completer wired", path)
	}
	return cmd.ValidArgsFunction(cmd, args, "")
}

// completeSpaces must list every CONFIGURED space (each spaces/<name>/space.yaml),
// not just spaces that happen to own an agentbox. Regression: it derived the
// set from ListAllAgentboxes() plus a hardcoded "default", so a space created
// with `space create` but no boxes yet was invisible — `space ssh <TAB>` only
// ever offered "default".
func TestCompleteSpacesListsConfiguredSpaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "alpha", "/tmp/alpha")
	saveSpaceForTest(t, "beta", "/tmp/beta")

	got, directive := completeSpaces(nil, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if want := []string{"alpha", "beta"}; !slices.Equal(got, want) {
		t.Errorf("completeSpaces() = %v, want %v", got, want)
	}
}

// With no configured spaces, completion offers nothing — an uncreated space
// (even "default") is not actionable, since every space-scoped command requires
// its space.yaml (ensureSpaceRunning hard-errors otherwise).
func TestCompleteSpacesEmptyWhenNoneConfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, _ := completeSpaces(nil, nil, "")
	if len(got) != 0 {
		t.Errorf("completeSpaces() = %v, want empty", got)
	}
}

// filterByRunState is the pure core of the runtime-aware space AND box
// completers: keep declared entities matching the wanted run-state, and fall
// back to ALL declared when the runtime is unavailable (nil map).
func TestFilterByRunState(t *testing.T) {
	declared := []string{"alpha", "beta", "gamma"}
	running := map[string]bool{"alpha": true, "gamma": true}

	if got := filterByRunState(declared, running, true); !slices.Equal(got, []string{"alpha", "gamma"}) {
		t.Errorf("running set = %v, want [alpha gamma]", got)
	}
	if got := filterByRunState(declared, running, false); !slices.Equal(got, []string{"beta"}) {
		t.Errorf("startable set = %v, want [beta]", got)
	}
	// Queried successfully, nothing running: stop → none, start → all.
	if got := filterByRunState(declared, map[string]bool{}, true); len(got) != 0 {
		t.Errorf("running set (none up) = %v, want empty", got)
	}
	if got := filterByRunState(declared, map[string]bool{}, false); !slices.Equal(got, declared) {
		t.Errorf("startable set (none up) = %v, want all declared", got)
	}
	// Runtime unavailable (nil): both fall back to the full declared set.
	if got := filterByRunState(declared, nil, true); !slices.Equal(got, declared) {
		t.Errorf("running fallback = %v, want all declared", got)
	}
	if got := filterByRunState(declared, nil, false); !slices.Equal(got, declared) {
		t.Errorf("startable fallback = %v, want all declared", got)
	}
}

// `space create` / `box create` name a NEW entity, so they must suggest
// nothing (and not fall back to file completion).
func TestCreatePositionalsCompleteNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "alpha", "/tmp/alpha")

	for _, path := range [][]string{{"space", "create"}, {"box", "create"}} {
		got, directive := runCompleter(t, nil, path...)
		if len(got) != 0 {
			t.Errorf("%v completion = %v, want nothing", path, got)
		}
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%v directive = %v, want NoFileComp (no host-file fallback)", path, directive)
		}
	}
}

// Every space subcommand that takes a SPACE positional must have a completer
// wired (including the nested `space mount` subcommands). Declaration-level
// commands return the configured spaces; the runtime-gated ones (stop/ssh/
// start) are only checked for wiring here — their filtering is covered by
// TestFilterSpacesByRunState (calling them would shell out to limactl).
func TestSpacePositionalCompletionWiring(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "alpha", "/tmp/alpha",
		spaceconfig.Mount{Source: "/tmp/alpha", Writable: true},
		spaceconfig.Mount{Source: "/tmp/extra", Writable: true})
	saveSpaceForTest(t, "beta", "/tmp/beta")
	declared := []string{"alpha", "beta"}

	for _, path := range [][]string{
		{"space", "destroy"}, {"space", "show"}, {"space", "use"}, {"space", "logs"},
		{"space", "mount", "list"},
	} {
		if got, _ := runCompleter(t, nil, path...); !slices.Equal(got, declared) {
			t.Errorf("%v completion = %v, want %v", path, got, declared)
		}
	}

	// Runtime-gated commands: wired (the call itself is exercised elsewhere).
	for _, path := range [][]string{{"space", "stop"}, {"space", "ssh"}, {"space", "start"}} {
		cmd, _, err := newRootCmd().Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Errorf("%v: no positional completer wired", path)
		}
	}

	// `space mount add SPACE PATH`: first the space, then directory completion.
	if got, _ := runCompleter(t, nil, "space", "mount", "add"); !slices.Equal(got, declared) {
		t.Errorf("space mount add SPACE = %v, want %v", got, declared)
	}
	// PATH candidates are generated Go-side (completion_path.go), so the
	// directive suppresses the shell's own file completion instead of
	// delegating to it.
	if _, directive := runCompleter(t, []string{"alpha"}, "space", "mount", "add"); directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Errorf("space mount add PATH directive = %v, want NoFileComp set", directive)
	}

	// `space mount remove SPACE PATH`: the PATH offers only that space's
	// currently granted mount sources.
	if got, _ := runCompleter(t, []string{"alpha"}, "space", "mount", "remove"); !slices.Equal(got, []string{"/tmp/alpha", "/tmp/extra"}) {
		t.Errorf("space mount remove PATH = %v, want the granted sources", got)
	}
}

// Box positional completion. The declaration-level commands (attach/logs/show/
// destroy/recreate/reconcile/diff/edit/set) return the configured boxes;
// `box create` (a NEW name) returns nothing. The run-state-gated commands
// (stop/exec/start) are checked for wiring only — calling them would shell the
// VM's `nerdctl ps`; their filtering is covered by TestFilterByRunState.
func TestBoxPositionalCompletionWiring(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saved, _, err := buildAgentboxConfig(t.Context(), flagsTestOpts("coder"))
	if err != nil {
		t.Fatalf("build box: %v", err)
	}
	if err := saved.Save(); err != nil {
		t.Fatalf("save box: %v", err)
	}

	for _, path := range [][]string{
		{"box", "attach"}, {"box", "logs"}, {"box", "show"}, {"box", "destroy"},
		{"box", "recreate"}, {"box", "reconcile"}, {"box", "diff"}, {"box", "edit"}, {"box", "set"},
		{"box", "session"},
	} {
		if got, _ := runCompleter(t, nil, path...); !slices.Equal(got, []string{"coder"}) {
			t.Errorf("%v completion = %v, want [coder]", path, got)
		}
	}

	// Run-state-gated: wired (not called — would query the VM).
	for _, path := range [][]string{{"box", "stop"}, {"box", "exec"}, {"box", "start"}} {
		cmd, _, err := newRootCmd().Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Errorf("%v: no positional completer wired", path)
		}
	}

	// `box session BOX` takes one box; nothing follows it.
	if got, _ := runCompleter(t, []string{"coder"}, "box", "session"); len(got) != 0 {
		t.Errorf("box session second-arg completion = %v, want nothing", got)
	}

	if got, _ := runCompleter(t, nil, "box", "create"); len(got) != 0 {
		t.Errorf("box create completion = %v, want nothing", got)
	}

	// `box copy SOURCE NEWNAME`: SOURCE completes the declared boxes,
	// NEWNAME names a NEW box and suggests nothing.
	if got, _ := runCompleter(t, nil, "box", "copy"); !slices.Equal(got, []string{"coder"}) {
		t.Errorf("box copy SOURCE completion = %v, want [coder]", got)
	}
	if got, _ := runCompleter(t, []string{"coder"}, "box", "copy"); len(got) != 0 {
		t.Errorf("box copy NEWNAME completion = %v, want nothing", got)
	}

}

// Root attach uses the same positional completion as box attach.
func TestRootAliasPositionalCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saved, _, err := buildAgentboxConfig(t.Context(), flagsTestOpts("coder"))
	if err != nil {
		t.Fatalf("build box: %v", err)
	}
	if err := saved.Save(); err != nil {
		t.Fatalf("save box: %v", err)
	}

	// Root attach: any declared box (mirrors `box attach`).
	if got, _ := runCompleter(t, nil, "attach"); !slices.Equal(got, []string{"coder"}) {
		t.Errorf("root attach completion = %v, want [coder]", got)
	}

}

// Space-scoped box completion must honor the PINNED context space, not the
// hardcoded "default". Regression: spaceScope returned defaultSpaceName
// whenever --space wasn't explicitly typed — but the --space flag's OWN default
// is "default", so its value is never empty and the old emptiness check never
// fell through to the context pin. Result: `box show <TAB>` always listed the
// default space's boxes even after `space use other`.
func TestBoxCompletionHonorsPinnedSpace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveSpaceForTest(t, "other", "/tmp/other")

	// A box that exists ONLY in the non-default space.
	opts := flagsTestOpts("coder")
	opts.Space = "other"
	saved, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("build box: %v", err)
	}
	if err := saved.Save(); err != nil {
		t.Fatalf("save box: %v", err)
	}

	// Pin the context to that space (mirrors `yoloctl space use other`).
	if err := (&config.Context{Space: "other"}).Save(); err != nil {
		t.Fatalf("pin context: %v", err)
	}

	// With no --space flag, completion must scope to the pinned space, so the
	// box declared only in "other" is offered.
	if got, _ := runCompleter(t, nil, "box", "show"); !slices.Equal(got, []string{"coder"}) {
		t.Errorf("box show (pinned other) = %v, want [coder]", got)
	}
}

func TestBoxShowDestroyCompletionIncludesRuntimeOrphans(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveCompletionBox(t, defaultSpaceName, "declared")
	withCompletionResolver(t, []container.Container{
		{Name: "orphan", Status: "Up 1 minute", Image: "img"},
	})

	for _, path := range [][]string{{"box", "show"}, {"box", "destroy"}} {
		if got, _ := runCompleter(t, nil, path...); !slices.Equal(got, []string{"declared", "orphan"}) {
			t.Errorf("%v completion = %v, want [declared orphan]", path, got)
		}
	}
}

func TestBoxReconcileCompletionUsesDiffState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveCompletionBox(t, defaultSpaceName, "drifted")
	saveCompletionBox(t, defaultSpaceName, "synced")
	withCompletionResolver(t, []container.Container{
		{Name: "drifted", Status: "Up 1 minute", Image: "img"},
		{Name: "synced", Status: "Up 1 minute", Image: "img"},
	})

	got, _ := runCompleter(t, nil, "box", "reconcile")
	if !slices.Equal(got, []string{"drifted"}) {
		t.Errorf("box reconcile completion = %v, want [drifted]", got)
	}
}

func saveCompletionBox(t *testing.T, space, name string) {
	t.Helper()
	saveCompletionBoxAt(t, space, name, "/tmp/def")
}

// saveCompletionBoxAt writes a minimal declared box bound to the given host
// workdir (stored verbatim as both source and canonical).
func saveCompletionBoxAt(t *testing.T, space, name, workdir string) {
	t.Helper()
	cfg := &agentbox.Config{
		SchemaVersion: agentbox.SchemaVersion,
		Name:          name,
		Space:         space,
		Agent:         "codex",
		Account:       "default",
		Image:         "img",
		Features:      []string{"codex"},
		Workdir: agentbox.Workdir{
			Mode:      agentbox.WorkdirModeHost,
			Source:    workdir,
			Canonical: workdir,
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("save box: %v", err)
	}
}

func withCompletionResolver(t *testing.T, containers []container.Container) {
	t.Helper()
	old := newBoxStateResolver
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: defaultSpaceName, Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return containers, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionNone}
			},
			InspectActual: func(_ context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
				image := cfg.Image
				if cfg.Name == "drifted" {
					image = "old-img"
				}
				return agentbox.ActualState{
					Exists:        true,
					Image:         image,
					ReadOnlyWD:    !cfg.WorkdirWritable(),
					WorkdirSource: cfg.Workdir.MountSource(),
					WorkdirDest:   cfg.Workdir.ContainerPath(),
					Labels:        map[string]string{agentbox.LabelAgent: cfg.Agent},
					PolicyContent: []byte("policy"),
				}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) {
				return []byte("policy"), nil
			},
			ResolvedImage: func(c *agentbox.Config) (string, bool, error) {
				return c.Image, false, nil
			},
		}
	}
	t.Cleanup(func() { newBoxStateResolver = old })
}

// The remaining entity positionals are wired to their listers. The global
// credential store + its completion are gone; assert wiring (non-nil) for the
// rest — including the new `account auth` subcommands — so a future refactor
// can't silently drop them.
func TestOtherEntityCompletionWiring(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, path := range [][]string{
		{"agent", "update"},
		{"image", "pull"},
		{"port", "forward"}, {"port", "unforward"}, {"port", "list"},
		{"account", "auth", "import"}, {"account", "auth", "status"},
		// Catalog verbs: show/copy/edit/destroy for each kind.
		{"agent", "show"}, {"agent", "copy"}, {"agent", "edit"}, {"agent", "destroy"},
		{"feature", "show"}, {"feature", "copy"}, {"feature", "edit"}, {"feature", "destroy"},
		{"capability", "show"}, {"capability", "copy"}, {"capability", "edit"}, {"capability", "destroy"},
	} {
		cmd, _, err := newRootCmd().Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Errorf("%v: no positional completer wired", path)
		}
	}
}
