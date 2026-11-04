package operator

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func identityFixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeTaggedIdentityFixture(t *testing.T, root string, identity RegistrationIdentity) []byte {
	t.Helper()
	dir := filepath.Join(root, identity.Incarnation)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	writeIdentityFixtureBytes(t, dir, raw)
	return raw
}

func writeIdentityFixtureBytes(t *testing.T, dir string, raw []byte) {
	t.Helper()
	name := filepath.Join(dir, "identity.json")
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGenericRemoveBoxPreflightsMalformedBeforeEveryDelete(t *testing.T) {
	for _, scenario := range []string{"malformed-kept", "malformed-last"} {
		t.Run(scenario, func(t *testing.T) {
			root := identityFixtureRoot(t)
			legacy := LegacyRegistrationIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
			last := LegacyRegistrationIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("f", 32)})
			writeTaggedIdentityFixture(t, root, legacy)
			writeTaggedIdentityFixture(t, root, last)
			keep := ""
			if scenario == "malformed-kept" {
				keep = last.Incarnation
			}
			writeIdentityFixtureBytes(t, filepath.Join(root, last.Incarnation), []byte(`{"box":"box"}`))
			calls := 0
			err := removeBoxAt(root, "box", keep, func(string) error { calls++; return nil })
			if err == nil || calls != 0 {
				t.Fatal("generic path deleted before complete classification", calls, err)
			}
		})
	}
}

// As on the old path, the box's cleanup removes every registration of the box,
// native ones included (a create that failed before its container leaves one),
// except the kept incarnation.
func TestGenericRemoveBoxRemovesNativeRegistrationsOfTheBox(t *testing.T) {
	root := identityFixtureRoot(t)
	legacy := LegacyRegistrationIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
	native := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)})
	kept := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("c", 32)})
	foreign := taggedNativeIdentity(Endpoint{Box: "other", Incarnation: strings.Repeat("d", 32)})
	for _, i := range []RegistrationIdentity{legacy, native, kept, foreign} {
		writeTaggedIdentityFixture(t, root, i)
	}
	var selected []string
	if err := removeBoxAt(root, "box", kept.Incarnation, func(path string) error { selected = append(selected, path); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, legacy.Incarnation), filepath.Join(root, native.Incarnation)}
	if len(selected) != 2 || selected[0] != want[0] || selected[1] != want[1] {
		t.Fatalf("selected %v, want %v", selected, want)
	}
}

func TestGenericRemoveBoxPreservesLegacyKeepAndForeignNativeIdentity(t *testing.T) {
	root := identityFixtureRoot(t)
	a := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	b := Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)}
	foreign := Endpoint{Box: "other", Incarnation: strings.Repeat("c", 32)}
	writeTaggedIdentityFixture(t, root, LegacyRegistrationIdentity(a))
	writeTaggedIdentityFixture(t, root, LegacyRegistrationIdentity(b))
	writeTaggedIdentityFixture(t, root, taggedNativeIdentity(foreign))
	var selected []string
	if err := removeBoxAt(root, "box", b.Incarnation, func(path string) error { selected = append(selected, path); return nil }); err != nil {
		t.Fatal("old legacy keep path rejected", err)
	}
	if len(selected) != 1 || selected[0] != filepath.Join(root, a.Incarnation) {
		t.Fatal("keep or foreign registration selected", selected)
	}
}

func TestLegacyIdentityPreparationRetainsExistingBytesAndRefusesNative(t *testing.T) {
	e := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	for _, shape := range []string{"new", "matching-legacy", "native", "conflicting-legacy", "malformed"} {
		t.Run(shape, func(t *testing.T) {
			dir := identityFixtureRoot(t)
			var original []byte
			switch shape {
			case "matching-legacy":
				original = []byte(" { \"incarnation\":\"" + e.Incarnation + "\", \"box\":\"box\" } \n")
			case "native":
				original, _ = json.Marshal(taggedNativeIdentity(e))
			case "conflicting-legacy":
				original, _ = json.Marshal(Endpoint{Box: "other", Incarnation: e.Incarnation})
			case "malformed":
				original = []byte(`{"box":"box"}`)
			}
			if original != nil {
				writeIdentityFixtureBytes(t, dir, original)
			}
			err := prepareLegacyIdentityAt(dir, e)
			if shape == "new" || shape == "matching-legacy" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("legacy Prepare downgraded existing native/conflicting metadata")
			}
			if shape == "native" && !errors.Is(err, ErrNativeRegistration) {
				t.Fatal("native identity was not explicitly refused", err)
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, "identity.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if original != nil && !bytes.Equal(raw, original) {
				t.Fatal("existing identity bytes were rewritten")
			}
			if shape == "new" {
				want, _ := json.Marshal(e)
				if !bytes.Equal(raw, want) {
					t.Fatal("new legacy producer bytes changed")
				}
			}
			names, readErr := os.ReadDir(dir)
			if readErr != nil || len(names) != 1 || names[0].Name() != "identity.json" {
				t.Fatal("legacy identity staging artifacts remained", readErr)
			}
		})
	}
}

func TestGenericEndpointJSONCannotEraseNativeTag(t *testing.T) {
	original := Endpoint{Box: "old", Incarnation: strings.Repeat("a", 32)}
	native := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)})
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	e := original
	if err := json.Unmarshal(raw, &e); !errors.Is(err, ErrNativeRegistration) || e != original {
		t.Fatal("generic JSON reader erased native tag", err, e)
	}
	raw, err = json.Marshal(native.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &e); err != nil || e != native.Endpoint {
		t.Fatal("generic legacy identity decode changed", err)
	}
	native.Incarnation = strings.Repeat("A", 32)
	if err := native.Validate(); err == nil {
		t.Fatal("noncanonical native incarnation accepted")
	}
}

func TestLegacyIdentityPublicationCannotReplaceConcurrentNativeIdentity(t *testing.T) {
	dir := identityFixtureRoot(t)
	e := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	native, err := json.Marshal(taggedNativeIdentity(e))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = prepareLegacyIdentityWithPublisher(dir, e, func(fd int, from, to string) error {
		calls++
		if err := os.WriteFile(filepath.Join(dir, "identity.json"), native, 0644); err != nil {
			return err
		}
		return renameLegacyIdentityNoReplace(fd, from, to)
	})
	if !errors.Is(err, ErrNativeRegistration) || calls != 1 {
		t.Fatal("no-replace publication downgraded concurrent native identity", err, calls)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err != nil || !bytes.Equal(raw, native) {
		t.Fatal("concurrent native bytes changed", err)
	}
	names, err := os.ReadDir(dir)
	if err != nil || len(names) != 1 {
		t.Fatal("own identity staging was not retired", err)
	}
}

func TestLegacyIdentityPublicationRefusesSymlinkParent(t *testing.T) {
	root := identityFixtureRoot(t)
	outside := identityFixtureRoot(t)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if err := prepareLegacyIdentityAt(alias, Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}); err == nil {
		t.Fatal("legacy publication followed symlink ancestor")
	}
	names, err := os.ReadDir(outside)
	if err != nil || len(names) != 0 {
		t.Fatal("symlink target changed", err)
	}
}
