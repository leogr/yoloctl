package openshellrelease

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/nativeartifacts"
	"github.com/leogr/yoloctl/internal/openshell"
)

func testHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func fixtureSelection(t *testing.T, mutate func(*openshell.ReleasePins)) ([]byte, string, []byte) {
	t.Helper()
	// Fictional qualification stays test-only. Its hash is never installed as a
	// production trust root and its helper is never fetched/built/launched here.
	raw, err := os.ReadFile("../openshell/testdata/qualification-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Pins          openshell.ReleasePins         `json:"pins"`
		Qualification openshell.QualificationRecord `json:"qualification"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	rego := []byte("paired-rego-fixture")
	document.Pins.RegoSHA256 = testHash(rego)
	if mutate != nil {
		mutate(&document.Pins)
	}
	subject, err := json.Marshal(document.Pins)
	if err != nil {
		t.Fatal(err)
	}
	document.Qualification.SubjectSHA256 = testHash(subject)
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw, testHash(raw), rego
}

// A complete selection written by make native-qualify is usable only while
// its helper inputs match this compilation. Source edits may legitimately
// leave a stale selection until the next qualification: Load must refuse it,
// never fall back to the fixture or manufacture a new trust hash.
func TestCompiledNativeReleaseUnavailableCannotUseFixtureFallback(t *testing.T) {
	manifest, reviewedHash, compiledRego, selErr := nativeartifacts.Selection()
	if selErr != nil {
		t.Fatal(selErr)
	}
	if len(manifest) == 0 {
		manifest, reviewedHash, compiledRego = assets.NativeOpenShellQualification() // committed default
	}
	release, rego, err := Load()
	if len(manifest) == 0 {
		if !errors.Is(err, ErrUnavailable) || release.Identity() != "" || rego != nil {
			t.Fatalf("missing final production evidence acquired runtime authority: %q %v %v", release.Identity(), len(rego), err)
		}
		return
	}
	fixture, err2 := os.ReadFile("../openshell/testdata/qualification-fixture.json")
	if err2 != nil {
		t.Fatal(err2)
	}
	if bytes.Equal(manifest, fixture) {
		t.Fatal("production selection is the test fixture")
	}
	selected, selectionErr := openshell.LoadQualifiedRelease(manifest, reviewedHash)
	if selectionErr != nil {
		t.Fatalf("invalid compiled qualification: %v", selectionErr)
	}
	var document struct {
		Pins openshell.ReleasePins `json:"pins"`
	}
	if json.Unmarshal(selected.Manifest(), &document) != nil || testHash(compiledRego) != document.Pins.RegoSHA256 {
		t.Fatal("compiled Rego differs from its selected qualification")
	}
	if sourceErr := selected.ValidateHelperSource(); sourceErr != nil {
		if err == nil || release.Identity() != "" || rego != nil || !strings.Contains(err.Error(), sourceErr.Error()) {
			t.Fatalf("stale helper selection gained authority or lost its source refusal: %q %d %v", release.Identity(), len(rego), err)
		}
		return
	}
	if err != nil || release.Identity() != testHash(manifest) || !bytes.Equal(rego, compiledRego) {
		t.Fatalf("compiled selection: %q %d %v", release.Identity(), len(rego), err)
	}
}

func TestCompiledNativeReleasePrivateSelectionChecksTrustRegoAndSource(t *testing.T) {
	for _, failure := range []string{"", "stale hash", "different Rego", "missing Rego", "oversized Rego", "missing manifest", "missing checker", "source mismatch"} {
		t.Run(failure, func(t *testing.T) {
			raw, hash, rego := fixtureSelection(t, nil)
			sourceErr := errors.New("final helper source differs")
			sourceCalls := 0
			check := func(release openshell.QualifiedRelease) error {
				sourceCalls++
				if release.Identity() != hash {
					t.Fatal("source checker received another release")
				}
				if failure == "source mismatch" {
					return sourceErr
				}
				return nil
			}
			switch failure {
			case "stale hash":
				hash = strings.Repeat("a", 64)
			case "different Rego":
				rego = []byte("different")
			case "missing Rego":
				rego = nil
			case "oversized Rego":
				rego = bytes.Repeat([]byte("x"), (1<<20)+1)
			case "missing manifest":
				raw = nil
			case "missing checker":
				check = nil
			}
			release, paired, err := loadSelection(raw, hash, rego, check)
			if failure == "" {
				if err != nil || release.Identity() != hash || !bytes.Equal(paired, rego) || sourceCalls != 1 {
					t.Fatalf("paired selection failed: %q %v %v calls=%d", release.Identity(), paired, err, sourceCalls)
				}
				raw[0] = 'x'
				rego[0] = 'x'
				if testHash(release.Manifest()) != hash || bytes.Equal(paired, rego) {
					t.Fatal("caller modified retained qualification/Rego")
				}
				return
			}
			if err == nil || release.Identity() != "" || paired != nil {
				t.Fatalf("failed qualification yielded runtime authority: %q %v %v", release.Identity(), paired, err)
			}
			wantCalls := 0
			if failure == "source mismatch" {
				wantCalls = 1
				if !errors.Is(err, sourceErr) {
					t.Fatal("lost original source refusal", err)
				}
			}
			if sourceCalls != wantCalls {
				t.Fatalf("qualification refusal ordering changed: calls=%d want=%d", sourceCalls, wantCalls)
			}
		})
	}
}

func TestCompiledNativeReleaseRefusesIncompletePlatformOrProtocol(t *testing.T) {
	for _, mutate := range []func(*openshell.ReleasePins){
		func(p *openshell.ReleasePins) { delete(p.Platforms, "arm64") },
		func(p *openshell.ReleasePins) { p.BootstrapSchema++ },
		func(p *openshell.ReleasePins) { p.Helper.Protocol = "unreviewed" },
	} {
		raw, hash, rego := fixtureSelection(t, mutate)
		check := func(openshell.QualifiedRelease) error { t.Fatal("source checked invalid release"); return nil }
		release, paired, err := loadSelection(raw, hash, rego, check)
		if err == nil || release.Identity() != "" || paired != nil {
			t.Fatal("incomplete release became qualified", err)
		}
	}
}
