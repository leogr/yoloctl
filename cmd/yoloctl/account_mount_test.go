package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

// Native state must never appear in the host mount grant set.
func TestAccountStateIsNotAHostMount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/work")
	mounts, err := spaceWritableMounts("default")
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Location != "/work" {
		t.Fatalf("host mounts = %v", mounts)
	}
	cfg, err := effectiveSpaceConfig("default")
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := layout.AccountStatePath("personal", "claude")
	if classifyBindGrant(cfg, leaf, true) != bindNotGranted {
		t.Fatal("guest storage gained host mount authority")
	}
}

// TestAccountBox_ContainerBindStaysNarrow: although the VM mounts the whole
// account-state root, the box's own account-state bind SOURCE is its narrow
// per-agent leaf (account-state/<account>/<agent>), never the root — so a
// non-escaped agent sees only its own identity (the VM-level breadth is
// escape-only, the accepted VM-level tradeoff).
func TestAccountBox_ContainerBindStaysNarrow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/work")
	if err := account.New("default", account.Default).Save(); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{
		Name: "test-leo", Space: "default", Agent: "claude",
		Account: account.Default,
		Workdir: agentbox.Workdir{Mode: agentbox.WorkdirModeHost, Source: "/work", Canonical: "/work"},
	}
	mount, _, err := applyAgentStateMount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	leaf := mount.Source
	wantLeaf, _ := layout.AccountStatePath("default", "claude")
	if leaf != wantLeaf {
		t.Errorf("container bind source = %q, want the narrow leaf %q", leaf, wantLeaf)
	}
	root, _ := layout.AccountStateRoot()
	if leaf == root {
		t.Errorf("container bind must be the narrow leaf, not the account-state root %q", root)
	}
	if !strings.HasPrefix(leaf, root) {
		t.Errorf("leaf %q should live under the mounted root %q", leaf, root)
	}
}

// TestRejectAccountRootBinds is the "liveness is not grant authority"
// guard: the managed root is always Lima-mounted, but an ORDINARY bind —
// workdir or --mount — may never intersect it. Negative cases: the root
// itself, a SIBLING account's leaf, and a parent directory that would expose
// the root. The box's own internally derived leaf (the managed slot) stays
// allowed.
func TestRejectAccountRootBinds(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := layout.AccountStateRoot()
	if err != nil {
		t.Fatal(err)
	}
	siblingLeaf, _ := layout.AccountStatePath("other", "claude")
	cfgRoot, _ := layout.ConfigRoot()
	if err := os.MkdirAll(cfgRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkAlias := filepath.Join(t.TempDir(), "innocent-looking")
	if err := os.Symlink(cfgRoot, symlinkAlias); err != nil {
		t.Fatal(err)
	}

	base := func() *agentbox.Config {
		cfg := &agentbox.Config{
			Name: "coder", Space: "default", Agent: "claude", Account: account.Default,
			Workdir: agentbox.Workdir{Mode: agentbox.WorkdirModeHost, Source: "/work", Canonical: "/work"},
		}
		// Normalize the managed slot in (the box's OWN leaf — always legal).
		if _, _, err := applyAgentStateMount(cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	// The base config (own leaf only) passes.
	if err := rejectAccountRootBinds(base()); err != nil {
		t.Fatalf("own-leaf managed bind must pass: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*agentbox.Config)
	}{
		{"mount of the root", func(c *agentbox.Config) {
			c.Mounts = append(c.Mounts, agentbox.Mount{Source: root, Target: "/steal"})
		}},
		{"mount of a sibling leaf", func(c *agentbox.Config) {
			c.Mounts = append(c.Mounts, agentbox.Mount{Source: siblingLeaf, Target: "/steal"})
		}},
		{"symlink resolving to a sibling leaf", func(c *agentbox.Config) {
			c.Mounts = append(c.Mounts, agentbox.Mount{Source: symlinkAlias, Target: "/steal"})
		}},
		{"mount of a parent of the root", func(c *agentbox.Config) {
			c.Mounts = append(c.Mounts, agentbox.Mount{Source: cfgRoot, Target: "/steal"})
		}},
		{"workdir inside the root", func(c *agentbox.Config) {
			c.Workdir = agentbox.Workdir{Mode: agentbox.WorkdirModeHost, Source: siblingLeaf, Canonical: siblingLeaf}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			err := rejectAccountRootBinds(cfg)
			if err == nil {
				t.Fatal("bind intersecting the account-state root must be rejected")
			}
			if !IsUserError(err) {
				t.Errorf("error %q should be a user error naming the managed root", err)
			}
		})
	}

	// An unrelated user mount passes untouched.
	ok := base()
	ok.Mounts = append(ok.Mounts, agentbox.Mount{Source: "/host/data", Target: "/data"})
	if err := rejectAccountRootBinds(ok); err != nil {
		t.Errorf("unrelated user mount must pass: %v", err)
	}
}

// TestCanonicalizeUserMountSourcesBeforeGrantCheck locks the other half of the
// mount-boundary fix: an alias located under an allowed space grant cannot
// smuggle in a source outside that grant. The resolved path is both the value
// checked for authority and the value retained for persistence/runtime use.
func TestCanonicalizeUserMountSourcesBeforeGrantCheck(t *testing.T) {
	granted := t.TempDir()
	outside := t.TempDir()
	alias := filepath.Join(granted, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{
		Agent:   account.None,
		Account: account.None,
		Mounts:  []agentbox.Mount{{Source: alias, Target: "/data"}},
	}
	changed, err := canonicalizeUserMountSources(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("symlinked mount source was not canonicalized")
	}
	want, _, err := agentbox.CanonicalizeWorkdir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Mounts[0].Source; got != want {
		t.Fatalf("canonical mount source = %q, want %q", got, want)
	}
	spacecfg := &spaceconfig.Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001",
		Mounts: []spaceconfig.Mount{{Source: granted, Writable: true}},
	}
	if got := classifyBindGrant(spacecfg, cfg.Mounts[0].Source, true); got != bindNotGranted {
		t.Fatalf("resolved outside path unexpectedly inherited the symlink's grant: got %d", got)
	}
}
