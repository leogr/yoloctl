package operator

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeIdentityPublicationIsExclusiveAndPreservesEveryExistingVariant(t *testing.T) {
	identity := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
	for _, existing := range []string{"absent", "matching", "other-qualification", "legacy", "malformed"} {
		t.Run(existing, func(t *testing.T) {
			directory := identityFixtureRoot(t)
			path := filepath.Join(directory, "identity.json")
			var old []byte
			switch existing {
			case "matching":
				old, _ = json.Marshal(identity)
			case "other-qualification":
				other := identity
				other.Qualification = strings.Repeat("c", 64)
				old, _ = json.Marshal(other)
			case "legacy":
				old, _ = json.Marshal(identity.Endpoint)
			case "malformed":
				old = []byte(`{"box":"box"}`)
			}
			if old != nil {
				if err := os.WriteFile(path, old, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := prepareNativeIdentityAt(directory, identity)
			if existing == "absent" {
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(identity)
				if !bytes.Equal(raw, want) {
					t.Fatal("native publication erased or changed explicit fields")
				}
				decoded, err := DecodeRegistrationIdentity(raw)
				if err != nil || decoded != identity {
					t.Fatal(decoded, err)
				}
				if err := prepareNativeIdentityAt(directory, identity); !errors.Is(err, os.ErrExist) {
					t.Fatal("matching native bytes adopted as fresh creation", err)
				}
				if err := prepareLegacyIdentityAt(directory, identity.Endpoint); !errors.Is(err, ErrNativeRegistration) {
					t.Fatal("legacy producer downgraded native identity", err)
				}
			} else {
				if err == nil {
					t.Fatal("existing identity adopted/replaced")
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(raw, old) {
					t.Fatal("failed publication changed existing bytes", readErr)
				}
			}
		})
	}
}

func TestNativeIdentityPublicationRaceNeverAdoptsConcurrentMatchingBytes(t *testing.T) {
	directory := identityFixtureRoot(t)
	identity := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)})
	raw, _ := json.Marshal(identity)
	called := 0
	err := prepareRegistrationIdentityWithPublisher(directory, identity, false, func(fd int, from, to string) error {
		called++
		// A competing original publisher wins after initial absence. The
		// actual no-replace rename must retain that winner and refuse adoption.
		if err := os.WriteFile(filepath.Join(directory, "identity.json"), raw, 0o644); err != nil {
			return err
		}
		return renameLegacyIdentityNoReplace(fd, from, to)
	})
	if !errors.Is(err, os.ErrExist) || called != 1 {
		t.Fatal("concurrent matching identity was adopted/retried", err, called)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != "identity.json" {
		t.Fatal("original temporary leaf not retired", entries, readErr)
	}
	actual, _ := os.ReadFile(filepath.Join(directory, "identity.json"))
	if !bytes.Equal(actual, raw) {
		t.Fatal("winner changed")
	}
}

func TestNativeIdentityPublicationRefusesLegacyInputAndLinksBeforeRename(t *testing.T) {
	identity := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)})
	for _, kind := range []string{"legacy", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			directory := identityFixtureRoot(t)
			if kind == "legacy" {
				if err := prepareNativeIdentityAt(directory, LegacyRegistrationIdentity(identity.Endpoint)); err == nil {
					t.Fatal("legacy variant accepted by native writer")
				}
				return
			}
			outside := filepath.Join(identityFixtureRoot(t), "original")
			if err := os.WriteFile(outside, []byte("retained"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "identity.json")
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(outside, path); err != nil {
				t.Fatal(err)
			}
			called := false
			err := prepareRegistrationIdentityWithPublisher(directory, identity, false, func(int, string, string) error { called = true; return unix.EIO })
			if err == nil || called {
				t.Fatal("foreign linked identity reached publication")
			}
			old, _ := os.ReadFile(outside)
			if string(old) != "retained" {
				t.Fatal("foreign file changed")
			}
		})
	}
}
