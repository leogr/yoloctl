package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
)

// These tests pin the account transaction rule: box-create persistence and
// `account auth import` hold the selected account's lock from the
// existence/selectability check through their last account-coupled write, so
// a concurrent `account destroy` serializes entirely before or entirely
// after — never interleaved into a dangling box manifest or a resurrected
// state leaf.

// txnBoxConfig returns a real-agent box declaration selecting acct, plus its
// resolved (enabled) account-state mount, without touching the filesystem.
func txnBoxConfig(t *testing.T, acct string) (*agentbox.Config, agentStateMount) {
	t.Helper()
	cfg := &agentbox.Config{
		Image:         "img:1",
		SchemaVersion: agentbox.SchemaVersion,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Account:       acct,
		Workdir:       agentbox.NewEphemeralWorkdir("coder"),
	}
	src, err := account.StatePath(acct, cfg.Agent)
	if err != nil {
		t.Fatalf("StatePath: %v", err)
	}
	return cfg, agentStateMount{Source: src, Destination: "/home/sandbox/.claude", Enabled: true}
}

// boxManifestExists reports whether the box declaration was persisted.
func boxManifestExists(t *testing.T, space, name string) bool {
	t.Helper()
	path, err := layout.BoxPath(space, name)
	if err != nil {
		t.Fatalf("BoxPath: %v", err)
	}
	_, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		t.Fatalf("stat box manifest: %v", statErr)
	}
	return statErr == nil
}

// TestPersistBoxManifestHoldsAccountLock: the create-persist transaction
// blocks while another holder owns the account lock (it would self-deadlock
// here instead if it nested a second acquisition), and completes — leaf dir
// and manifest together — once the lock is released.
func TestPersistBoxManifestHoldsAccountLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := txnBoxConfig(t, "personal")

	release, err := account.Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- persistBoxManifestWithAccount(cfg)
	}()
	select {
	case err := <-done:
		t.Fatalf("persist completed while the account lock was held elsewhere (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	if boxManifestExists(t, cfg.Space, cfg.Name) {
		t.Fatal("box manifest written while the account lock was held elsewhere")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("persist after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("persist never completed after the lock was released")
	}
	if !boxManifestExists(t, cfg.Space, cfg.Name) {
		t.Fatal("box manifest missing after successful persist")
	}
}

// TestPersistBoxManifestNoDanglingBoxAfterDestroy is the dangling-box
// regression: an account destroy that wins the lock completes fully
// (state, then manifest) before a pending box create may proceed; the create
// must then fail with the actionable create hint, persisting neither a box
// manifest nor a state leaf for the destroyed identity.
func TestPersistBoxManifestNoDanglingBoxAfterDestroy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	acct := account.New("default", "personal")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := txnBoxConfig(t, "personal")

	// The destroyer holds the lock…
	release, err := account.Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	// …a box create arrives concurrently and must queue behind it…
	done := make(chan error, 1)
	go func() {
		done <- persistBoxManifestWithAccount(cfg)
	}()
	// …while the destroy completes under its lock: state first, manifest last
	// (the same order runAccountDestroy uses).
	if err := acct.Delete(); err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("box persist succeeded against a destroyed account")
		}
		if !strings.Contains(err.Error(), "does not exist") ||
			!strings.Contains(err.Error(), "yoloctl account create") {
			t.Fatalf("error %q should carry the actionable create hint", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("persist never completed")
	}
	if boxManifestExists(t, cfg.Space, cfg.Name) {
		t.Fatal("dangling box manifest left behind after a concurrent account destroy")
	}
}

// TestAuthImportSerializesWithAccountDestroy: `account auth import` queues
// behind an in-flight destroy holding the account lock, and once the destroy
// finishes it must fail with the create hint WITHOUT resurrecting any native
// state for the destroyed identity (the import's leaf write happens under the
// same lock as its selectability check).
func TestAuthImportSerializesWithAccountDestroy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	acct := account.New("default", "personal")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(src, []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	release, err := account.Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "codex", "--file", src)
		done <- err
	}()
	// Destroy completes under its lock before the import may proceed.
	if err := acct.Delete(); err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("auth import succeeded against a destroyed account")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("error %q should be the missing-account create hint", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("auth import never completed")
	}
	if ok, _ := account.Exists("default", "personal"); ok {
		t.Fatal("auth import resurrected a destroyed named account")
	}
}

// TestAuthImportBlocksWhileAccountLockHeld: the import's account/state
// mutation is serialized on the account lock — nothing is written while
// another holder owns it, and the one-time copy lands (0600) after release.
func TestAuthImportBlocksWhileAccountLockHeld(t *testing.T) {
	native := fakeNativeAccounts(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(src, []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf, err := native("personal", "codex"), error(nil)
	if err != nil {
		t.Fatal(err)
	}

	release, err := account.Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := execAccount(t, newAccountAuthImportCmd(), "", "personal", "codex", "--file", src)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("auth import completed while the account lock was held elsewhere (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(leaf, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("auth file written while the account lock was held elsewhere, stat err = %v", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("auth import after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("auth import never completed after the lock was released")
	}
	info, err := os.Stat(filepath.Join(leaf, "auth.json"))
	if err != nil {
		t.Fatalf("imported auth file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("imported auth file mode = %o, want 0600", perm)
	}
}

// TestSaveRepointableBoxManifest pins the box-edit re-point path:
// the save runs under the selected account's lock with a LOAD check, so a
// missing (or concurrently destroyed) account fails with the create hint
// BEFORE anything is persisted — an edit can never newly persist a dangling
// account reference — while an existing account saves normally. Edit is not
// a write-through use: default is never auto-created here.
func TestSaveRepointableBoxManifest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := txnBoxConfig(t, "personal")

	// Missing named account: hint, nothing saved.
	err := saveRepointableBoxManifest(cfg)
	if err == nil {
		t.Fatal("save against a missing account succeeded")
	}
	if !strings.Contains(err.Error(), "does not exist") ||
		!strings.Contains(err.Error(), "yoloctl account create") {
		t.Fatalf("error %q should carry the actionable create hint", err)
	}
	if boxManifestExists(t, cfg.Space, cfg.Name) {
		t.Fatal("manifest persisted despite the missing account")
	}

	// Missing default: same rule — edit never auto-creates (not a
	// write-through use).
	dflt, _ := txnBoxConfig(t, account.Default)
	if err := saveRepointableBoxManifest(dflt); err == nil {
		t.Fatal("save against a missing default succeeded")
	}
	if ok, _ := account.Exists("default", account.Default); ok {
		t.Fatal("edit-save auto-created the default account")
	}

	// Existing account: saved.
	if err := account.New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	if err := saveRepointableBoxManifest(cfg); err != nil {
		t.Fatalf("save with existing account: %v", err)
	}
	if !boxManifestExists(t, cfg.Space, cfg.Name) {
		t.Fatal("manifest missing after successful save")
	}
}

// TestSaveRepointableBoxManifestSerializesWithDestroy: the edit-save queues
// behind an in-flight destroy holding the account lock; once the destroy
// completes, the save must fail with the create hint and persist nothing —
// the destroy-scan-vs-edit-save interleaving that previously left a freshly
// saved manifest referencing a just-destroyed account.
func TestSaveRepointableBoxManifestSerializesWithDestroy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	acct := account.New("default", "personal")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := txnBoxConfig(t, "personal")

	release, err := account.Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- saveRepointableBoxManifest(cfg)
	}()
	select {
	case err := <-done:
		t.Fatalf("edit-save completed while the account lock was held elsewhere (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	// The destroy completes under its lock (state, then manifest).
	if err := acct.Delete(); err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("edit-save succeeded against a destroyed account")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("error %q should be the missing-account create hint", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("edit-save never completed")
	}
	if boxManifestExists(t, cfg.Space, cfg.Name) {
		t.Fatal("edit-save persisted a manifest referencing a destroyed account")
	}
}

// TestBoxEditRejectsMissingAccountRepoint drives the full `box edit` flow
// with an $EDITOR that re-points the box at a nonexistent account: the edit
// must fail with the actionable create hint BEFORE saving, leaving the
// on-disk manifest still selecting its previous account (no newly persisted
// dangling reference), and never proceed to any VM work.
func TestBoxEditRejectsMissingAccountRepoint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := account.New("default", account.Default).Save(); err != nil {
		t.Fatal(err)
	}
	box := &agentbox.Config{
		SchemaVersion: agentbox.SchemaVersion,
		Name:          "coder",
		Space:         "default",
		Agent:         "claude",
		Account:       account.Default,
		Image:         "img:1",
		Workdir:       agentbox.NewEphemeralWorkdir("coder"),
	}
	if err := box.Save(); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(script,
		[]byte("#!/bin/sh\nsed 's/^account: .*$/account: ghost/' \"$1\" > \"$1.tmp\" && mv \"$1.tmp\" \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)

	err := runEdit(t.Context(), io.Discard, "default", "coder")
	if err == nil {
		t.Fatal("box edit re-pointing at a missing account must fail")
	}
	if !strings.Contains(err.Error(), `account "ghost"`) ||
		!strings.Contains(err.Error(), "yoloctl account create ghost") {
		t.Fatalf("error %q should carry the actionable create hint for the new account", err)
	}
	saved, loadErr := agentbox.Load("default", "coder")
	if loadErr != nil {
		t.Fatalf("reload manifest: %v", loadErr)
	}
	if saved.Account != account.Default {
		t.Fatalf("manifest account = %q; the rejected edit must leave the previous selection", saved.Account)
	}
}
