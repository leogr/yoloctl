package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"gopkg.in/yaml.v3"
)

// copyChanged builds the merge function's `changed` input from a flag-name
// list, so the pure merge is driven without cobra.
func copyChanged(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(name string) bool { return set[name] }
}

// claudeStateDest is the claude profile's StateContainerPath — the managed
// account-state slot target in a claude box's mounts.
const claudeStateDest = "/home/sandbox/.claude"

// copySourceConfig builds a rich claude/default source manifest over the
// given host workdir: user mount + managed state slot, ports, env, extra
// args, custom resources — one representative value per copyable field.
// canonicalTempDir is a test directory without symlinks in its path. A copy
// carries the source's canonical workdir, so a source saved under macOS's
// /var (a link to /private/var) would not compare equal to it.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func copySourceConfig(t *testing.T, wd string) *agentbox.Config {
	t.Helper()
	workdir, _, err := agentbox.NewHostWorkdir(wd)
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	stateSource, err := layout.AccountStatePath("default", "claude")
	if err != nil {
		t.Fatalf("AccountStatePath: %v", err)
	}
	return &agentbox.Config{
		SchemaVersion:  agentbox.SchemaVersion,
		Name:           "src",
		Space:          "default",
		Agent:          "claude",
		Account:        "default",
		Image:          "img:1",
		AgentExtraArgs: []string{"--model", "opus"},
		Workdir:        workdir,
		Resources:      agentbox.Resources{CPUs: "3", Memory: "4g"},
		Features:       []string{"claude", "github"},
		Capabilities:   []string{capabilities.WorkdirWriteCapability, "network.github"},
		Ports:          []agentbox.Port{{Published: 8080, Target: 3000}},
		Env:            map[string]agentbox.EnvVar{"A": {Literal: "b"}},
		Mounts: []agentbox.Mount{
			{Source: "/host/data", Target: "/data", ReadOnly: true},
			{Source: stateSource, Target: claudeStateDest},
			{Source: filepath.Join(stateSource, "anthropic"), Target: "/home/sandbox/.config/anthropic"},
		},
	}
}

// No flags: every copyable setting mirrors the source; the managed state
// slot is stripped (the pipeline re-derives it); read-only derives from the
// effective posture; devcontainer discovery is off; the inherited ports
// produce the collision notice.
func TestCopyOptionsFromSource_NoFlagsMirrorsSource(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := copySourceConfig(t, t.TempDir())

	opts, notices, err := copyOptionsFromSource(copyChanged(), agentboxCreateOptions{}, src, "default", "dst")
	if err != nil {
		t.Fatalf("copyOptionsFromSource: %v", err)
	}
	if opts.Name != "dst" || opts.Space != "default" {
		t.Errorf("identity = %s/%s, want default/dst", opts.Space, opts.Name)
	}
	if !opts.skipDevcontainerPorts {
		t.Error("copy must disable devcontainer port discovery")
	}
	if opts.Agent != "claude" || opts.Account != "default" {
		t.Errorf("agent/account = %s/%s, want claude/default", opts.Agent, opts.Account)
	}
	if opts.Image != "img:1" {
		t.Errorf("image = %q, want img:1", opts.Image)
	}
	if !slices.Equal(opts.AgentExtraArgs, src.AgentExtraArgs) {
		t.Errorf("extra args = %v, want %v", opts.AgentExtraArgs, src.AgentExtraArgs)
	}
	if !slices.Equal(opts.Features, src.Features) || !slices.Equal(opts.Capabilities, src.Capabilities) {
		t.Errorf("features/capabilities = %v/%v, want source's", opts.Features, opts.Capabilities)
	}
	if opts.CPUs != "3" || opts.Memory != "4g" {
		t.Errorf("resources = %s/%s, want 3/4g", opts.CPUs, opts.Memory)
	}
	if opts.ReadOnly {
		t.Error("read-only must derive false from a workdir-writable source")
	}
	if opts.UnconfinedNetwork {
		t.Error("unconfined-network must mirror the source (false)")
	}
	if len(opts.Env) != 1 || opts.Env["A"].Literal != "b" {
		t.Errorf("env = %v, want A=b", opts.Env)
	}
	wantMounts := []agentbox.Mount{{Source: "/host/data", Target: "/data", ReadOnly: true}}
	if !slices.Equal(opts.Mounts, wantMounts) {
		t.Errorf("mounts = %v, want managed slot stripped, user mount kept (%v)", opts.Mounts, wantMounts)
	}
	if !slices.Equal(opts.Ports, src.Ports) {
		t.Errorf("ports = %v, want verbatim %v", opts.Ports, src.Ports)
	}
	if opts.WorkdirMode != agentbox.WorkdirModeHost || opts.Workdir != src.Workdir.MountSource() {
		t.Errorf("workdir = %s (%s), want canonical %s (host)", opts.Workdir, opts.WorkdirMode, src.Workdir.MountSource())
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "8080") {
		t.Errorf("notices = %v, want one ports notice naming 8080", notices)
	}
}

// The behavior flags ride the merge untouched — a rebuilt options struct
// that dropped one of them (a past near-miss for NoAttach) would silently
// re-enable the behavior the user opted out of.
func TestCopyOptionsFromSource_PreservesBehaviorFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := copySourceConfig(t, t.TempDir())
	opts, _, err := copyOptionsFromSource(copyChanged(),
		agentboxCreateOptions{ManifestOnly: true, NoAutoupdate: true, NoAttach: true},
		src, "default", "dst")
	if err != nil {
		t.Fatalf("copyOptionsFromSource: %v", err)
	}
	if !opts.ManifestOnly || !opts.NoAutoupdate || !opts.NoAttach {
		t.Errorf("behavior flags dropped by the merge: manifestOnly=%v noAutoupdate=%v noAttach=%v",
			opts.ManifestOnly, opts.NoAutoupdate, opts.NoAttach)
	}
}

// A copy keeps the source's privileged setting unless --privileged overrides
// it in either direction.
func TestCopyOptionsFromSource_Privileged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := copySourceConfig(t, t.TempDir())
	src.Privileged = true
	for name, tc := range map[string]struct {
		changed []string
		flag    bool
		want    bool
	}{
		"inherited":   {nil, false, true},
		"overridden":  {[]string{"privileged"}, false, false},
		"set to true": {[]string{"privileged"}, true, true},
	} {
		opts, _, err := copyOptionsFromSource(copyChanged(tc.changed...), agentboxCreateOptions{Privileged: tc.flag}, src, "default", "dst")
		if err != nil || opts.Privileged != tc.want {
			t.Errorf("%s: privileged = %v (%v), want %v", name, opts.Privileged, err, tc.want)
		}
	}
	src.Privileged = false
	if opts, _, err := copyOptionsFromSource(copyChanged("privileged"), agentboxCreateOptions{Privileged: true}, src, "default", "dst"); err != nil || !opts.Privileged {
		t.Fatalf("confined source with --privileged = %v (%v)", opts.Privileged, err)
	}
}

func TestCopyOptionsFromSource_ScalarOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := copySourceConfig(t, t.TempDir())

	opts, _, err := copyOptionsFromSource(
		copyChanged("image", "cpus", "memory", "unconfined-network", "account"),
		agentboxCreateOptions{Image: "other:2", CPUs: "8", Memory: "16g", UnconfinedNetwork: true, Account: "personal"},
		src, "default", "dst")
	if err != nil {
		t.Fatalf("copyOptionsFromSource: %v", err)
	}
	if opts.Image != "other:2" || opts.CPUs != "8" || opts.Memory != "16g" || !opts.UnconfinedNetwork {
		t.Errorf("overrides not applied: %+v", opts)
	}
	if opts.Account != "personal" {
		t.Errorf("account = %q, want personal", opts.Account)
	}
	// Untouched flags still mirror the source.
	if opts.Agent != "claude" || !slices.Equal(opts.Features, src.Features) {
		t.Errorf("agent/features changed unexpectedly: %s %v", opts.Agent, opts.Features)
	}
}

// Collection flags replace wholesale; an empty Changed value (the pflag
// yield of `--features ""` / a filtered `--port ""`) clears the inherited
// collection — and a cleared port list must not warn about collisions.
func TestCopyOptionsFromSource_CollectionsReplaceAndClear(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := copySourceConfig(t, t.TempDir())

	replaced, notices, err := copyOptionsFromSource(
		copyChanged("features", "capability", "agent-extra-args", "port", "mount", "env"),
		agentboxCreateOptions{
			Features:       []string{"go"},
			Capabilities:   []string{"network.pypi"},
			AgentExtraArgs: []string{"--fast"},
			Ports:          []agentbox.Port{{Published: 9999, Target: 9999}},
			Mounts:         []agentbox.Mount{{Source: "/other", Target: "/other"}},
			Env:            map[string]agentbox.EnvVar{"B": {Literal: "c"}},
		},
		src, "default", "dst")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !slices.Equal(replaced.Features, []string{"go"}) ||
		!slices.Equal(replaced.Capabilities, []string{"network.pypi"}) ||
		!slices.Equal(replaced.AgentExtraArgs, []string{"--fast"}) ||
		!slices.Equal(replaced.Ports, []agentbox.Port{{Published: 9999, Target: 9999}}) ||
		!slices.Equal(replaced.Mounts, []agentbox.Mount{{Source: "/other", Target: "/other"}}) ||
		replaced.Env["B"].Literal != "c" || len(replaced.Env) != 1 {
		t.Errorf("wholesale replacement failed: %+v", replaced)
	}
	if len(notices) != 0 {
		t.Errorf("explicit --port must not warn: %v", notices)
	}

	cleared, notices, err := copyOptionsFromSource(
		copyChanged("features", "capability", "agent-extra-args", "port", "mount", "env"),
		agentboxCreateOptions{Features: []string{""}, Capabilities: nil, AgentExtraArgs: []string{" "}},
		src, "default", "dst")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Features != nil || cleared.Capabilities != nil || cleared.AgentExtraArgs != nil ||
		cleared.Ports != nil || cleared.Mounts != nil || cleared.Env != nil {
		t.Errorf("empty values must clear the inherited collections: %+v", cleared)
	}
	if len(notices) != 0 {
		t.Errorf("cleared ports must not warn: %v", notices)
	}
}

func TestCopyOptionsFromSource_AgentOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/work")

	t.Run("shell to real agent defaults the account", func(t *testing.T) {
		src := copySourceConfig(t, t.TempDir())
		src.Agent, src.Account = "none", "none"
		src.AgentExtraArgs = nil
		opts, _, err := copyOptionsFromSource(copyChanged("agent"), agentboxCreateOptions{Agent: "claude"}, src, "default", "dst")
		if err != nil {
			t.Fatalf("copyOptionsFromSource: %v", err)
		}
		if opts.Agent != "claude" || opts.Account != "default" {
			t.Errorf("agent/account = %s/%s, want claude/default", opts.Agent, opts.Account)
		}
	})

	t.Run("real agent to none resets derived fields", func(t *testing.T) {
		src := copySourceConfig(t, t.TempDir())
		src.Account = "personal"
		opts, notices, err := copyOptionsFromSource(copyChanged("agent"), agentboxCreateOptions{Agent: "none"}, src, "default", "dst")
		if err != nil {
			t.Fatalf("copyOptionsFromSource: %v", err)
		}
		if opts.Agent != "none" || opts.Account != "none" {
			t.Errorf("agent/account = %s/%s, want none/none", opts.Agent, opts.Account)
		}
		if opts.Image != defaultAgentboxImage {
			t.Errorf("image = %q, want the create default (the old agent's image must not survive)", opts.Image)
		}
		if opts.Features != nil {
			t.Errorf("features = %v, want none", opts.Features)
		}
		if opts.AgentExtraArgs != nil {
			t.Errorf("extra args = %v, want cleared for a generic shell", opts.AgentExtraArgs)
		}
		joined := strings.Join(notices, "\n")
		if !strings.Contains(joined, "personal") || !strings.Contains(joined, "agent_extra_args") {
			t.Errorf("notices = %v, want account drop + extra-args drop", notices)
		}
		// The merged options must survive the real config build: an
		// unstripped managed slot would fail rejectAccountRootBinds here.
		wd, _, err := agentbox.NewHostWorkdir(t.TempDir())
		if err != nil {
			t.Fatalf("NewHostWorkdir: %v", err)
		}
		opts.workdir = wd
		cfg, _, err := buildAgentboxConfig(t.Context(), opts)
		if err != nil {
			t.Fatalf("buildAgentboxConfig on merged options: %v", err)
		}
		if n := countMountsAt(cfg.Mounts, claudeStateDest); n != 0 {
			t.Errorf("managed slot survived the copy to a generic shell: %+v", cfg.Mounts)
		}
	})

	t.Run("agent switch keeps a real account", func(t *testing.T) {
		src := copySourceConfig(t, t.TempDir())
		src.Account = "personal"
		opts, _, err := copyOptionsFromSource(copyChanged("agent"), agentboxCreateOptions{Agent: "codex"}, src, "default", "dst")
		if err != nil {
			t.Fatalf("copyOptionsFromSource: %v", err)
		}
		if opts.Agent != "codex" || opts.Account != "personal" {
			t.Errorf("agent/account = %s/%s, want codex/personal (a real account fits any real agent)", opts.Agent, opts.Account)
		}
		// Image/features re-derivation from the codex profile is RunE's
		// applyAgentProfileDefaults; the merge itself keeps the source values.
		if opts.Image != "img:1" || !slices.Equal(opts.Features, src.Features) {
			t.Errorf("merge must not re-derive image/features itself: %q %v", opts.Image, opts.Features)
		}
	})

	t.Run("explicit contradictory account still errors", func(t *testing.T) {
		src := copySourceConfig(t, t.TempDir())
		_, _, err := copyOptionsFromSource(copyChanged("agent", "account"),
			agentboxCreateOptions{Agent: "claude", Account: "none"}, src, "default", "dst")
		if err == nil || !strings.Contains(err.Error(), "real account") {
			t.Errorf("err = %v, want the real-agents-require-real-accounts error", err)
		}
	})
}

// A read-only source (capabilities without fs.workdir.write) round-trips to
// an identical capability set through the real config build, and
// --read-only=false re-grants write.
func TestCopyOptionsFromSource_ReadOnlyRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd := t.TempDir()
	src := copySourceConfig(t, wd)
	src.Capabilities = []string{"network.github"} // no workdir-write: read-only posture

	opts, _, err := copyOptionsFromSource(copyChanged(), agentboxCreateOptions{}, src, "default", "dst")
	if err != nil {
		t.Fatalf("copyOptionsFromSource: %v", err)
	}
	if !opts.ReadOnly {
		t.Fatal("read-only must derive true from a source without fs.workdir.write")
	}
	opts.workdir = src.Workdir
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if !slices.Equal(cfg.Capabilities, src.Capabilities) {
		t.Errorf("capabilities = %v, want exact round-trip %v", cfg.Capabilities, src.Capabilities)
	}

	rw, _, err := copyOptionsFromSource(copyChanged("read-only"), agentboxCreateOptions{ReadOnly: false}, src, "default", "dst")
	if err != nil {
		t.Fatalf("copyOptionsFromSource: %v", err)
	}
	rw.workdir = src.Workdir
	cfg, _, err = buildAgentboxConfig(t.Context(), rw)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if !slices.Contains(cfg.Capabilities, capabilities.WorkdirWriteCapability) {
		t.Errorf("--read-only=false must re-grant workdir write: %v", cfg.Capabilities)
	}
}

func TestCopyOptionsFromSource_WorkdirPairing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hostSrc := copySourceConfig(t, t.TempDir())
	ephemeralSrc := copySourceConfig(t, t.TempDir())
	ephemeralSrc.Workdir = agentbox.NewEphemeralWorkdir("src")

	for _, tc := range []struct {
		name     string
		src      *agentbox.Config
		changed  func(string) bool
		flags    agentboxCreateOptions
		wantMode string
		wantDir  string
	}{
		{"host source mirrors canonical", hostSrc, copyChanged(), agentboxCreateOptions{},
			agentbox.WorkdirModeHost, hostSrc.Workdir.MountSource()},
		{"ephemeral source re-derives", ephemeralSrc, copyChanged(), agentboxCreateOptions{},
			agentbox.WorkdirModeEphemeral, ""},
		{"workdir flag on ephemeral source implies host", ephemeralSrc, copyChanged("workdir"),
			agentboxCreateOptions{Workdir: "/x"}, agentbox.WorkdirModeHost, "/x"},
		{"ephemeral flag on host source drops the path", hostSrc, copyChanged("workdir-mode"),
			agentboxCreateOptions{WorkdirMode: agentbox.WorkdirModeEphemeral}, agentbox.WorkdirModeEphemeral, ""},
		{"host flag on ephemeral source falls back to space default", ephemeralSrc, copyChanged("workdir-mode"),
			agentboxCreateOptions{WorkdirMode: agentbox.WorkdirModeHost}, agentbox.WorkdirModeHost, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, err := copyOptionsFromSource(tc.changed, tc.flags, tc.src, "default", "dst")
			if err != nil {
				t.Fatalf("copyOptionsFromSource: %v", err)
			}
			if opts.WorkdirMode != tc.wantMode || opts.Workdir != tc.wantDir {
				t.Errorf("workdir = %q (%s), want %q (%s)", opts.Workdir, opts.WorkdirMode, tc.wantDir, tc.wantMode)
			}
		})
	}
}

// Regression: the inherited host workdir is the source's CANONICAL identity,
// not its original (possibly symlinked) path — a retargeted symlink must not
// silently point the copy at a different directory than the source mounts.
func TestCopyOptionsFromSource_SymlinkRetargetKeepsCanonicalWorkdir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realA := filepath.Join(base, "a")
	realB := filepath.Join(base, "b")
	link := filepath.Join(base, "repo")
	for _, dir := range []string{realA, realB} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.Symlink(realA, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	src := copySourceConfig(t, link) // Source=link, Canonical=realA
	if src.Workdir.Canonical != realA {
		t.Fatalf("fixture: canonical = %q, want %q", src.Workdir.Canonical, realA)
	}
	// Retarget the symlink after the source was declared.
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.Symlink(realB, link); err != nil {
		t.Fatalf("retarget: %v", err)
	}

	opts, _, err := copyOptionsFromSource(copyChanged(), agentboxCreateOptions{}, src, "default", "dst")
	if err != nil {
		t.Fatalf("copyOptionsFromSource: %v", err)
	}
	if opts.Workdir != realA {
		t.Errorf("inherited workdir = %q, want the source's canonical %q (not the retargeted link)", opts.Workdir, realA)
	}
	wd, err := buildWorkdir("dst", opts.Workdir, opts.WorkdirMode, io.Discard)
	if err != nil {
		t.Fatalf("buildWorkdir: %v", err)
	}
	if wd.Canonical != realA {
		t.Errorf("resolved canonical = %q, want %q", wd.Canonical, realA)
	}
}

// stubStoppedSpaceResolver makes box-state resolution deterministic without a
// VM: every space reports Stopped, so runtime is Unavailable and declaration
// state comes from the YAML on disk (the same tolerance a real limactl miss
// gets). Mirrors the destroy test's stub.
func stubStoppedSpaceResolver(t *testing.T) {
	t.Helper()
	old := newBoxStateResolver
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: defaultSpaceName, Status: "Stopped"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				t.Fatal("a stopped space must not be queried for containers")
				return nil, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionUnknown}
			},
			InspectActual: func(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
				t.Fatal("manifest-only copy must not inspect actual state")
				return agentbox.ActualState{}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) {
				t.Fatal("manifest-only copy must not compose policy")
				return nil, nil
			},
			ResolvedImage: func(*agentbox.Config) (string, bool, error) {
				t.Fatal("manifest-only copy must not resolve image")
				return "", false, nil
			},
		}
	}
	t.Cleanup(func() { newBoxStateResolver = old })
}

// End-to-end (manifest-only, no VM): the copy lands at the destination
// BoxPath with every setting mirrored, the source YAML is untouched, and
// stdout/stderr carry the copy message + ports notice.
func TestBoxCopy_ManifestOnlyEndToEnd(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", "default").Save(); err != nil {
		t.Fatal(err)
	}
	stubStoppedSpaceResolver(t)
	src := copySourceConfig(t, canonicalTempDir(t))
	if err := src.Save(); err != nil {
		t.Fatalf("save source: %v", err)
	}
	before, err := os.ReadFile(src.Path())
	if err != nil {
		t.Fatalf("read source: %v", err)
	}

	stdout, stderr, err := execCmd(t, newAgentboxCopyCmd(), "", "src", "dst", "--manifest-only")
	if err != nil {
		t.Fatalf("box copy: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, `copied box "src" (space default) to "dst" (space default)`) {
		t.Errorf("stdout = %q, want the copied-box line", stdout)
	}
	if !strings.Contains(stderr, "8080") {
		t.Errorf("stderr = %q, want the inherited-ports notice", stderr)
	}

	dst, err := agentbox.Load("default", "dst")
	if err != nil {
		t.Fatalf("load copy: %v", err)
	}
	if dst.Name != "dst" || dst.Space != "default" {
		t.Errorf("copy identity = %s/%s, want default/dst", dst.Space, dst.Name)
	}
	want := *src
	want.Name, want.Space, want.ID = dst.Name, dst.Space, dst.ID
	wantYAML, err := yaml.Marshal(&want)
	if err != nil {
		t.Fatal(err)
	}
	gotYAML, err := yaml.Marshal(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotYAML) != string(wantYAML) {
		t.Errorf("copy settings differ from source: got %s, want %s", gotYAML, wantYAML)
	}
	if !slices.Equal(dst.Ports, src.Ports) {
		t.Errorf("ports = %v, want verbatim %v", dst.Ports, src.Ports)
	}
	after, err := os.ReadFile(src.Path())
	if err != nil {
		t.Fatalf("re-read source: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("source manifest was modified by the copy:\nbefore=%s\nafter=%s", before, after)
	}
}

// Flag overrides land in the copied manifest while everything else mirrors.
func TestBoxCopy_OverrideEndToEnd(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", "default").Save(); err != nil {
		t.Fatal(err)
	}
	stubStoppedSpaceResolver(t)
	src := copySourceConfig(t, t.TempDir())
	if err := src.Save(); err != nil {
		t.Fatalf("save source: %v", err)
	}

	_, stderr, err := execCmd(t, newAgentboxCopyCmd(), "",
		"src", "ro", "--manifest-only", "--read-only", "--cpus", "8")
	if err != nil {
		t.Fatalf("box copy: %v (stderr: %s)", err, stderr)
	}
	dst, err := agentbox.Load("default", "ro")
	if err != nil {
		t.Fatalf("load copy: %v", err)
	}
	if dst.WorkdirWritable() {
		t.Errorf("--read-only override ignored: %v", dst.Capabilities)
	}
	if dst.Resources.CPUs != "8" || dst.Resources.Memory != "4g" {
		t.Errorf("resources = %+v, want cpus overridden to 8, memory mirrored 4g", dst.Resources)
	}
	if dst.Agent != "claude" || dst.Image != "img:1" {
		t.Errorf("unrelated settings must mirror: agent=%s image=%s", dst.Agent, dst.Image)
	}
}

func TestBoxCopy_CrossSpaceManifestOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", "default").Save(); err != nil {
		t.Fatal(err)
	}
	stubStoppedSpaceResolver(t)
	src := copySourceConfig(t, canonicalTempDir(t))
	src.Space = "other"
	if err := src.Save(); err != nil {
		t.Fatalf("save source: %v", err)
	}

	stdout, stderr, err := execCmd(t, newAgentboxCopyCmd(), "",
		"src", "dst", "--from-space", "other", "--manifest-only")
	if err != nil {
		t.Fatalf("box copy: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, `copied box "src" (space other) to "dst" (space default)`) {
		t.Errorf("stdout = %q, want the cross-space copied-box line", stdout)
	}
	dst, err := agentbox.Load("default", "dst")
	if err != nil {
		t.Fatalf("load copy in destination space: %v", err)
	}
	if dst.Space != "default" {
		t.Errorf("copy space = %q, want the destination default", dst.Space)
	}
	want := *src
	want.Name, want.Space, want.ID = dst.Name, dst.Space, dst.ID
	wantYAML, err := yaml.Marshal(&want)
	if err != nil {
		t.Fatal(err)
	}
	gotYAML, err := yaml.Marshal(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotYAML) != string(wantYAML) {
		t.Errorf("copy settings differ from source: got %s, want %s", gotYAML, wantYAML)
	}
}

// The copy takes the source's persisted port snapshot verbatim: a
// devcontainer.json in the (shared) workdir must NOT be re-read.
func TestBoxCopy_DoesNotRereadDevcontainer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", "default").Save(); err != nil {
		t.Fatal(err)
	}
	stubStoppedSpaceResolver(t)
	wd := t.TempDir()
	src := copySourceConfig(t, wd)
	if err := src.Save(); err != nil {
		t.Fatalf("save source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wd, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wd, ".devcontainer", "devcontainer.json"),
		[]byte(`{"forwardPorts": [9090]}`), 0o644); err != nil {
		t.Fatalf("write devcontainer.json: %v", err)
	}

	_, stderr, err := execCmd(t, newAgentboxCopyCmd(), "", "src", "dst", "--manifest-only")
	if err != nil {
		t.Fatalf("box copy: %v (stderr: %s)", err, stderr)
	}
	dst, err := agentbox.Load("default", "dst")
	if err != nil {
		t.Fatalf("load copy: %v", err)
	}
	if !slices.Equal(dst.Ports, src.Ports) {
		t.Errorf("ports = %v, want the source snapshot %v (devcontainer.json must not be re-read)", dst.Ports, src.Ports)
	}
}

func TestBoxCopy_Errors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubStoppedSpaceResolver(t)
	src := copySourceConfig(t, t.TempDir())
	if err := src.Save(); err != nil {
		t.Fatalf("save source: %v", err)
	}
	seedAgentbox(t, "default", "taken")

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"missing source", []string{"ghost", "dst", "--manifest-only"}, "not found in space"},
		{"destination exists", []string{"src", "taken", "--manifest-only"}, "already exists"},
		{"self copy", []string{"src", "src", "--manifest-only"}, "onto itself"},
		{"invalid new name", []string{"src", "bad/name", "--manifest-only"}, "invalid agentbox name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := execCmd(t, newAgentboxCopyCmd(), "", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want mention of %q", err, tc.wantErr)
			}
		})
	}
}

// skipDevcontainerPorts turns discovery off entirely (the copy path).
func TestBuildAgentboxConfig_SkipDevcontainerPorts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wd, ".devcontainer", "devcontainer.json"),
		[]byte(`{"forwardPorts": [9090]}`), 0o644); err != nil {
		t.Fatalf("write devcontainer.json: %v", err)
	}
	workdir, _, err := agentbox.NewHostWorkdir(wd)
	if err != nil {
		t.Fatalf("NewHostWorkdir: %v", err)
	}
	opts := agentboxCreateOptions{
		Space:                 "default",
		Name:                  "coder",
		Image:                 "img:1",
		Ports:                 []agentbox.Port{{Published: 8080, Target: 3000}},
		workdir:               workdir,
		skipDevcontainerPorts: true,
	}
	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if want := []agentbox.Port{{Published: 8080, Target: 3000}}; !slices.Equal(cfg.Ports, want) {
		t.Errorf("ports = %v, want only the explicit %v (discovery off)", cfg.Ports, want)
	}
}
