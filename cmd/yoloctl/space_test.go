package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	agentboxcfg "github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestWriteSpaceShow(t *testing.T) {
	data := spaceShowFixture()
	var buf bytes.Buffer
	if err := writeSpaceShow(&buf, data); err != nil {
		t.Fatalf("writeSpaceShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:       default",
		"Status:     Running",
		"Config:     /home/test/.config/yoloctl/lima/yolo-default/lima.yaml",
		"Host log:   /home/test/.config/yoloctl/lima/yolo-default/ha.stdout.log",
		"Workdirs:",
		"/work/app",
		"coder,reviewer",
		"Configured: coder, scratch",
		"Runtime:",
		"yoloctl/agentbox:ubuntu-24.04",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("writeSpaceShow output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteSpaceShowJSON(t *testing.T) {
	data := spaceShowFixture()
	var buf bytes.Buffer
	if err := writeSpaceShowJSON(&buf, data); err != nil {
		t.Fatalf("writeSpaceShowJSON: %v", err)
	}
	var got struct {
		Name             string `json:"name"`
		Status           string `json:"status"`
		MemoryBytes      uint64 `json:"memory_bytes"`
		ConfigPath       string `json:"config_path"`
		HostAgentLogPath string `json:"host_agent_log_path"`
		Workdirs         []struct {
			Workdir    string   `json:"workdir"`
			Mode       string   `json:"mode"`
			Agentboxes []string `json:"boxes"`
		} `json:"workdirs"`
		Agentboxes struct {
			Configured      []string `json:"configured"`
			ConfiguredCount int      `json:"configured_count"`
			Runtime         []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Image  string `json:"image"`
			} `json:"runtime"`
			RuntimeCount  int    `json:"runtime_count"`
			RuntimeStatus string `json:"runtime_status"`
		} `json:"boxes"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode json: %v\n%s", err, buf.String())
	}
	if got.Name != "default" || got.Status != "Running" {
		t.Fatalf("identity = %s/%s, want default/Running", got.Name, got.Status)
	}
	if got.MemoryBytes != 4*1024*1024*1024 {
		t.Fatalf("memory_bytes = %d", got.MemoryBytes)
	}
	if got.ConfigPath != "/home/test/.config/yoloctl/lima/yolo-default/lima.yaml" {
		t.Fatalf("config_path = %q", got.ConfigPath)
	}
	if len(got.Workdirs) != 1 || got.Workdirs[0].Workdir != "/work/app" || len(got.Workdirs[0].Agentboxes) != 2 {
		t.Fatalf("workdirs = %+v", got.Workdirs)
	}
	if got.Agentboxes.ConfiguredCount != 2 || got.Agentboxes.RuntimeCount != 1 || got.Agentboxes.RuntimeStatus != "ok" {
		t.Fatalf("agentboxes = %+v", got.Agentboxes)
	}
}

func TestWriteSpaceShowJSONEmptySlices(t *testing.T) {
	data := &spaceShowData{
		Instance: vmhost.Instance{
			Name:   "stopped",
			Status: "Stopped",
			Dir:    "/home/test/.config/yoloctl/lima/yolo-stopped",
		},
		ConfigPath:       "/home/test/.config/yoloctl/lima/yolo-stopped/lima.yaml",
		HostAgentLogPath: "/home/test/.config/yoloctl/lima/yolo-stopped/ha.stdout.log",
		RuntimeStatus:    "skipped",
	}
	var buf bytes.Buffer
	if err := writeSpaceShowJSON(&buf, data); err != nil {
		t.Fatalf("writeSpaceShowJSON: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`"workdirs": []`,
		`"configured": []`,
		`"runtime": []`,
		`"runtime_status": "skipped"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("json missing %s:\n%s", want, out)
		}
	}
}

func TestSpaceShowJSONDeclarationAvailability(t *testing.T) {
	for _, metadataOnly := range []bool{false, true} {
		data := spaceShowFixture()
		data.MetadataOnly = metadataOnly
		var out bytes.Buffer
		if err := writeSpaceShowJSON(&out, data); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Available *bool           `json:"declarations_available"`
			Workdirs  json.RawMessage `json:"workdirs"`
			Boxes     struct {
				Configured      json.RawMessage `json:"configured"`
				ConfiguredCount int             `json:"configured_count"`
				Runtime         json.RawMessage `json:"runtime"`
				RuntimeCount    int             `json:"runtime_count"`
			} `json:"boxes"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Available == nil || *got.Available == metadataOnly {
			t.Fatalf("metadata-only=%v: missing/incorrect declaration availability: %s", metadataOnly, &out)
		}
		if metadataOnly && (string(got.Workdirs) != "null" || string(got.Boxes.Configured) != "null" ||
			string(got.Boxes.Runtime) != "null" || got.Boxes.ConfiguredCount != -1 || got.Boxes.RuntimeCount != -1) {
			t.Fatalf("unavailable declarations presented as an empty inventory: %s", &out)
		}
	}
}

func TestSplitSpaceShellArgs(t *testing.T) {
	// join collapses nil and empty to "" so the table can ignore the
	// nil-vs-empty-slice distinction that DeepEqual would trip on.
	join := func(s []string) string { return strings.Join(s, "\x00") }
	cases := []struct {
		name       string
		args       []string
		dash       int
		wantSpace  []string
		wantRemote []string
		wantErr    bool
	}{
		{"no args", nil, -1, nil, nil, false},
		{"space only", []string{"prod"}, -1, []string{"prod"}, nil, false},
		{"extra positionals without dash", []string{"prod", "ls"}, -1, nil, nil, true},
		{"dash leading, no space", []string{"ls", "-la"}, 0, nil, []string{"ls", "-la"}, false},
		{"space then command", []string{"prod", "ls", "-la"}, 1, []string{"prod"}, []string{"ls", "-la"}, false},
		{"trailing dash, empty command", []string{"prod"}, 1, []string{"prod"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSpace, gotRemote, err := splitSpaceShellArgs(tc.args, tc.dash)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got space=%v remote=%v", gotSpace, gotRemote)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if join(gotSpace) != join(tc.wantSpace) {
				t.Fatalf("space = %v, want %v", gotSpace, tc.wantSpace)
			}
			if join(gotRemote) != join(tc.wantRemote) {
				t.Fatalf("remote = %v, want %v", gotRemote, tc.wantRemote)
			}
		})
	}
}

func TestSpaceShellLimaArgs(t *testing.T) {
	join := func(s []string) string { return strings.Join(s, "\x00") }
	cases := []struct {
		name   string
		space  string
		root   bool
		remote []string
		want   []string
	}{
		{
			"interactive user shell",
			"default", false, nil,
			[]string{"shell", vmhost.InstanceName("default")},
		},
		{
			"interactive root shell",
			"default", true, nil,
			[]string{"shell", vmhost.InstanceName("default"), "--", "sudo", "-i"},
		},
		{
			"command as user",
			"default", false, []string{"uname", "-a"},
			[]string{"shell", vmhost.InstanceName("default"), "--", "uname", "-a"},
		},
		{
			"command as root",
			"prod", true, []string{"journalctl", "-u", "containerd"},
			[]string{"shell", vmhost.InstanceName("prod"), "--", "sudo", "journalctl", "-u", "containerd"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spaceShellLimaArgs(tc.space, tc.root, tc.remote)
			if join(got) != join(tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
		})
	}
}

func spaceShowFixture() *spaceShowData {
	const gib = 1024 * 1024 * 1024
	return &spaceShowData{
		Instance: vmhost.Instance{
			Name:   "default",
			Status: "Running",
			VMType: "qemu",
			Arch:   "x86_64",
			CPUs:   4,
			Memory: 4 * gib,
			Disk:   40 * gib,
			Dir:    "/home/test/.config/yoloctl/lima/yolo-default",
		},
		ConfigPath:           "/home/test/.config/yoloctl/lima/yolo-default/lima.yaml",
		HostAgentLogPath:     "/home/test/.config/yoloctl/lima/yolo-default/ha.stdout.log",
		Workdirs:             []spaceShowWorkdir{{Workdir: "/work/app", Mode: agentboxcfg.WorkdirModeHost, Agentboxes: []string{"coder", "reviewer"}}},
		ConfiguredAgentboxes: []string{"coder", "scratch"},
		RuntimeStatus:        "ok",
		RuntimeAgentboxes: []spaceShowRuntimeAgentbox{
			{Name: "coder", Status: "Up", Image: "yoloctl/agentbox:ubuntu-24.04"},
		},
	}
}

// agentboxHostMounts is the single source of truth for both the Lima set
// and the preflight mount-live checks, so a path it returns is exactly a
// path that gets checked. It must include the workdir, the resolved
// account-state dir, and every extra_mount source.
func TestAgentboxHostMounts_CoversWorkdirStateAndExtras(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	wd := t.TempDir()
	cfg := &agentboxcfg.Config{Name: "coder", Space: "default", Agent: "claude", Account: "default"}
	workdir, _, err := agentboxcfg.NewHostWorkdir(wd)
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	cfg.Workdir = workdir
	cfg.Mounts = []agentboxcfg.Mount{{Source: "/host/data", Target: "/data"}}

	wantState, err := layout.AccountStatePath("default", "claude")
	if err != nil {
		t.Fatalf("AccountStatePath: %v", err)
	}
	got := hostMountPaths(agentboxHostMounts(cfg))
	assertHasPath(t, got, cfg.Workdir.MountSource())
	if slices.Contains(got, wantState) {
		t.Fatal("native state exposed as host mount")
	}
	assertHasPath(t, got, "/host/data")

	// Ephemeral workdirs are VM-local and must NOT be a Lima mount.
	ephemeral := &agentboxcfg.Config{
		Name: "bot", Space: "default",
		Workdir: agentboxcfg.NewEphemeralWorkdir("bot"),
	}
	for _, hm := range agentboxHostMounts(ephemeral) {
		if hm.Path == ephemeral.Workdir.Source {
			t.Errorf("ephemeral workdir source must not be a host mount, got %v", agentboxHostMounts(ephemeral))
		}
	}
}

// hostMountPaths projects the paths out of a []hostMount for path-set
// assertions (the writability tag is checked separately where it matters).
func hostMountPaths(ms []hostMount) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Path
	}
	return out
}

// hostMountWritable returns the Writable tag of the entry at path, and whether
// such an entry exists.
func hostMountWritable(ms []hostMount, path string) (writable, found bool) {
	for _, m := range ms {
		if m.Path == path {
			return m.Writable, true
		}
	}
	return false, false
}

// A stale managed account-state mount left in Mounts (from a since-changed
// account that hasn't been reconciled — e.g. a hand edit, or an account flip
// while the space was stopped) must NOT leak alongside the freshly-resolved
// one. agentboxHostMounts skips the managed slot in Mounts and takes the
// source resolved from the CURRENT account instead, so space start (via
// spaceWritableMounts) mounts exactly one state dir.
func TestAgentboxHostMounts_DropsStaleManagedStateSlot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const stateDest = "/home/sandbox/.claude" // claude profile StateContainerPath

	wd := t.TempDir()
	// The box was re-pointed to account "personal", but Mounts still carries
	// the OLD default-account managed slot at the same target (simulating a
	// hand edit / account flip with no reconcile).
	cfg := &agentboxcfg.Config{Name: "coder", Space: "default", Agent: "claude", Account: "personal"}
	workdir, _, err := agentboxcfg.NewHostWorkdir(wd)
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	cfg.Workdir = workdir
	staleLeaf, err := layout.AccountStatePath("default", "claude")
	if err != nil {
		t.Fatalf("AccountStatePath: %v", err)
	}
	cfg.Mounts = []agentboxcfg.Mount{{Source: staleLeaf, Target: stateDest}}

	wantLeaf, err := layout.AccountStatePath("personal", "claude")
	if err != nil {
		t.Fatalf("AccountStatePath: %v", err)
	}
	paths := hostMountPaths(agentboxHostMounts(cfg))
	if slices.Contains(paths, wantLeaf) {
		t.Fatal("native state exposed as host mount")
	}
	if slices.Contains(paths, staleLeaf) {
		t.Errorf("stale account-state leaf leaked into host mounts: %v", paths)
	}
}

// Finding 2 (Medium): a read_only mount is tagged non-writable so the
// preflight probes it readable (test -r), not writable (test -w) — its host
// source may legitimately be unwritable. A writable mount stays writable.
func TestAgentboxHostMounts_ReadOnlyExtraNotRequiredWritable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := &agentboxcfg.Config{
		Name: "coder", Space: "default",
		Workdir: agentboxcfg.NewEphemeralWorkdir("coder"),
	}
	cfg.Mounts = []agentboxcfg.Mount{
		{Source: "/host/ro", Target: "/ro", ReadOnly: true},
		{Source: "/host/rw", Target: "/rw"},
	}

	ms := agentboxHostMounts(cfg)
	if w, ok := hostMountWritable(ms, "/host/ro"); !ok || w {
		t.Errorf("read_only extra_mount /host/ro: writable=%v found=%v, want writable=false", w, ok)
	}
	if w, ok := hostMountWritable(ms, "/host/rw"); !ok || !w {
		t.Errorf("writable extra_mount /host/rw: writable=%v found=%v, want writable=true", w, ok)
	}
}

// TestPurgeSpaceBundle_Boundaries asserts `space destroy --purge` deletes
// the whole yoloctl-owned per-space bundle (space.yaml, box YAMLs) and clears
// the context pin — while leaving accounts + their account-state (host-global
// outside every bundle) and OTHER spaces' bundles untouched.
func TestPurgeSpaceBundle_Boundaries(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	const target = "purgeme"
	// Bundle: space.yaml + an agentbox YAML.
	saveSpaceForTest(t, target, "/home/me/code")
	box := &agentboxcfg.Config{
		Name: "coder", Space: target,
		Image:   "img:1",
		Workdir: agentboxcfg.NewEphemeralWorkdir("coder"),
	}
	if err := box.Save(); err != nil {
		t.Fatalf("save agentbox: %v", err)
	}
	// Things that must SURVIVE: a second space, and account manifest + state
	// (both live outside any space bundle).
	saveSpaceForTest(t, "keepme", "/home/me/other")
	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	// Pin context at the target so destroy must clear it.
	ctx, err := config.LoadContext()
	if err != nil {
		t.Fatal(err)
	}
	ctx.Space = target
	if err := ctx.Save(); err != nil {
		t.Fatal(err)
	}

	if err := purgeSpaceBundle(target, io.Discard); err != nil {
		t.Fatalf("purgeSpaceBundle: %v", err)
	}

	// Gone: the whole target bundle.
	bundle, _ := layout.SpaceBundleDir(target)
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("space bundle %s should be removed, stat err=%v", bundle, err)
	}
	// Context pin cleared.
	if ctx, err := config.LoadContext(); err != nil {
		t.Fatal(err)
	} else if ctx.Space != "" {
		t.Errorf("context pin = %q, want cleared", ctx.Space)
	}
	// Preserved: other space bundle, account manifest + account state.
	keep, _ := layout.SpaceBundleDir("keepme")
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("other space bundle %s must survive purge: %v", keep, err)
	}
	if ok, _ := account.Exists("default", "personal"); !ok {
		t.Error("account manifest must survive purge")
	}

}

// saveSpaceForTest writes a space config under the test's XDG_CONFIG_HOME.
func saveSpaceForTest(t *testing.T, name, defaultWorkdir string, mounts ...spaceconfig.Mount) {
	t.Helper()
	if len(mounts) == 0 {
		mounts = []spaceconfig.Mount{{Source: defaultWorkdir, Writable: true}}
	}
	scfg := &spaceconfig.Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: name, DefaultWorkdir: defaultWorkdir, Mounts: mounts}
	if err := scfg.Save(); err != nil {
		t.Fatalf("save space %s: %v", name, err)
	}
}

// Space owns Lima mounts: spaceWritableMounts derives ONLY from the space
// config grants plus the managed internal account-state root — never from
// agentbox YAMLs, and never a blanket home mount.
func TestSpaceWritableMounts_FromSpaceConfigPlusManagedRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/home/me/code",
		spaceconfig.Mount{Source: "/home/me/code", Writable: true},
		spaceconfig.Mount{Source: "/data", Writable: false},
	)

	mounts, err := spaceWritableMounts("default")
	if err != nil {
		t.Fatalf("spaceWritableMounts: %v", err)
	}
	byLoc := map[string]bool{}
	for _, m := range mounts {
		byLoc[m.Location] = m.Writable
	}
	wantRoot, err := layout.AccountStateRoot()
	if err != nil {
		t.Fatalf("AccountStateRoot: %v", err)
	}
	bundle, _ := layout.SpaceBundleDir("default")
	if _, ok := byLoc["/home/me/code"]; !ok {
		t.Errorf("missing the default-workdir grant; mounts=%v", mounts)
	}
	if w, ok := byLoc["/data"]; !ok || w {
		t.Errorf("/data grant should be present and read-only; mounts=%v", mounts)
	}
	if _, ok := byLoc[wantRoot]; ok {
		t.Errorf("native root exposed as a host mount %s; mounts=%v", wantRoot, mounts)
	}
	// No blanket home mount, and never the whole space bundle (which would
	// expose agentbox YAMLs into the VM) — only the account-state subdir.
	home, _ := os.UserHomeDir()
	for loc := range byLoc {
		if loc == home || loc == "~" || loc == bundle {
			t.Errorf("space must not mount %q (blanket home / whole bundle); mounts=%v", loc, mounts)
		}
	}
}

// Mounts are a property of the SPACE, not its agentboxes: saving a box (with
// an account, a workdir, or an extra mount) does not change the Lima mount
// set — every account leaf rides the single managed account-state root, so no
// box ever needs a per-box Lima mount.
func TestSpaceWritableMounts_IgnoresAccountAgentboxes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/home/me/code")

	before, err := spaceWritableMounts("default")
	if err != nil {
		t.Fatalf("spaceWritableMounts: %v", err)
	}

	// A space-account box + a host workdir + an extra mount — none of which may
	// alter the space's Lima mounts (the workdir/extra are box binds validated
	// against grants, not added to the space; the account is space-scoped).
	box := &agentboxcfg.Config{Name: "coder", Space: "default", Agent: "claude", Account: "default", Image: "img:1"}
	workdir, _, err := agentboxcfg.NewHostWorkdir(t.TempDir())
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	box.Workdir = workdir
	box.Mounts = []agentboxcfg.Mount{{Source: "/host/extra", Target: "/extra"}}
	if err := box.Save(); err != nil {
		t.Fatalf("save agentbox: %v", err)
	}

	after, err := spaceWritableMounts("default")
	if err != nil {
		t.Fatalf("spaceWritableMounts: %v", err)
	}
	if !slices.Equal(before, after) {
		t.Errorf("agentboxes changed the space mount set:\nbefore=%v\nafter=%v", before, after)
	}
}

// A not-yet-configured space contributes only the single managed
// account-state root — never a user grant or a blanket home mount.
func TestSpaceWritableMounts_UnconfiguredSpace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := spaceWritableMounts("ghost"); !os.IsNotExist(err) {
		t.Fatalf("missing space: %v", err)
	}
}

// resolveEffectiveWorkdir: --workdir-mode ephemeral → VM-local; no --workdir →
// the space's default workdir; the space must already be configured.
func TestResolveEffectiveWorkdir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd := t.TempDir()
	saveSpaceForTest(t, "default", wd)
	// MountSource returns the canonical (symlink-resolved) path. On macOS
	// t.TempDir() lives under /var, a symlink to /private/var, so compare
	// against the same canonicalization the resolver applies (a no-op on
	// Linux, where the temp dir has no symlink components).
	wantWd, _, err := agentboxcfg.CanonicalizeWorkdir(wd)
	if err != nil {
		t.Fatalf("canonicalize workdir: %v", err)
	}

	// No --workdir, host mode → space default workdir (host mode).
	hostWd, err := resolveEffectiveWorkdir("default", "coder", "", agentboxcfg.WorkdirModeHost, io.Discard)
	if err != nil {
		t.Fatalf("resolveEffectiveWorkdir(default): %v", err)
	}
	if !hostWd.IsHost() || hostWd.MountSource() != wantWd {
		t.Errorf("default workdir = %+v, want host %s", hostWd, wantWd)
	}

	// --workdir-mode ephemeral → VM-local, no host source.
	ephemeral, err := resolveEffectiveWorkdir("default", "bot", "", agentboxcfg.WorkdirModeEphemeral, io.Discard)
	if err != nil {
		t.Fatalf("resolveEffectiveWorkdir(ephemeral): %v", err)
	}
	if ephemeral.IsHost() {
		t.Errorf("ephemeral should not be a host workdir: %+v", ephemeral)
	}

	// Unconfigured space → hard error (box commands never invent a default).
	if _, err := resolveEffectiveWorkdir("ghost", "coder", "", agentboxcfg.WorkdirModeHost, io.Discard); err == nil {
		t.Error("expected an error for an unconfigured space's default workdir")
	}
}

// `space create` requires --workdir, and the check fires before any VM or
// disk work — so it's testable without Lima.
func TestSpaceCreate_RequiresWorkdir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := newSpaceCreateCmd()
	cmd.SetArgs([]string{"testspace"}) // no --workdir
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("space create without --workdir should error")
	}
	if !strings.Contains(err.Error(), "workdir") {
		t.Errorf("error = %v, want it to mention the required workdir", err)
	}
	// Nothing should have been persisted.
	if exists, _ := spaceconfig.Exists("testspace"); exists {
		t.Error("a refused create must not save a space config")
	}
}

func TestSpaceCreate_ExistingConfigSuggestsStartOrDestroy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/home/me/code")

	cmd := newSpaceCreateCmd()
	cmd.SetArgs([]string{"default", "--workdir", "/home/me/code", "--account", "personal"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("space create with an existing config should error")
	}
	for _, want := range []string{
		"space start",
		"space recreate",
		"space destroy",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestSpaceMountAdd_DeclinedRestartChangesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defaultWorkdir := t.TempDir()
	saveSpaceForTest(t, "default", defaultWorkdir)
	mountPath := filepath.Join(t.TempDir(), "new-mount")

	var lifecycleCalls []string
	restoreSpaceMountLifecycle(t,
		func(context.Context, string) (vmhost.Instance, bool, error) {
			return vmhost.Instance{Name: "default", Status: "Running"}, true, nil
		},
		func(context.Context, string) ([]container.Container, error) {
			return []container.Container{{Name: "coder", Status: "Up 2 minutes"}}, nil
		},
		func(context.Context, string) error {
			lifecycleCalls = append(lifecycleCalls, "stop")
			return nil
		},
		func(string) error {
			lifecycleCalls = append(lifecycleCalls, "refresh")
			return nil
		},
		func(context.Context, string) error {
			lifecycleCalls = append(lifecycleCalls, "start")
			return nil
		},
	)

	var stderr bytes.Buffer
	cmd := newSpaceMountAddCmd()
	if flag := cmd.Flags().Lookup("restart"); flag != nil {
		t.Fatalf("space mount add still exposes removed --restart flag: %+v", flag)
	}
	cmd.SetArgs([]string{"default", mountPath})
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("declined mount add error = %v, want nothing-changed error", err)
	}
	if len(lifecycleCalls) != 0 {
		t.Fatalf("declined mount add lifecycle calls = %v, want none", lifecycleCalls)
	}
	if _, err := os.Stat(mountPath); !os.IsNotExist(err) {
		t.Fatalf("declined mount add created %s, stat err=%v", mountPath, err)
	}
	scfg, err := spaceconfig.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := scfg.GrantFor(mountPath); ok {
		t.Fatalf("declined mount add persisted grant for %s", mountPath)
	}
	for _, want := range []string{
		"coder (Up 2 minutes)",
		"restart space default and apply the mount change?",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestSpaceMountAdd_RestartsRunningSpace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defaultWorkdir := t.TempDir()
	// macOS temp paths can contain /var -> /private/var. Grants use the
	// canonical host path, regardless of how the caller spells it.
	mountPath, _, err := agentboxcfg.CanonicalizeWorkdir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saveSpaceForTest(t, "default", defaultWorkdir)

	var lifecycleCalls []string
	restoreSpaceMountLifecycle(t,
		func(context.Context, string) (vmhost.Instance, bool, error) {
			return vmhost.Instance{Name: "default", Status: "Running"}, true, nil
		},
		func(context.Context, string) ([]container.Container, error) {
			return []container.Container{
				{Name: "coder", Status: "Up 2 minutes"},
				{Name: "stopped", Status: "Exited (0)"},
			}, nil
		},
		func(context.Context, string) error {
			lifecycleCalls = append(lifecycleCalls, "stop")
			return nil
		},
		func(string) error {
			lifecycleCalls = append(lifecycleCalls, "refresh")
			return nil
		},
		func(context.Context, string) error {
			lifecycleCalls = append(lifecycleCalls, "start")
			return nil
		},
	)

	var stderr bytes.Buffer
	cmd := newSpaceMountAddCmd()
	cmd.SetArgs([]string{"default", mountPath})
	cmd.SetIn(strings.NewReader("yes\n"))
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("space mount add: %v", err)
	}
	if want := []string{"stop", "refresh", "start"}; !slices.Equal(lifecycleCalls, want) {
		t.Fatalf("mount add lifecycle calls = %v, want %v", lifecycleCalls, want)
	}
	scfg, err := spaceconfig.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	if grant, ok := scfg.GrantFor(mountPath); !ok || grant.Source != mountPath || !grant.Writable {
		t.Fatalf("mount add grant = %+v, found=%v", grant, ok)
	}
	if strings.Contains(stderr.String(), "stopped (Exited") {
		t.Fatalf("restart warning should list only running boxes:\n%s", stderr.String())
	}
}

func TestSpaceMountRemove_YesRestartsRunningSpace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defaultWorkdir := t.TempDir()
	mountPath, _, err := agentboxcfg.CanonicalizeWorkdir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saveSpaceForTest(t, "default", defaultWorkdir,
		spaceconfig.Mount{Source: defaultWorkdir, Writable: true},
		spaceconfig.Mount{Source: mountPath, Writable: true},
	)

	var lifecycleCalls []string
	restoreSpaceMountLifecycle(t,
		func(context.Context, string) (vmhost.Instance, bool, error) {
			return vmhost.Instance{Name: "default", Status: "Running"}, true, nil
		},
		func(context.Context, string) ([]container.Container, error) {
			return nil, nil
		},
		func(context.Context, string) error {
			lifecycleCalls = append(lifecycleCalls, "stop")
			return nil
		},
		func(string) error {
			lifecycleCalls = append(lifecycleCalls, "refresh")
			return nil
		},
		func(context.Context, string) error {
			lifecycleCalls = append(lifecycleCalls, "start")
			return nil
		},
	)

	cmd := newSpaceMountRemoveCmd()
	if flag := cmd.Flags().Lookup("restart"); flag != nil {
		t.Fatalf("space mount remove still exposes removed --restart flag: %+v", flag)
	}
	cmd.SetArgs([]string{"default", mountPath, "--yes"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("space mount remove --yes: %v", err)
	}
	if want := []string{"stop", "refresh", "start"}; !slices.Equal(lifecycleCalls, want) {
		t.Fatalf("mount remove lifecycle calls = %v, want %v", lifecycleCalls, want)
	}
	scfg, err := spaceconfig.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range scfg.Mounts {
		if mount.Source == mountPath {
			t.Fatalf("mount remove preserved grant %+v", mount)
		}
	}
}

func restoreSpaceMountLifecycle(
	t *testing.T,
	find func(context.Context, string) (vmhost.Instance, bool, error),
	list func(context.Context, string) ([]container.Container, error),
	stop func(context.Context, string) error,
	refresh func(string) error,
	start func(context.Context, string) error,
) {
	t.Helper()
	oldFind := spaceMountFindInstance
	oldList := spaceMountListBoxes
	oldStop := spaceMountStop
	oldRefresh := spaceMountRefresh
	oldStart := spaceMountStart
	spaceMountFindInstance = find
	spaceMountListBoxes = list
	spaceMountStop = stop
	spaceMountRefresh = refresh
	spaceMountStart = start
	t.Cleanup(func() {
		spaceMountFindInstance = oldFind
		spaceMountListBoxes = oldList
		spaceMountStop = oldStop
		spaceMountRefresh = oldRefresh
		spaceMountStart = oldStart
	})
}

// classifyBindGrant is the pure, config-only half of ensureBindGranted (the
// live `test -w/-r` probe needs a real VM). Grant authority comes from the
// space config — the source of truth — so a path that isn't granted is
// rejected regardless of whether it happens to be live in the VM.
func TestClassifyBindGrant(t *testing.T) {
	cfg := &spaceconfig.Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Mounts: []spaceconfig.Mount{
		{Source: "/data", Writable: false},   // read-only parent
		{Source: "/data/rw", Writable: true}, // writable child (more specific)
		{Source: "/code", Writable: true},
	}}
	cases := []struct {
		name     string
		cfg      *spaceconfig.Config
		path     string
		writable bool
		want     bindGrantStatus
	}{
		{"nil config", nil, "/code", true, bindNotGranted},
		{"ungranted path", cfg, "/elsewhere", true, bindNotGranted},
		{"writable bind under writable grant", cfg, "/code/repo", true, bindGrantOK},
		{"writable bind under read-only grant", cfg, "/data/x", true, bindReadOnlyConflict},
		{"read-only bind under read-only grant", cfg, "/data/x", false, bindGrantOK},
		// Teeth for the most-specific GrantFor fix: without it, the read-only
		// /data parent would mask the writable /data/rw child and this would
		// wrongly classify as a conflict.
		{"writable bind under most-specific writable child", cfg, "/data/rw/x", true, bindGrantOK},
	}
	for _, tc := range cases {
		if got := classifyBindGrant(tc.cfg, tc.path, tc.writable); got != tc.want {
			t.Errorf("%s: classifyBindGrant(%q, writable=%v) = %d, want %d",
				tc.name, tc.path, tc.writable, got, tc.want)
		}
	}
}
