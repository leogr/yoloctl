package openshell

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestIssuerInitializationIsPrivateStableAndConcurrent(t *testing.T) {
	root := t.TempDir()
	const count = 8
	results := make([][]byte, count)
	errors := make([]error, count)
	var group sync.WaitGroup
	for n := range count {
		group.Go(func() { results[n], errors[n] = issuerAt(root, os.Geteuid(), os.Getegid()) })
	}
	group.Wait()
	for n := range count {
		if errors[n] != nil {
			t.Fatal(errors[n])
		}
		if !bytes.Equal(results[0], results[n]) {
			t.Fatal("concurrent initializer replaced the issuer identity")
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "issuer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "signing.key" {
		t.Fatal("issuer staging files leaked")
	}
	info, err := os.Stat(filepath.Join(root, "issuer/signing.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatal("issuer permissions widened")
	}
}

func TestIssuerRefusesUnsafeExistingStateWithoutRepair(t *testing.T) {
	for _, kind := range []string{"symlink-dir", "symlink-key", "hardlink-key", "permissive-key", "invalid-key", "permissive-dir"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if _, err := issuerAt(root, os.Geteuid(), os.Getegid()); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "issuer")
			key := filepath.Join(dir, "signing.key")
			outside := filepath.Join(root, "outside")
			switch kind {
			case "symlink-dir":
				if err := os.Rename(dir, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			case "symlink-key":
				if err := os.Rename(key, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, key); err != nil {
					t.Fatal(err)
				}
			case "hardlink-key":
				if err := os.Link(key, outside); err != nil {
					t.Fatal(err)
				}
			case "permissive-key":
				if err := os.Chmod(key, 0o644); err != nil {
					t.Fatal(err)
				}
			case "invalid-key":
				if err := os.WriteFile(key, []byte("invalid key"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "permissive-dir":
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := issuerAt(root, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("unsafe issuer state adopted or repaired")
			}
			if kind == "permissive-key" {
				info, err := os.Stat(key)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Fatal("unsafe state was silently repaired")
				}
			}
		})
	}
}

func TestNativeIssuerRefusesOrdinaryHostContext(t *testing.T) {
	if _, err := nativeIssuer(); err == nil {
		t.Fatal("ordinary process acquired native issuer authority")
	}
}
