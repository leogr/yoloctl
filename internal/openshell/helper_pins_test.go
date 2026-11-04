package openshell

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHelperPinCannotBeOmittedEvenWithRewrittenSubject(t *testing.T) {
	for _, mutate := range []func(*HelperPins){
		func(p *HelperPins) { p.Protocol = "1" },
		func(p *HelperPins) { p.Inputs.SourceSHA256 = "" },
		func(p *HelperPins) { p.Platforms["amd64"] = p.Platforms["arm64"] },
		func(p *HelperPins) { delete(p.Platforms, "arm64") },
		func(p *HelperPins) { p.GoVersion = "devel" },
		func(p *HelperPins) { p.BuildProofSHA256["arm64"] = "" },
	} {
		d := releaseFixture(t)
		mutate(&d.Pins.Helper)
		subject, _ := json.Marshal(d.Pins)
		d.Qualification.SubjectSHA256 = sha256Hex(subject)
		raw, _ := json.Marshal(d)
		if _, err := LoadQualifiedRelease(raw, sha256Hex(raw)); err == nil {
			t.Fatal("unqualified static helper accepted")
		}
	}
}

func TestCompiledSourceBindingRefusesStaleHelper(t *testing.T) {
	d := releaseFixture(t)
	raw, _ := json.Marshal(d)
	r, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.validateHelperInputs(d.Pins.Helper.Inputs); err != nil {
		t.Fatal(err)
	}
	changed := d.Pins.Helper.Inputs
	changed.SourceSHA256 = strings.Repeat("c", 64)
	if err := r.validateHelperInputs(changed); err == nil {
		t.Fatal("stale helper source accepted")
	}
	copy := r.Manifest()
	copy[0] = 'x'
	if sha256Hex(r.Manifest()) != r.Identity() {
		t.Fatal("caller modified retained qualification")
	}
}

// A box made by an earlier build keeps its installed helper: its own release
// gets one notice per box, never a refusal (old-path parity). New creates and
// installs keep the strict source check.
func TestExistingBoxHelperFromEarlierBuildIsANotice(t *testing.T) {
	d := releaseFixture(t)
	raw, _ := json.Marshal(d)
	r, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateHelperSource(); err == nil {
		t.Fatal("fixture unexpectedly matches this binary's helper sources")
	}
	var boxes []string
	old := existingHelperNotice
	existingHelperNotice = func(box string) { boxes = append(boxes, box) }
	defer func() { existingHelperNotice = old }()
	if err := r.checkExistingBoxHelper("chat"); err != nil || len(boxes) != 1 || boxes[0] != "chat" {
		t.Fatal("earlier-build helper refused or not reported", err, boxes)
	}
	if err := (QualifiedRelease{}).checkExistingBoxHelper("chat"); err == nil {
		t.Fatal("missing qualification accepted")
	}
}

// The installers used by an existing box's generation (runtime pair and
// helper) take the same non-refusing check; a new box keeps the strict one.
func TestHelperSourceCheckForNewAndExistingBoxes(t *testing.T) {
	d := releaseFixture(t)
	raw, _ := json.Marshal(d)
	r, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	var boxes []string
	old := existingHelperNotice
	existingHelperNotice = func(box string) { boxes = append(boxes, box) }
	defer func() { existingHelperNotice = old }()
	if err := r.checkHelperSourceFor(""); err == nil || len(boxes) != 0 {
		t.Fatal("new box skipped the strict helper source check", err)
	}
	if err := r.checkHelperSourceFor("chat"); err != nil || len(boxes) != 1 {
		t.Fatal("existing box from an earlier build was refused", err, boxes)
	}
}
