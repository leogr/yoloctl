package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
)

// TestResolveAgentAndAccount pins the creation-time resolution of the raw
// --agent / --account flags into the box's stored Agent + Account fields
// (DECISION A: omitted agent ⇒ generic shell none/none; DECISION B: a real
// agent with an omitted --account defaults to "default" and a real agent with
// --account none is rejected).
func TestResolveAgentAndAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/work")
	cases := []struct {
		name        string
		agentFlag   string
		accountFlag string
		wantAgent   string
		wantAccount string
		wantErr     string
	}{
		{
			name:        "agent omitted -> generic shell none/none",
			agentFlag:   "",
			accountFlag: "",
			wantAgent:   account.None,
			wantAccount: account.None,
		},
		{
			name:        "agent none explicit -> generic shell none/none",
			agentFlag:   "none",
			accountFlag: "",
			wantAgent:   account.None,
			wantAccount: account.None,
		},
		{
			name:        "real agent omitted account -> default",
			agentFlag:   "claude",
			accountFlag: "",
			wantAgent:   "claude",
			wantAccount: account.Default,
		},
		{
			name:        "real agent explicit account kept",
			agentFlag:   "codex",
			accountFlag: "personal",
			wantAgent:   "codex",
			wantAccount: "personal",
		},
		{
			name:        "real agent with account none rejected (DECISION B)",
			agentFlag:   "claude",
			accountFlag: "none",
			wantErr:     "real agents require real accounts",
		},
		{
			name:        "generic shell with real account rejected (DECISION A)",
			agentFlag:   "",
			accountFlag: "personal",
			wantErr:     "requires a real --agent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent, acct, err := resolveAgentAndAccount("default", tc.agentFlag, tc.accountFlag)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (agent=%q account=%q)", tc.wantErr, agent, acct)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if agent != tc.wantAgent || acct != tc.wantAccount {
				t.Fatalf("resolveAgentAndAccount(%q,%q) = (%q,%q), want (%q,%q)",
					tc.agentFlag, tc.accountFlag, agent, acct, tc.wantAgent, tc.wantAccount)
			}
		})
	}
}

// ensureSelectableForTest composes the guard exactly the way production
// callers do: lockBoxAccount + ensureAccountSelectableLocked ("" / none are
// the guard's no-op cases and take no real-account lock).
func ensureSelectableForTest(acct string, notice io.Writer) error {
	if acct == "" || acct == account.None {
		return ensureAccountSelectableLocked("default", acct)
	}
	release, err := lockBoxAccount("default", acct)
	if err != nil {
		return err
	}
	defer release()
	return ensureAccountSelectableLocked("default", acct)
}

// Selectability requires an existing scoped account before any box save.
func TestAccountSelectabilityGuard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Explicitly named missing account: fail with the hint (typo protection).
	err := ensureSelectableForTest("personal", io.Discard)
	if err == nil {
		t.Fatal("ensureSelectableForTest with a missing named account: expected error")
	}
	if !strings.Contains(err.Error(), "does not exist") ||
		!strings.Contains(err.Error(), "yoloctl account create") {
		t.Fatalf("error %q must mention the missing account and the create hint", err.Error())
	}
	if ok, _ := account.Exists("default", "personal"); ok {
		t.Fatal("a failed guard must not create the named account")
	}

	// The guard sits before every Save: no box manifest may exist either.
	boxPath, err := layout.BoxPath("default", "coder")
	if err != nil {
		t.Fatalf("BoxPath: %v", err)
	}
	if _, statErr := os.Stat(boxPath); !os.IsNotExist(statErr) {
		t.Fatalf("box manifest must not exist after a failed account guard, stat err = %v", statErr)
	}

	if err := ensureSelectableForTest(account.Default, io.Discard); err == nil {
		t.Fatal("missing default must fail")
	}
	if err := account.New("default", account.Default).Save(); err != nil {
		t.Fatal(err)
	}

	// Idempotent once created; and a real named account passes after create.
	if err := ensureSelectableForTest(account.Default, io.Discard); err != nil {
		t.Fatalf("ensureSelectableForTest on existing default: %v", err)
	}
	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := ensureSelectableForTest("personal", io.Discard); err != nil {
		t.Fatalf("ensureSelectableForTest after create: %v", err)
	}

	// "none" (generic shell) and "" skip the check entirely.
	if err := ensureSelectableForTest(account.None, io.Discard); err != nil {
		t.Fatalf("ensureSelectableForTest(none): %v", err)
	}
	if err := ensureSelectableForTest("", io.Discard); err != nil {
		t.Fatalf("ensureSelectableForTest(empty): %v", err)
	}

	// The guard loads the manifest: corruption fails selectability BEFORE
	// anything is persisted — and a corrupt default is never clobbered by the
	// auto-create.
	personal, err := account.Load("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(personal.Path(), []byte(":\t:not yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	err = ensureSelectableForTest("personal", io.Discard)
	if err == nil {
		t.Fatal("corrupt account manifest passed the selectability guard")
	}
	if strings.Contains(err.Error(), "account create") {
		t.Errorf("corrupt manifest misreported as missing: %v", err)
	}
}

// TestRequireAccountForLifecycle pins the STRICT lifecycle existence rule
// An existing box whose account manifest is missing fails with an
// actionable hint — and never resurrects a destroyed default.
func TestRequireAccountForLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &agentbox.Config{
		SchemaVersion: agentbox.SchemaVersion,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Account:       account.Default,
		Workdir:       agentbox.NewEphemeralWorkdir("coder"),
	}
	err := requireAccountForLifecycle(cfg)
	if err == nil {
		t.Fatal("lifecycle with a missing account must fail strictly")
	}
	if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "account create") {
		t.Errorf("error %q should carry the create hint", err)
	}
	// Crucially: the strict check never auto-creates, not even default (a
	// dangling account: default fails like any other missing account).
	if ok, _ := account.Exists("default", account.Default); ok {
		t.Fatal("lifecycle check resurrected the default account")
	}
	if err := account.New("default", account.Default).Save(); err != nil {
		t.Fatal(err)
	}
	if err := requireAccountForLifecycle(cfg); err != nil {
		t.Fatalf("lifecycle with an existing account: %v", err)
	}
	// Generic shell: no account dimension, always passes.
	shell := &agentbox.Config{Name: "sh", Space: "default", Agent: account.None, Account: account.None}
	if err := requireAccountForLifecycle(shell); err != nil {
		t.Fatalf("generic shell lifecycle check: %v", err)
	}
}

// TestRequireAccountForLifecycle_FailsClosedOnCorruptManifest: the gate LOADS
// the manifest — an account file that exists but cannot be decoded is
// an error, never treated as "exists, proceed" the way a bare existence stat
// would.
func TestRequireAccountForLifecycle_FailsClosedOnCorruptManifest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	acct := account.New("default", account.Default)
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(acct.Path(), []byte(":\t:not yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{
		SchemaVersion: agentbox.SchemaVersion,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Account:       account.Default,
		Workdir:       agentbox.NewEphemeralWorkdir("coder"),
	}
	err := requireAccountForLifecycle(cfg)
	if err == nil {
		t.Fatal("lifecycle gate passed a corrupt account manifest")
	}
	if strings.Contains(err.Error(), "refusing to start") {
		t.Errorf("corrupt manifest misreported as missing: %v", err)
	}
}

// TestRequireAccountForLifecycle_SerializesWithDestroy: the gate reads under
// the account lock, so it queues behind an in-flight destroy and then reports
// the post-destroy truth (the actionable missing-account error) instead of a
// half-observed state — and never auto-creates.
func TestRequireAccountForLifecycle_SerializesWithDestroy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	acct := account.New("default", account.Default)
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	cfg := &agentbox.Config{
		SchemaVersion: agentbox.SchemaVersion,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Account:       account.Default,
		Workdir:       agentbox.NewEphemeralWorkdir("coder"),
	}

	release, err := account.Lock("default", account.Default)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- requireAccountForLifecycle(cfg)
	}()
	select {
	case err := <-done:
		t.Fatalf("lifecycle gate read the account while its lock was held elsewhere (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	// The destroy completes under its lock (state then manifest), then releases.
	home, err := account.StateHome(account.Default)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if err := acct.Delete(); err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lifecycle gate passed a destroyed account")
		}
		if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "account create") {
			t.Errorf("error %q should carry the actionable create hint", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle gate never completed")
	}
	if ok, _ := account.Exists("default", account.Default); ok {
		t.Fatal("lifecycle gate auto-created the destroyed account")
	}
}

// TestBuildAgentboxConfig_GenericShell covers DECISION A end to end at the
// config-build level: an --agent-omitted create resolves to Agent "none" &
// Account "none", the built Config reports HasAgent()==false, and it validates.
func TestBuildAgentboxConfig_GenericShell(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	agent, acct, err := resolveAgentAndAccount("default", "", "")
	if err != nil {
		t.Fatalf("resolveAgentAndAccount: %v", err)
	}
	if agent != account.None || acct != account.None {
		t.Fatalf("generic-shell resolution = (%q,%q), want (none,none)", agent, acct)
	}
	opts := agentboxCreateOptions{
		Space:    "default",
		Name:     "shell",
		Agent:    agent,
		Account:  acct,
		Image:    "img:1",
		Features: []string{"go"}, // a generic-shell box brings its own policy intent
	}
	opts.workdir = agentbox.NewEphemeralWorkdir(opts.Name)

	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if cfg.HasAgent() {
		t.Fatalf("generic-shell box must report HasAgent()==false (agent=%q)", cfg.Agent)
	}
	if cfg.Agent != account.None || cfg.Account != account.None {
		t.Fatalf("generic-shell config = (agent %q, account %q), want (none,none)", cfg.Agent, cfg.Account)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generic-shell Config.Validate(): %v", err)
	}
}

// TestBuildAgentboxConfig_RealAgentDefaultAccount covers DECISION B at the
// config-build level: a real --agent with an omitted --account resolves and
// stamps the plain "default" name onto the Config.
func TestBuildAgentboxConfig_RealAgentDefaultAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, "default", "/work")

	agent, acct, err := resolveAgentAndAccount("default", "claude", "")
	if err != nil {
		t.Fatalf("resolveAgentAndAccount: %v", err)
	}
	opts := agentboxCreateOptions{
		Space:    "default",
		Name:     "coder",
		Agent:    agent,
		Account:  acct,
		Image:    "img:1",
		Features: []string{"claude"},
	}
	opts.workdir = agentbox.NewEphemeralWorkdir(opts.Name)

	cfg, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatalf("buildAgentboxConfig: %v", err)
	}
	if cfg.Account != account.Default {
		t.Fatalf("real-agent default account = %q, want %q", cfg.Account, account.Default)
	}
	if !cfg.HasAgent() {
		t.Fatalf("real-agent box must report HasAgent()==true (agent=%q)", cfg.Agent)
	}
}
