package account

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestValidateNameReservesNone(t *testing.T) {
	if err := ValidateName(None); err == nil {
		t.Error("ValidateName(none): expected error — none is the reserved no-account sentinel")
	}
	for _, name := range []string{"default", "personal", "work-2"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q): unexpected error %v", name, err)
		}
	}
	for _, name := range []string{"", "../escape", "a/b", "-lead"} {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q): expected error, got nil", name)
		}
	}
	// The reservation holds at every entry point: a "none" manifest can be
	// neither saved nor loaded.
	if err := New("default", None).Save(); err == nil {
		t.Error("Save of account named none: expected error")
	}
	if _, err := Load("default", None); err == nil {
		t.Error("Load none: expected error")
	}
}

func TestSaveLoadSecretsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := New("default", "personal")
	c.SetSecret("ANTHROPIC_API_KEY", "sk-secret-value")
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Manifest holds secrets → mode 0600.
	info, err := os.Stat(c.Path())
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("manifest mode = %o, want 0600", perm)
	}
	got, err := Load("default", "personal")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if got.Secrets["ANTHROPIC_API_KEY"] != "sk-secret-value" {
		t.Errorf("secret value did not round-trip")
	}
	// SecretNames exposes keys only, never values.
	names := got.SecretNames()
	if len(names) != 1 || names[0] != "ANTHROPIC_API_KEY" {
		t.Errorf("SecretNames = %v, want [ANTHROPIC_API_KEY]", names)
	}
	if !got.UnsetSecret("ANTHROPIC_API_KEY") {
		t.Error("UnsetSecret: secret should have existed")
	}
	if _, ok := got.Secrets["ANTHROPIC_API_KEY"]; ok {
		t.Error("UnsetSecret: secret remains")
	}
}

func TestLoadNormalizesRetiredDevelopmentRevision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := layout.AccountPath("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf(
		"schema_version: %d\nname: personal\nrevision: 7\nsecrets:\n  ANTHROPIC_API_KEY: test-value\n",
		retiredDevelopmentSchemaVersion,
	)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load("default", "personal")
	if err != nil {
		t.Fatalf("Load retired development manifest: %v", err)
	}
	if c.SchemaVersion != SchemaVersion {
		t.Fatalf("normalized schema version = %d, want %d", c.SchemaVersion, SchemaVersion)
	}
	if c.Secrets["ANTHROPIC_API_KEY"] != "test-value" {
		t.Fatal("legacy normalization lost the secret value")
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save normalized manifest: %v", err)
	}
	canonical, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), "revision:") {
		t.Fatalf("canonical manifest retained retired revision field:\n%s", canonical)
	}
	if !strings.Contains(string(canonical), fmt.Sprintf("schema_version: %d", SchemaVersion)) {
		t.Fatalf("canonical manifest has wrong schema version:\n%s", canonical)
	}

	// Accounts written during the brief interval after revision was removed
	// used the same development schema number without the field. Normalize
	// those too, so every pre-release account converges on the one schema.
	withoutRevision := fmt.Sprintf(
		"schema_version: %d\nname: personal\n",
		retiredDevelopmentSchemaVersion,
	)
	parsed, err := ParseManifest([]byte(withoutRevision))
	if err != nil {
		t.Fatalf("ParseManifest without retired revision: %v", err)
	}
	if parsed.SchemaVersion != SchemaVersion {
		t.Fatalf("normalized revision-less schema version = %d, want %d", parsed.SchemaVersion, SchemaVersion)
	}
}

func TestLoadMissingIsNotExist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Load("default", "personal")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load of missing account: error %v, want fs.ErrNotExist (so callers can hint to create it)", err)
	}
}

func TestLoadRejectsNameMismatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	// Copy the manifest under a different filename: the path is authoritative
	// for identity, so the mismatched load must fail.
	src, err := Load("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(src.Path())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(filepath.Dir(src.Path()), "work.yaml")
	if err := os.WriteFile(dest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("default", "work"); err == nil {
		t.Error("Load of manifest whose name does not match its location: expected error")
	}
}

func TestList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	empty, err := List("default")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("List with no registry = %v, want empty", empty)
	}
	for _, n := range []string{"work", "default", "personal"} {
		if err := New("default", n).Save(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List("default")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"default", "personal", "work"}
	if len(got) != len(want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List = %v, want %v (sorted)", got, want)
		}
	}
}

func TestStatePaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home, err := StateHome("personal")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := StatePath("personal", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(leaf) != home {
		t.Errorf("leaf %q is not directly under the identity home %q", leaf, home)
	}
	if filepath.Base(leaf) != "claude" {
		t.Errorf("leaf %q should end in the agent name", leaf)
	}
	if _, err := StatePath(None, "claude"); err == nil {
		t.Error("StatePath(none, ...): expected error")
	}
	if _, err := StatePath("personal", "../escape"); err == nil {
		t.Error("StatePath with escaping agent: expected error")
	}
}

// lockInode returns the inode of the account's lock file, failing the test if
// the lock path does not exist. The inode is the identity flock(2) exclusion
// binds to — if it ever changes, holders of the old inode and the new one no
// longer exclude each other.
func lockInode(t *testing.T, name string) uint64 {
	t.Helper()
	lockPath, err := layout.AccountLockPath("default", name)
	if err != nil {
		t.Fatalf("AccountLockPath: %v", err)
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock %s: %v", lockPath, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat lock %s: no Stat_t", lockPath)
	}
	return st.Ino
}

// TestDeleteKeepsLockInode: Delete removes the manifest but must NEVER unlink
// the lock sibling. Unlinking it would let the next Lock create a
// fresh inode at the same path while an in-flight holder still holds the old
// one — two "exclusive" holders at once. The lock inode must survive
// delete + recreate unchanged.
func TestDeleteKeepsLockInode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := New("default", "personal")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	release, err := Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	release()
	before := lockInode(t, "personal")

	if err := c.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(c.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("manifest survives Delete: %v", err)
	}
	if got := lockInode(t, "personal"); got != before {
		t.Fatalf("lock inode changed across Delete: %d -> %d (the lock file must never be unlinked)", before, got)
	}

	// Recreate the account and lock it again: still the same inode.
	if err := New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	release, err = Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if got := lockInode(t, "personal"); got != before {
		t.Fatalf("lock inode changed across delete+recreate: %d -> %d", before, got)
	}
}

// TestLockExcludesAcrossDestroyRecreate is the stable-lock regression: a transaction
// that acquired the account lock BEFORE a destroy+recreate must still exclude
// a locker arriving AFTER it. If destroy unlinked the lock path, the late
// locker would open a brand-new inode and acquire immediately, splitting the
// mutual exclusion across two files.
func TestLockExcludesAcrossDestroyRecreate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := New("default", "personal")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	// An in-flight transaction holds the lock…
	release1, err := Lock("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	// …while the account is destroyed and recreated (manifest churn only; the
	// lock path itself must stay untouched).
	if err := c.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := Lock("default", "personal")
		if err != nil {
			t.Errorf("post-recreate Lock: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		release2()
	}()
	select {
	case <-acquired:
		t.Fatal("a post-destroy locker acquired while the pre-destroy holder was still active — the lock inode was replaced")
	case <-time.After(100 * time.Millisecond):
	}
	release1()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("post-recreate Lock never acquired after the holder released")
	}
}

// TestLockContention verifies the per-account flock serializes concurrent
// mutators (review #4): a second Lock on the same account blocks until the
// first releases. flock(2) locks are per open file description, so two
// acquisitions within one process DO contend — exactly like two yoloctl
// invocations.
func TestLockContention(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	release1, err := Lock("default", "default")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		release2, err := Lock("default", "default")
		if err != nil {
			t.Errorf("second Lock: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		release2()
	}()
	select {
	case <-acquired:
		t.Fatal("second Lock acquired while the first was still held")
	case <-time.After(100 * time.Millisecond):
	}
	release1()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("second Lock never acquired after the first was released")
	}
	// Distinct accounts never contend.
	ra, err := Lock("default", "a")
	if err != nil {
		t.Fatal(err)
	}
	defer ra()
	rb, err := Lock("default", "b")
	if err != nil {
		t.Fatal(err)
	}
	rb()
}

// TestLoadRejectsUnknownFieldsAndTrailingDocs pins strict decoding of the
// account manifest. The keychain is security-sensitive: with a lenient
// parser, a misspelled `secrets:` key silently loads as an EMPTY keychain
// (fail open — the box launches with no injected credentials and no error).
// Unknown fields and stowaway trailing documents must fail closed instead.
func TestLoadRejectsUnknownFieldsAndTrailingDocs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := New("default", "personal").Save(); err != nil {
		t.Fatal(err)
	}
	c, err := Load("default", "personal")
	if err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(c.Path(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Misspelled secrets map: must error naming the field, not load empty.
	write("schema_version: 1\nname: personal\nsecret:\n  ANTHROPIC_API_KEY: v\n")
	if _, err := Load("default", "personal"); err == nil {
		t.Fatal("misspelled `secret:` accepted — the keychain would silently load empty")
	} else if !strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), c.Path()) {
		t.Errorf("error %q should name the unknown field and the manifest path", err)
	}

	// Arbitrary unknown field: same fail-closed rule.
	write("schema_version: 1\nname: personal\nrouting: anywhere\n")
	if _, err := Load("default", "personal"); err == nil {
		t.Fatal("unknown field accepted")
	}

	// Trailing second document.
	write("schema_version: 1\nname: personal\n---\nschema_version: 1\nname: stowaway\n")
	if _, err := Load("default", "personal"); err == nil {
		t.Fatal("trailing YAML document accepted")
	}

	// The exact valid shape still round-trips.
	write("schema_version: 1\nname: personal\nsecrets:\n  ANTHROPIC_API_KEY: v\n")
	got, err := Load("default", "personal")
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if got.Secrets["ANTHROPIC_API_KEY"] != "v" {
		t.Errorf("secrets did not round-trip: %v", got.SecretNames())
	}
}
