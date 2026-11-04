package agentbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalizeWorkdir_ExistingDir(t *testing.T) {
	dir := t.TempDir()
	canonical, exists, err := CanonicalizeWorkdir(dir)
	if err != nil {
		t.Fatalf("CanonicalizeWorkdir: %v", err)
	}
	if !exists {
		t.Errorf("exists = false for an existing dir")
	}
	// EvalSymlinks may resolve /tmp → /private/tmp etc.; the canonical
	// must itself be a real, absolute path.
	if !filepath.IsAbs(canonical) {
		t.Errorf("canonical %q is not absolute", canonical)
	}
}

func TestCanonicalizeWorkdir_ResolvesSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	canonical, exists, err := CanonicalizeWorkdir(link)
	if err != nil {
		t.Fatalf("CanonicalizeWorkdir: %v", err)
	}
	if !exists {
		t.Errorf("exists = false through a symlink to a real dir")
	}
	realResolved, _ := filepath.EvalSymlinks(real)
	if canonical != realResolved {
		t.Errorf("canonical = %q, want symlink target %q", canonical, realResolved)
	}
}

func TestCanonicalizeWorkdir_NonexistentLeafKeepsStableIdentity(t *testing.T) {
	parent := t.TempDir()
	leaf := filepath.Join(parent, "not-created-yet")
	canonical, exists, err := CanonicalizeWorkdir(leaf)
	if err != nil {
		t.Fatalf("CanonicalizeWorkdir: %v", err)
	}
	if exists {
		t.Errorf("exists = true for a not-yet-created leaf")
	}
	// The deepest existing ancestor (parent) is resolved and the missing
	// leaf re-appended, so the identity won't shift once it's created.
	parentResolved, _ := filepath.EvalSymlinks(parent)
	if want := filepath.Join(parentResolved, "not-created-yet"); canonical != want {
		t.Errorf("canonical = %q, want %q", canonical, want)
	}
	// Creating the dir must not change the canonical form.
	if err := os.Mkdir(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	after, _, _ := CanonicalizeWorkdir(leaf)
	if after != canonical {
		t.Errorf("canonical shifted after dir creation: %q -> %q", canonical, after)
	}
}

func TestCanonicalizeWorkdir_EmptyErrors(t *testing.T) {
	if _, _, err := CanonicalizeWorkdir("   "); err == nil {
		t.Error("expected error for empty/whitespace path")
	}
}

func TestWorkdir_MountSourceAndIsHost(t *testing.T) {
	host := Workdir{Mode: WorkdirModeHost, Source: "/abs/src", Canonical: "/canon/src"}
	if !host.IsHost() {
		t.Error("host workdir IsHost() = false")
	}
	if host.MountSource() != "/canon/src" {
		t.Errorf("host MountSource = %q, want canonical", host.MountSource())
	}
	ephemeral := NewEphemeralWorkdir("box")
	if ephemeral.IsHost() {
		t.Error("ephemeral workdir IsHost() = true")
	}
	if ephemeral.MountSource() != ephemeral.Source {
		t.Errorf("ephemeral MountSource = %q, want source %q", ephemeral.MountSource(), ephemeral.Source)
	}
}

func TestWorkdir_ContainerPath(t *testing.T) {
	// Host workdirs mirror the canonical host path under /workdir
	// (dropping only the leading slash); ephemeral binds at bare /workdir.
	cases := []struct {
		name string
		wd   Workdir
		want string
	}{
		{
			name: "nested host path",
			wd:   Workdir{Mode: WorkdirModeHost, Source: "/abs/src", Canonical: "/home/me/src/repo"},
			want: "/workdir/home/me/src/repo",
		},
		{
			name: "spaces in path components",
			wd:   Workdir{Mode: WorkdirModeHost, Canonical: "/home/me/my repo/x"},
			want: "/workdir/home/me/my repo/x",
		},
		{
			name: "host root",
			wd:   Workdir{Mode: WorkdirModeHost, Canonical: "/"},
			want: "/workdir",
		},
		{
			name: "host falls back to source when canonical empty",
			wd:   Workdir{Mode: WorkdirModeHost, Source: "/srv/code"},
			want: "/workdir/srv/code",
		},
		{
			name: "ephemeral is bare /workdir",
			wd:   NewEphemeralWorkdir("box"),
			want: "/workdir",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.wd.ContainerPath(); got != tc.want {
				t.Errorf("ContainerPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkdir_ContainerPath_SymlinkCanonical(t *testing.T) {
	// A host workdir built from a symlinked path mirrors its
	// EvalSymlinks-resolved canonical, not the symlink, under /workdir.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	wd, _, err := NewHostWorkdir(link)
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	realResolved, _ := filepath.EvalSymlinks(real)
	want := "/workdir" + realResolved
	if got := wd.ContainerPath(); got != want {
		t.Errorf("ContainerPath() = %q, want %q", got, want)
	}
}

// The agent-state "mode" axis was removed entirely: native state is now
// owned by the selected host-global account at a derived per-agent path. A
// manifest carrying the removed agent_state_mode / agent_state_host_path fields
// must fail CLOSED on strict decode, naming the rejected field, never be
// silently reinterpreted.
func TestOpen_RejectsRemovedAgentStateFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, field := range []string{"agent_state_mode: space", "agent_state_host_path: /tmp/x"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "box.yaml")
		body := "schema_version: 1\nname: box\nspace: default\naccount: none\n" + field + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		_, err := Open(path)
		if err == nil {
			t.Fatalf("manifest with %q should fail strict decode, got nil", field)
		}
		if name, _, _ := strings.Cut(field, ":"); !strings.Contains(err.Error(), name) {
			t.Errorf("error for %q = %v, want it to name the rejected field", field, err)
		}
	}
}
