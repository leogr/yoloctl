package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareNativeRefusesOrdinaryHostBeforeWriting(t *testing.T) {
	if _, err := PrepareNative(context.Background(), bootstrapInput(), QualifiedRelease{}, "", false, nil, nil); err == nil {
		t.Fatal("ordinary process prepared native runtime authority")
	}
}

func TestFreshRuntimeDirectoryNeverAdoptsAnExistingGeneration(t *testing.T) {
	root := t.TempDir()
	name := "g" + strings.Repeat("a", 48)
	created, err := freshGenerationDirectory(root, name)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "retained"), []byte("proof"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err = freshGenerationDirectory(root, name)
	if created || !errors.Is(err, os.ErrExist) {
		t.Fatal("existing runtime generation adopted", created, err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, name, "retained")); err != nil || string(raw) != "proof" {
		t.Fatal("existing generation was changed")
	}
	for _, name := range []string{"", "..", "/outside", "g" + strings.Repeat("a", 47) + "/", "g" + strings.Repeat("A", 48)} {
		created, err := freshGenerationDirectory(root, name)
		if created || err == nil {
			t.Fatal("invalid generation created")
		}
	}
}

func TestCanonicalSessionIdentifierRefusesAliasesAndNil(t *testing.T) {
	for _, invalid := range []string{"", "00000000-0000-0000-0000-000000000000", "{12345678-1234-4234-8234-123456789abc}", "12345678-1234-4234-8234-123456789ABC", "12345678123442348234123456789abc"} {
		if canonicalUUID(invalid) {
			t.Fatal("invalid session ID accepted:", invalid)
		}
	}
	if !canonicalUUID("12345678-1234-4234-8234-123456789abc") {
		t.Fatal("canonical session ID refused")
	}
}
