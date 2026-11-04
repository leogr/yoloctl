package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

// secretValue is the canonical never-should-leak token used across these
// tests. Any command output (stdout/stderr/JSON) containing it is a failure.
const secretValue = "sk-super-secret-VALUE-do-not-leak"

// execAccount runs a freshly-constructed account subcommand with the given
// stdin and args, capturing stdout / stderr / error. Each subcommand owns its
// own flags, so constructing them individually mirrors how cobra dispatches
// them at runtime.
func execAccount(t *testing.T, cmd *cobra.Command, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	if ok, _ := spaceconfig.Exists("default"); !ok {
		saveSpaceForTest(t, "default", "/work")
	}
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// mustCreate creates an account via the create command, failing on error.
func mustCreate(t *testing.T, name string) {
	t.Helper()
	if _, _, err := execAccount(t, newAccountCreateCmd(), "", name); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func TestAccountDestroyFailsClosedOnUnreadableBoxReferences(t *testing.T) {
	native := fakeNativeAccounts(t)
	_ = native
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustCreate(t, "personal")
	path, err := layout.BoxPath("default", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("account: personal\nfeatures: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = execAccount(t, newAccountDestroyCmd(), "y\n", "personal")
	if err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "cannot determine references") {
		t.Fatalf("destroy error = %v, want an unknown-reference user error", err)
	}
	if ok, err := account.Exists("default", "personal"); err != nil || !ok {
		t.Fatalf("refused destroy removed the account: exists=%v, error=%v", ok, err)
	}
	if _, _, err := execAccount(t, newAccountDestroyCmd(), "", "personal", "--force"); err != nil {
		t.Fatalf("forced destroy: %v", err)
	}
}

// TestAccountRoundtrip exercises create → show → secret set → secret ls →
// secret unset → destroy on a named account, asserting that the secret VALUE
// never appears in show / secret ls output, and that destroy removes both the
// manifest and the whole identity home.
func TestAccountRoundtrip(t *testing.T) {
	native := fakeNativeAccounts(t)
	_ = native
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const name = "personal"

	// create
	out, _, err := execAccount(t, newAccountCreateCmd(), "", name)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, name) {
		t.Errorf("create output %q should mention the name", out)
	}
	if ok, _ := account.Exists("default", name); !ok {
		t.Fatal("account should exist after create")
	}

	// show (no secrets yet)
	out, _, err = execAccount(t, newAccountShowCmd(), "", name)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, name) || !strings.Contains(out, "Manifest:") {
		t.Errorf("show output %q should include name + manifest path", out)
	}

	// secret set (value from stdin) — must NOT echo the value
	const secretName = "ANTHROPIC_API_KEY"
	out, errOut, err := execAccount(t, newAccountSecretSetCmd(), secretValue+"\n", name, secretName)
	if err != nil {
		t.Fatalf("secret set: %v", err)
	}
	if strings.Contains(out, secretValue) || strings.Contains(errOut, secretValue) {
		t.Errorf("secret set output leaked the value: %q / %q", out, errOut)
	}
	if !strings.Contains(out, secretName) {
		t.Errorf("secret set output %q should name the secret", out)
	}
	// Account content never synchronizes native state or drives reconcile;
	// create-time environment injection needs an explicit recreate.
	if strings.Contains(errOut, "box restart") || !strings.Contains(errOut, "recreate") ||
		!strings.Contains(errOut, "not updated automatically") {
		t.Errorf("secret set stderr %q should carry the manual pickup note", errOut)
	}

	// Confirm the value really persisted.
	c, err := account.Load("default", name)
	if err != nil {
		t.Fatalf("load after set: %v", err)
	}
	if c.Secrets[secretName] != secretValue {
		t.Errorf("stored secret value mismatch: got %q", c.Secrets[secretName])
	}

	// secret ls — names only, never the value
	out, _, err = execAccount(t, newAccountSecretListCmd(), "", name)
	if err != nil {
		t.Fatalf("secret ls: %v", err)
	}
	if !strings.Contains(out, secretName) {
		t.Errorf("secret ls %q should list the name", out)
	}
	if strings.Contains(out, secretValue) {
		t.Errorf("secret ls leaked the value: %q", out)
	}

	// show again — lists the secret NAME, never the value
	out, _, err = execAccount(t, newAccountShowCmd(), "", name)
	if err != nil {
		t.Fatalf("show with secret: %v", err)
	}
	if !strings.Contains(out, secretName) {
		t.Errorf("show %q should list the secret name", out)
	}
	if strings.Contains(out, secretValue) {
		t.Errorf("show leaked the value: %q", out)
	}

	// secret unset — present case
	out, _, err = execAccount(t, newAccountSecretUnsetCmd(), "", name, secretName)
	if err != nil {
		t.Fatalf("secret unset: %v", err)
	}
	if strings.Contains(out, secretValue) {
		t.Errorf("secret unset leaked the value: %q", out)
	}
	c, err = account.Load("default", name)
	if err != nil {
		t.Fatalf("load after unset: %v", err)
	}
	if _, ok := c.Secrets[secretName]; ok {
		t.Error("secret should be gone after unset")
	}
	// secret unset again — absent case (no error, reports "was not set")
	out, _, err = execAccount(t, newAccountSecretUnsetCmd(), "", name, secretName)
	if err != nil {
		t.Fatalf("secret unset (absent): %v", err)
	}
	if !strings.Contains(out, "was not set") {
		t.Errorf("unsetting an absent secret should report it: %q", out)
	}

	// destroy (no referencing boxes; -y to skip the prompt) removes manifest
	// AND the whole identity home (every per-agent leaf).
	leaf, err := native(name, "claude"), error(nil)
	if err != nil {
		t.Fatalf("StatePath: %v", err)
	}
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatalf("seed state leaf: %v", err)
	}
	if _, _, err := execAccount(t, newAccountDestroyCmd(), "", name, "-y"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if ok, _ := account.Exists("default", name); ok {
		t.Error("manifest should be gone after destroy")
	}
	home := native(name, "")
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("identity home should be removed after destroy, stat err = %v", err)
	}
}

// TestAccountCreateExisting: a second create of the same name errors; and the
// reserved "none" sentinel can never be created.
func TestAccountCreateExisting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustCreate(t, "work")
	_, _, err := execAccount(t, newAccountCreateCmd(), "", "work")
	if err == nil {
		t.Fatal("second create of the same name should error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want 'already exists'", err)
	}
	if !IsUserError(err) {
		t.Error("create-existing should be a user error")
	}

	if _, _, err := execAccount(t, newAccountCreateCmd(), "", "none"); err == nil {
		t.Fatal("creating the reserved name 'none' must fail")
	}
}

// TestAccountList: ls shows every account by plain name, sorted.
func TestAccountList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustCreate(t, "personal")
	mustCreate(t, "default")

	out, _, err := execAccount(t, newAccountListCmd(), "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out != "default\npersonal\n" {
		t.Errorf("list output = %q, want sorted plain names", out)
	}
}

// TestAccountShowStateLeaves: show lists the per-agent state leaves present
// under the identity home.
func TestAccountShowStateLeaves(t *testing.T) {
	native := fakeNativeAccounts(t)
	_ = native
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustCreate(t, "personal")
	for _, agent := range []string{"agy", "claude", "codex", "gemini", "opencode", "pi"} {
		leaf, err := native("personal", agent), error(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(leaf, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := execAccount(t, newAccountShowCmd(), "", "personal")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "State leaves:") || !strings.Contains(out, "claude") || !strings.Contains(out, "codex") || !strings.Contains(out, "gemini") {
		t.Errorf("show output %q should list all per-agent state leaves", out)
	}
}

// TestAccountDestroyRefusesReferencingBox: destroy fails (without --force)
// when a box in its space still selects the account, names the box on stderr,
// and preserves the manifest. --force then goes through.
func TestAccountDestroyRefusesReferencingBox(t *testing.T) {
	native := fakeNativeAccounts(t)
	_ = native
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const name = "personal"
	mustCreate(t, name)

	// Save a box in a space bundle WITHOUT a space.yaml (destroy must scan it
	// anyway — bundles without a loadable space config still count).
	box := &agentbox.Config{
		Image:         "img:1",
		SchemaVersion: agentbox.SchemaVersion,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Account:       name,
		Workdir:       agentbox.Workdir{Mode: agentbox.WorkdirModeEphemeral, Source: "/var/lib/yoloctl/agentboxes/coder"},
	}
	if err := box.Save(); err != nil {
		t.Fatalf("save box: %v", err)
	}

	// Without --force, destroy must refuse and preserve the manifest.
	_, errOut, err := execAccount(t, newAccountDestroyCmd(), "", name, "-y")
	if err == nil {
		t.Fatal("destroy should refuse while a box selects the account")
	}
	if !strings.Contains(errOut, "default/coder") {
		t.Errorf("stderr %q should name the referencing box as space/box", errOut)
	}
	if ok, _ := account.Exists("default", name); !ok {
		t.Error("a refused destroy must not delete the manifest")
	}

	// With --force it goes through.
	if _, _, err := execAccount(t, newAccountDestroyCmd(), "", name, "--force"); err != nil {
		t.Fatalf("forced destroy: %v", err)
	}
	if ok, _ := account.Exists("default", name); ok {
		t.Error("forced destroy should delete the manifest")
	}
}

// TestAccountDestroyReportsUnreadableBox: a box manifest that fails to load
// is REPORTED (never silently skipped) and its account field is extracted
// loosely, so a broken manifest can't hide a reference.
func TestAccountDestroyReportsUnreadableBox(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const name = "personal"
	mustCreate(t, name)

	// Hand-write a box YAML agentbox.Load rejects (unknown field) but whose
	// account field is loosely extractable.
	path, err := layout.BoxPath("default", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "schema_version: 1\nname: broken\nspace: default\nagent: claude\naccount: " + name + "\nbogus_field: true\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	_, errOut, err := execAccount(t, newAccountDestroyCmd(), "", name, "-y")
	if err == nil {
		t.Fatal("destroy should refuse: the unreadable box still selects the account")
	}
	if !strings.Contains(errOut, "unreadable") {
		t.Errorf("stderr %q should report the unreadable box manifest", errOut)
	}
	if !strings.Contains(errOut, "default/broken") {
		t.Errorf("stderr %q should name the loosely-extracted referencing box", errOut)
	}
}

// A deleted default stays deleted until explicitly created again.
func TestAccountDestroyDefaultStaysDeleted(t *testing.T) {
	native := fakeNativeAccounts(t)
	_ = native
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustCreate(t, account.Default)
	_, errOut, err := execAccount(t, newAccountDestroyCmd(), "", account.Default, "-y")
	if err != nil {
		t.Fatalf("destroy default: %v", err)
	}
	if strings.Contains(errOut, "auto-recreated") {
		t.Errorf("stderr %q must not promise auto-recreation", errOut)
	}
	if ok, _ := account.Exists("default", account.Default); ok {
		t.Error("default should be gone after destroy")
	}
}

// TestAccountShowMissing: show on a non-existent account errors with a create
// hint (and is a user error). Read paths never auto-create — not even default.
func TestAccountShowMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"personal", account.Default} {
		_, _, err := execAccount(t, newAccountShowCmd(), "", name)
		if err == nil {
			t.Fatalf("show on missing %q should error", name)
		}
		if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "account create") {
			t.Errorf("error = %v, want a 'does not exist' + create hint", err)
		}
		if !IsUserError(err) {
			t.Error("missing-account show should be a user error")
		}
		if ok, _ := account.Exists("default", name); ok {
			t.Errorf("show resurrected account %q — read paths must never create", name)
		}
	}
}

// Secret writes never implicitly create an account.
func TestAccountSecretSetMissingAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := execAccount(t, newAccountSecretSetCmd(), secretValue+"\n", "personal", "ANTHROPIC_API_KEY")
	if err == nil {
		t.Fatal("secret set on a missing named account should error")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %v, want 'does not exist'", err)
	}

	// A failed read (empty stdin) must not auto-create default.
	if _, _, err := execAccount(t, newAccountSecretSetCmd(), "", account.Default, "ANTHROPIC_API_KEY"); err == nil {
		t.Fatal("secret set with no value should error")
	}
	if ok, _ := account.Exists("default", account.Default); ok {
		t.Error("a failed secret set auto-created default (validation must precede auto-creation)")
	}

	if _, _, err := execAccount(t, newAccountSecretSetCmd(), secretValue+"\n", account.Default, "ANTHROPIC_API_KEY"); err == nil {
		t.Fatal("missing default must not be auto-created")
	}
	if ok, _ := account.Exists("default", account.Default); ok {
		t.Fatal("missing account was created")
	}

}

// TestAccountSecretSetFromEnv: --from-env reads the value from the env var,
// stores it, and never echoes it.
func TestAccountSecretSetFromEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const name = "personal"
	mustCreate(t, name)

	t.Setenv("ANTHROPIC_API_KEY", secretValue)
	out, _, err := execAccount(t, newAccountSecretSetCmd(), "", name, "ANTHROPIC_API_KEY", "--from-env")
	if err != nil {
		t.Fatalf("secret set --from-env: %v", err)
	}
	if strings.Contains(out, secretValue) {
		t.Errorf("--from-env output leaked the value: %q", out)
	}
	c, err := account.Load("default", name)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Secrets["ANTHROPIC_API_KEY"] != secretValue {
		t.Errorf("value from env not stored: got %q", c.Secrets["ANTHROPIC_API_KEY"])
	}
}

// TestAccountJSONNeverLeaksSecretValue: the --json forms of show and secret ls
// emit secret names but never values.
func TestAccountJSONNeverLeaksSecretValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const name = "personal"
	mustCreate(t, name)
	if _, _, err := execAccount(t, newAccountSecretSetCmd(), secretValue+"\n", name, "ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("secret set: %v", err)
	}

	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
	}{
		{"show --json", newAccountShowCmd},
		{"secret ls --json", newAccountSecretListCmd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := execAccount(t, tc.cmd(), "", name, "--json")
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !strings.Contains(out, "ANTHROPIC_API_KEY") {
				t.Errorf("%s output %q should include the secret name", tc.name, out)
			}
			if strings.Contains(out, secretValue) {
				t.Errorf("%s leaked the secret value: %q", tc.name, out)
			}
		})
	}
}

// TestAccountEditRejectsUnknownField: `account edit` strict-decodes the edited
// copy. An edit that misspells or invents a field — the classic
// mistyped `secrets:` — must be rejected with the field named, leaving the
// canonical 0600 manifest byte-identical; with a lenient decode the unknown
// mapping would silently vanish on the re-marshal instead.
func TestAccountEditRejectsUnknownField(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const name = "personal"
	mustCreate(t, name)
	if _, _, err := execAccount(t, newAccountSecretSetCmd(), secretValue+"\n", name, "ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	c, err := account.Load("default", name)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}

	// $EDITOR appends an unknown field to the temp copy.
	script := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'bogus_routing: anywhere' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	_, _, err = execAccount(t, newAccountEditCmd(), "", name)
	if err == nil {
		t.Fatal("edit introducing an unknown field must be rejected")
	}
	if !strings.Contains(err.Error(), "bogus_routing") || !strings.Contains(err.Error(), "untouched") {
		t.Errorf("error %q should name the unknown field and say the canonical manifest is untouched", err)
	}
	after, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("canonical manifest changed despite the rejected edit")
	}
}

func TestAccountEditCanonicalizesRetiredDevelopmentRevision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const name = "personal"
	mustCreate(t, name)
	c, err := account.Load("default", name)
	if err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf(
		"schema_version: %d\nname: personal\nrevision: 4\n",
		account.SchemaVersion+1,
	)
	if err := os.WriteFile(c.Path(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(t.TempDir(), "editor.sh")
	body := "#!/bin/sh\nprintf '\\nsecrets:\\n  ANTHROPIC_API_KEY: test-value\\n' >> \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	if _, _, err := execAccount(t, newAccountEditCmd(), "", name); err != nil {
		t.Fatalf("edit retired development manifest: %v", err)
	}
	canonical, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), "revision:") {
		t.Fatalf("account edit retained retired revision field:\n%s", canonical)
	}
	got, err := account.Load("default", name)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != account.SchemaVersion || got.Secrets["ANTHROPIC_API_KEY"] != "test-value" {
		t.Fatalf("canonicalized account = schema %d, secrets %v", got.SchemaVersion, got.SecretNames())
	}
}
