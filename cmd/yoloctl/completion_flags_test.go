package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

// flagCompleter resolves a command by path on a freshly wired root and
// returns it with the completer registered for one of its flags. Fails if no
// completer is wired — every test here is also a wiring regression test.
func flagCompleter(t *testing.T, flag string, path ...string) (*cobra.Command, cobra.CompletionFunc) {
	t.Helper()
	cmd, _, err := newRootCmd().Find(path)
	if err != nil {
		t.Fatalf("find %v: %v", path, err)
	}
	fn, ok := cmd.GetFlagCompletionFunc(flag)
	if !ok {
		t.Fatalf("%v: no completer wired for --%s", path, flag)
	}
	return cmd, fn
}

// The box-creating commands complete --workdir from the space's mount
// grants: an empty token seeds the grant roots (annotated), a token inside a
// grant walks the real filesystem, and paths outside every grant are
// suppressed — they are exactly the values create-time validation rejects.
func TestGrantedWorkdirFlagCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	grant := t.TempDir()
	if err := os.MkdirAll(filepath.Join(grant, "proj"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	saveSpaceForTest(t, defaultSpaceName, grant)

	for _, path := range [][]string{{"box", "create"}, {"box", "copy"}} {
		cmd, fn := flagCompleter(t, "workdir", path...)

		got, directive := fn(cmd, nil, "")
		if want := []cobra.Completion{grant + "/\tdefault workdir"}; !slices.Equal(got, want) {
			t.Errorf("%v --workdir seeds = %v, want %v", path, got, want)
		}
		if directive&cobra.ShellCompDirectiveNoFileComp == 0 {
			t.Errorf("%v --workdir directive = %v, want NoFileComp set", path, directive)
		}

		got, _ = fn(cmd, nil, grant+"/")
		if want := []cobra.Completion{grant + "/proj/"}; !slices.Equal(got, want) {
			t.Errorf("%v --workdir inside grant = %v, want %v", path, got, want)
		}
	}
}

// Workdir preflight uses the most-specific covering grant and requires it to
// be writable. Completion must therefore hide paths shadowed by a read-only
// child grant, while retaining that directory as a navigation step when it
// leads to a still-deeper writable grant.
func TestGrantedWorkdirCompletionHonorsEffectiveWritableGrant(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	defaultDir := filepath.Join(root, "default")
	readOnly := filepath.Join(root, "private")
	writableChild := filepath.Join(readOnly, "allowed")
	for _, dir := range []string{defaultDir, filepath.Join(readOnly, "blocked"), writableChild} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	saveSpaceForTest(t, defaultSpaceName, defaultDir,
		spaceconfig.Mount{Source: root, Writable: true},
		spaceconfig.Mount{Source: readOnly, Writable: false},
		spaceconfig.Mount{Source: writableChild, Writable: true},
	)

	cmd, fn := flagCompleter(t, "workdir", "box", "create")
	got, _ := fn(cmd, nil, root+"/")
	if want := []cobra.Completion{defaultDir + "/\tdefault workdir", readOnly + "/"}; !slices.Equal(got, want) {
		t.Fatalf("parent candidates = %v, want %v", got, want)
	}
	got, _ = fn(cmd, nil, readOnly+"/")
	if want := []cobra.Completion{writableChild + "/\tmount grant (rw)"}; !slices.Equal(got, want) {
		t.Fatalf("read-only subtree candidates = %v, want %v", got, want)
	}
}

// A valid space config with no writable grant is authoritative: it must yield
// no workdir candidates, not trigger the unreadable-config fallback to the
// entire host filesystem.
func TestGrantedWorkdirCompletionAllReadOnlyStaysConstrained(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	saveSpaceForTest(t, defaultSpaceName, root, spaceconfig.Mount{Source: root, Writable: false})

	cmd, fn := flagCompleter(t, "workdir", "box", "create")
	got, directive := fn(cmd, nil, "")
	if len(got) != 0 {
		t.Fatalf("candidates = %v, want none", got)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive = %v, want NoFileComp", directive)
	}
}

// --workdir-mode ephemeral makes --workdir invalid; once it's on the line
// the completer offers nothing instead of a value that will be rejected.
func TestGrantedWorkdirEphemeralSuppressesCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, t.TempDir())

	cmd, fn := flagCompleter(t, "workdir", "box", "create")
	if err := cmd.Flags().Set("workdir-mode", agentbox.WorkdirModeEphemeral); err != nil {
		t.Fatalf("set workdir-mode: %v", err)
	}
	got, directive := fn(cmd, nil, "")
	if len(got) != 0 {
		t.Errorf("candidates = %v, want none with --workdir-mode ephemeral", got)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}
}

// Without a readable space config the grant set is unknown: degrade to
// unconstrained host dirs (same fallback philosophy as the run-state
// completers) rather than completing nothing.
func TestGrantedWorkdirFallsBackWithoutSpaceConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cmd, fn := flagCompleter(t, "workdir", "box", "create")
	got, _ := fn(cmd, nil, dir+"/")
	if want := []cobra.Completion{dir + "/sub/"}; !slices.Equal(got, want) {
		t.Errorf("fallback = %v, want %v", got, want)
	}
}

// --mount completes its grant-constrained SOURCE half only; past the first
// colon the value is an in-box target with nothing to list. Files under a
// grant complete too — parseMountSpec accepts host paths to either kind of
// object, and binding a single config file is a standard bind-mount pattern.
func TestGrantedMountSourceFlagCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	grant := t.TempDir()
	if err := os.WriteFile(filepath.Join(grant, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	saveSpaceForTest(t, defaultSpaceName, grant)

	cmd, fn := flagCompleter(t, "mount", "box", "create")
	got, _ := fn(cmd, nil, "")
	if want := []cobra.Completion{grant + "/\tdefault workdir"}; !slices.Equal(got, want) {
		t.Errorf("--mount seeds = %v, want %v", got, want)
	}

	got, _ = fn(cmd, nil, grant+"/")
	if want := []cobra.Completion{grant + "/config.json"}; !slices.Equal(got, want) {
		t.Errorf("--mount file source = %v, want %v", got, want)
	}
	t.Chdir(grant)
	got, _ = fn(cmd, nil, "con")
	if want := []cobra.Completion{"config.json"}; !slices.Equal(got, want) {
		t.Errorf("--mount relative source = %v, want %v", got, want)
	}

	got, directive := fn(cmd, nil, grant+":")
	if len(got) != 0 {
		t.Errorf("--mount past colon = %v, want none", got)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("--mount past colon directive = %v, want NoFileComp", directive)
	}
}

// `box ls --workdir` filters by declared workdirs, so its completion draws
// from box manifests — deduplicated, host-mode only — not the filesystem.
// --all-spaces widens the scope to every space.
func TestDeclaredWorkdirFilterCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveCompletionBoxAt(t, defaultSpaceName, "one", "/tmp/def")
	saveCompletionBoxAt(t, defaultSpaceName, "two", "/tmp/def") // same workdir: deduplicated
	saveCompletionBoxAt(t, defaultSpaceName, "three", "/tmp/other")
	saveSpaceForTest(t, "second", "/tmp/second")
	saveCompletionBoxAt(t, "second", "four", "/tmp/second")

	want := []cobra.Completion{"/tmp/def", "/tmp/other"}
	for _, path := range [][]string{{"box", "list"}} {
		cmd, fn := flagCompleter(t, "workdir", path...)
		if got, _ := fn(cmd, nil, ""); !slices.Equal(got, want) {
			t.Errorf("%v --workdir = %v, want %v", path, got, want)
		}
	}

	cmd, fn := flagCompleter(t, "workdir", "box", "list")
	if err := cmd.Flags().Set("all-spaces", "true"); err != nil {
		t.Fatalf("set all-spaces: %v", err)
	}
	wantAll := []cobra.Completion{"/tmp/def", "/tmp/other", "/tmp/second"}
	if got, _ := fn(cmd, nil, ""); !slices.Equal(got, wantAll) {
		t.Errorf("--all-spaces --workdir = %v, want %v", got, wantAll)
	}
}

// Declared workdirs are stored canonically, but completion replies must retain
// the spelling the user started with or shells/the palette discard them as
// non-prefix matches.
func TestDeclaredWorkdirFilterPreservesLiteralPrefix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	workdir := filepath.Join(home, "src", "project")
	nested := filepath.Join(home, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	saveSpaceForTest(t, defaultSpaceName, workdir)
	saveCompletionBoxAt(t, defaultSpaceName, "one", workdir)

	cmd, fn := flagCompleter(t, "workdir", "box", "list")
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"~/s", "~/src/project"},
		{"../src/p", "../src/project"},
		{"./../src/p", "./../src/project"},
		{home + "//s", home + "//src/project"},
	} {
		got, directive := fn(cmd, nil, tc.input)
		if want := []cobra.Completion{tc.want}; !slices.Equal(got, want) {
			t.Errorf("input %q = %v, want %v", tc.input, got, want)
		}
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("input %q directive = %v, want NoFileComp", tc.input, directive)
		}
	}
}

// `account auth import --file` is a host FILE path: files and directories
// both complete (the login file may sit anywhere).
func TestAuthImportFileFlagCompletion(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cmd, fn := flagCompleter(t, "file", "account", "auth", "import")
	got, _ := fn(cmd, nil, dir+"/")
	if want := []cobra.Completion{dir + "/auth.json", dir + "/sub/"}; !slices.Equal(got, want) {
		t.Errorf("--file = %v, want %v", got, want)
	}
}

// `space create --workdir` names a NEW grant: it must stay unconstrained by
// any existing space's grants (the per-command overrides don't apply; the
// uniform walk wires plain host-dir completion).
func TestSpaceCreateWorkdirStaysUnconstrained(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/unrelated-grant")
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "fresh"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cmd, fn := flagCompleter(t, "workdir", "space", "create")
	got, _ := fn(cmd, nil, dir+"/")
	if want := []cobra.Completion{dir + "/fresh/"}; !slices.Equal(got, want) {
		t.Errorf("space create --workdir = %v, want %v (unconstrained)", got, want)
	}
}
