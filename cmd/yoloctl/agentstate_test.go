package main

import (
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/layout"
)

// TestResolveAgentStateMount covers the account-derived resolution: the state
// dir is OWNED by the box's account and is its per-agent leaf
// (account.StatePath(account, agent) = account-state/<account>/<agent>). It is
// never a box field. Plus the "no mount" branches (no agent; account none;
// profile without StateContainerPath).
func TestResolveAgentStateMount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	claude := &agentprofile.Profile{
		Name:               "claude",
		Image:              "yoloctl/agentbox:test",
		Command:            "claude",
		StateContainerPath: "/home/sandbox/.claude",
	}
	stateless := &agentprofile.Profile{
		Name:    "bare",
		Image:   "yoloctl/agentbox-bare:test",
		Command: "bash",
	}

	statePath := func(name, agent string) string {
		p, err := account.StatePath(name, agent)
		if err != nil {
			t.Fatalf("StatePath(%q,%q): %v", name, agent, err)
		}
		return p
	}

	cases := []struct {
		name        string
		profile     *agentprofile.Profile
		cfg         *agentbox.Config
		wantEnabled bool
		wantSource  string
		wantDest    string
		wantErr     string
	}{
		{
			name:        "generic_shell_no_agent_no_mount",
			profile:     claude,
			cfg:         &agentbox.Config{Agent: "", Account: ""},
			wantEnabled: false,
		},
		{
			name:        "account_none_no_mount",
			profile:     claude,
			cfg:         &agentbox.Config{Agent: "none", Account: account.None},
			wantEnabled: false,
		},
		{
			name:        "stateless_profile_no_mount",
			profile:     stateless,
			cfg:         &agentbox.Config{Agent: "bare", Account: "default", Space: "default"},
			wantEnabled: false,
		},
		{
			name:        "default_account_leaf",
			profile:     claude,
			cfg:         &agentbox.Config{Agent: "claude", Account: "default", Space: "default"},
			wantEnabled: true,
			wantSource:  statePath("default", "claude"),
			wantDest:    "/home/sandbox/.claude",
		},
		{
			// Accounts are host-global: the SAME account resolves to the SAME
			// leaf from any space.
			name:        "named_account_space_independent",
			profile:     claude,
			cfg:         &agentbox.Config{Agent: "claude", Account: "personal", Space: "untrusted"},
			wantEnabled: true,
			wantSource:  statePath("personal", "claude"),
			wantDest:    "/home/sandbox/.claude",
		},
		{
			name:    "invalid_account_name_errors",
			profile: claude,
			cfg:     &agentbox.Config{Agent: "claude", Account: "space/claude/default", Space: "default"},
			wantErr: "invalid account name",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveAgentStateMount(c.profile, c.cfg)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil; mount=%+v", c.wantErr, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.Enabled != c.wantEnabled {
				t.Errorf("Enabled: got %v, want %v", got.Enabled, c.wantEnabled)
			}
			if c.wantSource != "" && got.Source != c.wantSource {
				t.Errorf("Source: got %q, want %q", got.Source, c.wantSource)
			}
			if c.wantDest != "" && got.Destination != c.wantDest {
				t.Errorf("Destination: got %q, want %q", got.Destination, c.wantDest)
			}
		})
	}
}

// TestResolveAgentStateMount_LeafUnderManagedRoot locks the exact host-side
// leaf against the host-global account mount topology: every leaf
// lives directly under account-state/<name>/ which lives under the SINGLE
// managed root Lima mounts into every space VM (these are the SOURCE dirs the
// container binds narrow).
func TestResolveAgentStateMount_LeafUnderManagedRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	claude := &agentprofile.Profile{Name: "claude", Image: "i", Command: "claude", StateContainerPath: "/home/sandbox/.claude"}

	leaf, err := layout.AccountStatePath("personal", "claude")
	if err != nil {
		t.Fatalf("AccountStatePath: %v", err)
	}
	mount, err := resolveAgentStateMount(claude, &agentbox.Config{
		Agent: "claude", Account: "personal", Space: "default",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mount.Source != leaf {
		t.Errorf("source = %q, want %q", mount.Source, leaf)
	}
	root, err := layout.AccountStateRoot()
	if err != nil {
		t.Fatalf("AccountStateRoot: %v", err)
	}
	if !strings.HasPrefix(mount.Source, root+"/") {
		t.Errorf("leaf %q must live under the managed account-state root %q", mount.Source, root)
	}
	// Different agents on the same account get sibling leaves under one
	// identity home (the portable-identity-home shape).
	codex := &agentprofile.Profile{Name: "codex", Image: "i", Command: "codex", StateContainerPath: "/home/sandbox/.codex"}
	codexMount, err := resolveAgentStateMount(codex, &agentbox.Config{
		Agent: "codex", Account: "personal", Space: "default",
	})
	if err != nil {
		t.Fatalf("codex resolve: %v", err)
	}
	home, _ := layout.AccountStateHome("personal")
	for _, src := range []string{mount.Source, codexMount.Source} {
		if !strings.HasPrefix(src, home+"/") {
			t.Errorf("leaf %q not under the identity home %q", src, home)
		}
	}
	if mount.Source == codexMount.Source {
		t.Error("different agents must get distinct leaves")
	}
}

func TestBuildAgentboxConfigCreatesNoStateLeaf(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	opts := agentboxCreateOptions{
		Space:    "default",
		Name:     "coder",
		Agent:    "claude",
		Account:  "personal",
		Image:    "img:1",
		Features: []string{"claude"},
	}
	opts.workdir = agentbox.NewEphemeralWorkdir(opts.Name)
	cfg, mount, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if !mount.Enabled {
		t.Fatal("claude box should resolve an enabled state mount")
	}
	home, err := account.StateHome("personal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("config build materialized account state (stat err = %v) — building must be pure", err)
	}
	// Manifest persistence also leaves native storage untouched.
	if err := persistBoxManifestWithAccount(cfg); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if _, err := os.Stat(mount.Source); !os.IsNotExist(err) {
		t.Fatalf("manifest persist touched native storage: %v", err)
	}

	// After a destroy, a rebuild still creates nothing: no orphan resurrection
	// from the build path.
	acct, err := account.Load("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := acct.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildAgentboxConfig(t.Context(), opts); err != nil {
		t.Fatalf("buildAgentboxConfig after destroy: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("config build resurrected state for a destroyed account (stat err = %v)", err)
	}
}
