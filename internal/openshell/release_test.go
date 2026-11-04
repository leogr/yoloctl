package openshell

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	helperinputs "github.com/leogr/yoloctl"
)

// These intentionally fictional pins exercise qualification; they are never
// installed, fetched or used as production release metadata.
func releaseFixture(t *testing.T) releaseDocument {
	t.Helper()
	platforms := map[string]PlatformArtifacts{}
	for _, arch := range []string{"amd64", "arm64"} {
		artifact := func(member string) Artifact {
			return Artifact{Member: member, URL: EmbeddedArchivePrefix + member + "-" + arch + ".tar.gz",
				ArchiveSHA256: sha256Hex([]byte("fixture-archive-" + arch + member)), BinarySHA256: sha256Hex([]byte("fixture-binary-" + arch + member))}
		}
		platforms[arch] = PlatformArtifacts{Boundary: artifact("openshell-sandbox"), Supervisor: artifact("openshell-supervisor"), BoundaryLoader: "test-only-static", SupervisorLoader: "test-only-static"}
	}
	d := releaseDocument{Pins: ReleasePins{
		Version: qualifiedVersion, UpstreamCommit: qualifiedUpstreamCommit, UpstreamTree: qualifiedUpstreamTree,
		ForkCommit: strings.Repeat("a", 40), ForkTree: strings.Repeat("b", 40), BootstrapSchema: BootstrapSchema,
		ProtocolFixtureVersion: protocolFixtureVersion, ProtocolFixtureSHA256: sha256Hex([]byte("fixture")), RegoSHA256: sha256Hex([]byte("rego-fixture")),
		Patches: []SourcePatch{{"unmediated-network.patch", qualifiedPatch1SHA256, qualifiedUpstreamTree, qualifiedPatch1Tree},
			{"workspace-read-only.patch", sha256Hex([]byte("patch2-fixture")), qualifiedPatch1Tree, strings.Repeat("b", 40)}},
		Platforms: platforms,
		Helper: HelperPins{Protocol: HelperProtocol, Inputs: helperinputs.Identity{Schema: helperinputs.Schema, SourceSHA256: sha256Hex([]byte("helper-source-fixture")),
			Modules: []helperinputs.Module{{Path: "golang.org/x/crypto", Version: "v0.54.0", Sum: "h1:test-only"}, {Path: "golang.org/x/mod", Version: "v0.35.0", Sum: "h1:test-only"}, {Path: "golang.org/x/sys", Version: "v0.47.0", Sum: "h1:test-only"}, {Path: "gopkg.in/yaml.v3", Version: "v3.0.1", Sum: "h1:test-only"}}},
			GoVersion: "go1.27.0", SourceProofSHA256: sha256Hex([]byte("helper-source-proof-fixture")),
			BuildProofSHA256: map[string]string{"amd64": sha256Hex([]byte("helper-amd64-proof-fixture")), "arm64": sha256Hex([]byte("helper-arm64-proof-fixture"))},
			Platforms: map[string]Artifact{
				"amd64": {Member: HelperMember, URL: EmbeddedArchivePrefix + "helper-amd64.tar.gz", ArchiveSHA256: sha256Hex([]byte("helper-amd64-archive-fixture")), BinarySHA256: sha256Hex([]byte("helper-amd64-binary-fixture"))},
				"arm64": {Member: HelperMember, URL: EmbeddedArchivePrefix + "helper-arm64.tar.gz", ArchiveSHA256: sha256Hex([]byte("helper-arm64-archive-fixture")), BinarySHA256: sha256Hex([]byte("helper-arm64-binary-fixture"))},
			}},
	}, Qualification: QualificationRecord{Revision: "test-fixture", SourceProofSHA256: sha256Hex([]byte("source-fixture")),
		BuildProofSHA256: map[string]string{"amd64": sha256Hex([]byte("amd64-fixture")), "arm64": sha256Hex([]byte("arm64-fixture"))}}}
	subject, err := json.Marshal(d.Pins)
	if err != nil {
		t.Fatal(err)
	}
	d.Qualification.SubjectSHA256 = sha256Hex(subject)
	return d
}

func TestQualificationRejectsEveryChangedPinAgainstOldProof(t *testing.T) {
	for _, mutate := range []func(*releaseDocument){
		func(d *releaseDocument) { d.Pins.Version = "0.1.7" },
		func(d *releaseDocument) { d.Pins.UpstreamCommit = strings.Repeat("c", 40) },
		func(d *releaseDocument) { d.Pins.ForkTree = strings.Repeat("c", 40) },
		func(d *releaseDocument) { d.Pins.BootstrapSchema++ },
		func(d *releaseDocument) { d.Pins.ProtocolFixtureVersion = "old-schema" },
		func(d *releaseDocument) { d.Pins.ProtocolFixtureSHA256 = sha256Hex([]byte("modified flag fixture")) },
		func(d *releaseDocument) { d.Pins.RegoSHA256 = sha256Hex([]byte("modified rego")) },
		func(d *releaseDocument) { d.Pins.Patches = d.Pins.Patches[:1] },
		func(d *releaseDocument) { d.Pins.Patches[0], d.Pins.Patches[1] = d.Pins.Patches[1], d.Pins.Patches[0] },
		func(d *releaseDocument) { d.Pins.Patches[1].SHA256 = sha256Hex([]byte("changed patch")) },
		func(d *releaseDocument) {
			p := d.Pins.Platforms["amd64"]
			p.Supervisor.BinarySHA256 = sha256Hex([]byte("stale supervisor"))
			d.Pins.Platforms["amd64"] = p
		},
		func(d *releaseDocument) { delete(d.Pins.Platforms, "arm64") },
		func(d *releaseDocument) { d.Pins.Helper.Protocol = "1" },
		func(d *releaseDocument) {
			d.Pins.Helper.Inputs.SourceSHA256 = sha256Hex([]byte("changed helper source"))
		},
		func(d *releaseDocument) { delete(d.Pins.Helper.Platforms, "arm64") },
	} {
		d := releaseFixture(t)
		mutate(&d)
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		// Even changing the outer file's expected hash cannot silently refresh
		// the qualification subject. Check mode must not regenerate it.
		if _, err := LoadQualifiedRelease(raw, sha256Hex(raw)); err == nil {
			t.Fatal("changed pins accepted with stale qualification")
		}
	}
}

func TestQualifiedPairAndStrictManifest(t *testing.T) {
	d := releaseFixture(t)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Platform("amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Platform("arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Platform("other"); err == nil {
		t.Fatal("unqualified architecture accepted")
	}
	if _, err := (QualifiedRelease{}).Platform("amd64"); err == nil {
		t.Fatal("zero release accepted")
	}
	if _, err := LoadQualifiedRelease(append(raw, ' '), sha256Hex(raw)); err == nil {
		t.Fatal("changed manifest bytes accepted")
	}
	for _, suffix := range []string{`,"unexpected":true}`, `}{}`} {
		modified := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(suffix)...)
		if _, err := LoadQualifiedRelease(modified, sha256Hex(modified)); err == nil {
			t.Fatal("unknown or trailing manifest accepted")
		}
	}
}

func TestQualificationSubjectMatchesPythonVendorCheckerFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualification-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadQualifiedRelease(raw, sha256Hex(raw)); err != nil {
		t.Fatal("Go qualification subject differs from the Python checker fixture:", err)
	}
}

func TestQualificationRejectsDuplicateJSONKeys(t *testing.T) {
	d := releaseFixture(t)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := []byte(strings.Replace(string(raw), `"version":"0.1.6"`, `"version":"0.1.6","version":"0.1.6"`, 1))
	if _, err := LoadQualifiedRelease(duplicate, sha256Hex(duplicate)); err == nil {
		t.Fatal("duplicate manifest keys accepted under last-value-wins decoding")
	}
}

// The optional third patch (v0.0.72 syscall parity) is accepted only by name,
// chained on the second patch's tree and ending at the fork tree; two-patch
// releases stay valid for boxes recorded with them.
func TestQualifiedPatchStackAcceptsOptionalThirdPatch(t *testing.T) {
	d := releaseFixture(t)
	if !qualifiedPatchStack(d.Pins) {
		t.Fatal("two-patch release refused")
	}
	third := func() ReleasePins {
		p := releaseFixture(t).Pins
		p.Patches[1].Tree = strings.Repeat("d", 40)
		p.Patches = append(p.Patches, SourcePatch{qualifiedPatch3Name, sha256Hex([]byte("patch3-fixture")), strings.Repeat("d", 40), p.ForkTree})
		return p
	}
	if !qualifiedPatchStack(third()) {
		t.Fatal("three-patch release refused")
	}
	for name, mutate := range map[string]func(*ReleasePins){
		"wrong-name":    func(p *ReleasePins) { p.Patches[2].Name = "other.patch" },
		"broken-chain":  func(p *ReleasePins) { p.Patches[2].ParentTree = strings.Repeat("e", 40) },
		"not-fork-tree": func(p *ReleasePins) { p.Patches[2].Tree = strings.Repeat("e", 40) },
		"bad-sha":       func(p *ReleasePins) { p.Patches[2].SHA256 = "x" },
		"unnamed-fourth": func(p *ReleasePins) {
			p.Patches = append(p.Patches, SourcePatch{"extra.patch", sha256Hex([]byte("4")), p.ForkTree, p.ForkTree})
		},
		"fifth": func(p *ReleasePins) {
			p.Patches = append(p.Patches, SourcePatch{qualifiedPatch4Name, sha256Hex([]byte("4")), p.ForkTree, strings.Repeat("f", 40)},
				SourcePatch{"extra.patch", sha256Hex([]byte("5")), strings.Repeat("f", 40), strings.Repeat("f", 40)})
			p.ForkTree = strings.Repeat("f", 40)
		},
	} {
		p := third()
		mutate(&p)
		if qualifiedPatchStack(p) {
			t.Fatal("accepted", name)
		}
	}
}

// The optional fourth patch (process-group signals) chains on the third.
func TestQualifiedPatchStackAcceptsOptionalFourthPatch(t *testing.T) {
	p := releaseFixture(t).Pins
	p.Patches[1].Tree = strings.Repeat("d", 40)
	p.Patches = append(p.Patches,
		SourcePatch{qualifiedPatch3Name, sha256Hex([]byte("patch3-fixture")), strings.Repeat("d", 40), strings.Repeat("e", 40)},
		SourcePatch{qualifiedPatch4Name, sha256Hex([]byte("patch4-fixture")), strings.Repeat("e", 40), p.ForkTree})
	if !qualifiedPatchStack(p) {
		t.Fatal("four-patch release refused")
	}
	p.Patches[3].ParentTree = strings.Repeat("c", 40)
	if qualifiedPatchStack(p) {
		t.Fatal("fourth patch accepted off the chain")
	}
}
