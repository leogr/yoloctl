package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func registrationScopeRootFixture(t *testing.T, prefix int) string {
	t.Helper()
	root := t.TempDir()
	at := root
	for _, name := range []string{"run", "yoloctl", "operators"}[:prefix] {
		at = filepath.Join(at, name)
		if err := os.Mkdir(at, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestRegistrationScopeQualifiedMissingPrefixIsDistinctFromCapturedEmptyRoot(t *testing.T) {
	for prefix := 0; prefix <= 3; prefix++ {
		t.Run(string(rune('a'+prefix)), func(t *testing.T) {
			root := registrationScopeRootFixture(t, prefix)
			p, err := observeRegistrationScopeAt(t.Context(), root, os.Geteuid(), os.Getegid(), func(ctx context.Context) error { return ctx.Err() })
			if err != nil {
				t.Fatal(err)
			}
			owned := append([]registrationScopeNode(nil), p.nodes...)
			view, err := p.Observation(t.Context(), "box")
			if err != nil {
				t.Fatal(err)
			}
			if prefix < 3 {
				if view.Kind != RegistrationScopePrefixAbsent || view.MissingComponent != []string{"run", "yoloctl", "operators"}[prefix] || len(view.Identities) != 0 {
					t.Fatal("unknown source became absence", view)
				}
			} else if view.Kind != RegistrationScopeRootPresent || view.MissingComponent != "" || len(view.Identities) != 0 {
				t.Fatal("captured empty root became legacy/absent-prefix", view)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			for _, node := range owned {
				if _, err := node.file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatal("owned prefix descriptor leaked", err)
				}
			}
			if _, err := p.Observation(t.Context(), "box"); err == nil {
				t.Fatal("closed original observer returned absence")
			}
		})
	}
}
func TestRegistrationScopeFinalVerifierCannotPublishPreviouslyMissingPrefix(t *testing.T) {
	root := registrationScopeRootFixture(t, 2)
	armed := false
	current := func(ctx context.Context) error {
		if armed {
			armed = false
			if err := os.Mkdir(filepath.Join(root, "run", "yoloctl", "operators"), 0755); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	p, err := observeRegistrationScopeAt(t.Context(), root, os.Geteuid(), os.Getegid(), current)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	armed = true
	if _, err := p.Observation(t.Context(), "box"); err == nil {
		t.Fatal("last verifier publication became current absence")
	}
}
func TestRegistrationScopeOriginalEntriesIncludeKeepAndSameByteReplacementRefuses(t *testing.T) {
	root := registrationScopeRootFixture(t, 3)
	entry := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	dir := filepath.Join(root, "run", "yoloctl", "operators", entry.Incarnation)
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(LegacyRegistrationIdentity(entry))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	p, err := observeRegistrationScopeAt(t.Context(), root, os.Geteuid(), os.Getegid(), func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	view, err := p.Observation(t.Context(), "box")
	if err != nil || len(view.Identities) != 1 || view.Identities[0].Endpoint != entry {
		t.Fatal("same-box keep identity was omitted", err)
	}
	if err := os.Rename(filepath.Join(dir, "identity.json"), filepath.Join(root, "saved-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckOriginal(t.Context()); err == nil {
		t.Fatal("same bytes adopted a new registration identity")
	}
}
