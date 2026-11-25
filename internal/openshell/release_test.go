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
	version := `"version":"` + qualifiedVersion + `"`
	duplicate := []byte(strings.Replace(string(raw), version, version+","+version, 1))
	if string(duplicate) == string(raw) {
		t.Fatal("fixture lacks the version field")
	}
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

// The optional fifth patch (raw main input) chains on the fourth; unknown
// subsequent patches are refused.
func TestQualifiedPatchStackAcceptsOptionalFifthPatch(t *testing.T) {
	p := releaseFixture(t).Pins
	p.Patches[1].Tree = strings.Repeat("d", 40)
	p.Patches = append(p.Patches,
		SourcePatch{qualifiedPatch3Name, sha256Hex([]byte("patch3-fixture")), strings.Repeat("d", 40), strings.Repeat("e", 40)},
		SourcePatch{qualifiedPatch4Name, sha256Hex([]byte("patch4-fixture")), strings.Repeat("e", 40), strings.Repeat("a", 40)},
		SourcePatch{qualifiedPatch5Name, sha256Hex([]byte("patch5-fixture")), strings.Repeat("a", 40), p.ForkTree})
	if !qualifiedPatchStack(p) {
		t.Fatal("five-patch release refused")
	}
	broken := p
	broken.Patches = append([]SourcePatch(nil), p.Patches...)
	broken.Patches[4].ParentTree = strings.Repeat("c", 40)
	if qualifiedPatchStack(broken) {
		t.Fatal("fifth patch accepted off the chain")
	}
	sixth := p
	sixth.Patches = append(append([]SourcePatch(nil), p.Patches...), SourcePatch{"extra.patch", sha256Hex([]byte("6")), p.ForkTree, strings.Repeat("b", 40)})
	sixth.ForkTree = strings.Repeat("b", 40)
	if qualifiedPatchStack(sixth) {
		t.Fatal("unknown sixth patch accepted")
	}
}

func TestQualifiedPatchStackAcceptsOptionalSixthPatch(t *testing.T) {
	p := releaseFixture(t).Pins
	fork := p.ForkTree
	p.Patches[1].Tree = strings.Repeat("d", 40)
	p.Patches = append(p.Patches,
		SourcePatch{qualifiedPatch3Name, sha256Hex([]byte("patch3-fixture")), strings.Repeat("d", 40), strings.Repeat("e", 40)},
		SourcePatch{qualifiedPatch4Name, sha256Hex([]byte("patch4-fixture")), strings.Repeat("e", 40), strings.Repeat("a", 40)},
		SourcePatch{qualifiedPatch5Name, sha256Hex([]byte("patch5-fixture")), strings.Repeat("a", 40), strings.Repeat("b", 40)},
		SourcePatch{qualifiedPatch6Name, sha256Hex([]byte("patch6-fixture")), strings.Repeat("b", 40), fork})
	if !qualifiedPatchStack(p) {
		t.Fatal("six-patch release refused")
	}
	broken := p
	broken.Patches = append([]SourcePatch(nil), p.Patches...)
	broken.Patches[5].ParentTree = strings.Repeat("c", 40)
	if qualifiedPatchStack(broken) {
		t.Fatal("sixth patch accepted off the chain")
	}
	skipped := p
	skipped.Patches = append(append([]SourcePatch(nil), p.Patches[:4]...), SourcePatch{qualifiedPatch6Name, sha256Hex([]byte("patch6-fixture")), strings.Repeat("a", 40), fork})
	if qualifiedPatchStack(skipped) {
		t.Fatal("sixth patch accepted in the fifth position")
	}
	seventh := p
	seventh.Patches = append(append([]SourcePatch(nil), p.Patches...), SourcePatch{"extra.patch", sha256Hex([]byte("7")), fork, strings.Repeat("f", 40)})
	seventh.ForkTree = strings.Repeat("f", 40)
	if qualifiedPatchStack(seventh) {
		t.Fatal("seventh patch accepted")
	}
}

func TestQualifiedPatchStackAcceptsOptionalSeventhAndEighthPatches(t *testing.T) {
	p := releaseFixture(t).Pins
	fork := p.ForkTree
	p.Patches[1].Tree = strings.Repeat("d", 40)
	p.Patches = append(p.Patches,
		SourcePatch{qualifiedPatch3Name, sha256Hex([]byte("patch3-fixture")), strings.Repeat("d", 40), strings.Repeat("e", 40)},
		SourcePatch{qualifiedPatch4Name, sha256Hex([]byte("patch4-fixture")), strings.Repeat("e", 40), strings.Repeat("a", 40)},
		SourcePatch{qualifiedPatch5Name, sha256Hex([]byte("patch5-fixture")), strings.Repeat("a", 40), strings.Repeat("b", 40)},
		SourcePatch{qualifiedPatch6Name, sha256Hex([]byte("patch6-fixture")), strings.Repeat("b", 40), strings.Repeat("c", 40)},
		SourcePatch{qualifiedPatch7Name, sha256Hex([]byte("patch7-fixture")), strings.Repeat("c", 40), fork})
	if !qualifiedPatchStack(p) {
		t.Fatal("seven-patch release refused")
	}
	broken := p
	broken.Patches = append([]SourcePatch(nil), p.Patches...)
	broken.Patches[6].ParentTree = strings.Repeat("f", 40)
	if qualifiedPatchStack(broken) {
		t.Fatal("seventh patch accepted off the chain")
	}
	skipped := p
	skipped.Patches = append(append([]SourcePatch(nil), p.Patches[:5]...), p.Patches[6])
	if qualifiedPatchStack(skipped) {
		t.Fatal("seventh patch accepted without the sixth")
	}
	extra := p
	extra.Patches = append(append([]SourcePatch(nil), p.Patches...), SourcePatch{"extra.patch", sha256Hex([]byte("8")), fork, strings.Repeat("f", 40)})
	extra.ForkTree = strings.Repeat("f", 40)
	if qualifiedPatchStack(extra) {
		t.Fatal("unexpected eighth patch accepted")
	}
	extra.Patches[7].Name = qualifiedPatch8Name
	if !qualifiedPatchStack(extra) {
		t.Fatal("eight-patch release refused")
	}
	extra.Patches[7].ParentTree = strings.Repeat("c", 40)
	if qualifiedPatchStack(extra) {
		t.Fatal("eighth patch accepted off the chain")
	}
	extra.Patches[7].ParentTree = fork
	nine := extra
	nine.Patches = append(append([]SourcePatch(nil), extra.Patches...), SourcePatch{"other.patch", sha256Hex([]byte("9")), strings.Repeat("f", 40), strings.Repeat("9", 40)})
	nine.ForkTree = strings.Repeat("9", 40)
	if qualifiedPatchStack(nine) {
		t.Fatal("unexpected ninth patch accepted")
	}
	// 0.1.6's ninth patch (#5650 backport) is part of v0.1.10: refused for new
	// boxes; existing 0.1.6 boxes keep it under their recorded source.
	nine.Patches[8].Name = qualifiedPatch9Name
	if qualifiedPatchStack(nine) {
		t.Fatal("0.1.6's ninth patch accepted for the current source")
	}
}

func TestRecordedReleaseAcceptsAnEarlierSourceOnlyForExistingBoxes(t *testing.T) {
	earlier := sourceRelease{
		version: "0.0.9-fixture", upstreamCommit: strings.Repeat("d", 40), upstreamTree: strings.Repeat("e", 40),
		patch1Tree: strings.Repeat("f", 40), patch1SHA256: sha256Hex([]byte("earlier-patch1")),
		patchNames:      []string{"unmediated-network.patch", "workspace-read-only.patch", qualifiedPatch3Name},
		bootstrapSchema: BootstrapSchema, protocolFixtureVersion: "earlier-fixture",
	}
	saved := recordedReleases
	recordedReleases = []sourceRelease{earlier}
	t.Cleanup(func() { recordedReleases = saved })

	manifest := func(mutate func(*ReleasePins)) []byte {
		d := releaseFixture(t)
		p := &d.Pins
		p.Version, p.UpstreamCommit, p.UpstreamTree = earlier.version, earlier.upstreamCommit, earlier.upstreamTree
		p.ProtocolFixtureVersion = earlier.protocolFixtureVersion
		p.Patches = []SourcePatch{
			{"unmediated-network.patch", earlier.patch1SHA256, earlier.upstreamTree, earlier.patch1Tree},
			{"workspace-read-only.patch", sha256Hex([]byte("earlier-patch2")), earlier.patch1Tree, strings.Repeat("1", 40)},
			{qualifiedPatch3Name, sha256Hex([]byte("earlier-patch3")), strings.Repeat("1", 40), p.ForkTree},
		}
		if mutate != nil {
			mutate(p)
		}
		subject, err := json.Marshal(d.Pins)
		if err != nil {
			t.Fatal(err)
		}
		d.Qualification.SubjectSHA256 = sha256Hex(subject)
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := manifest(nil)
	if _, err := LoadRecordedRelease(raw, sha256Hex(raw)); err != nil {
		t.Fatalf("existing box's earlier source refused: %v", err)
	}
	if _, err := LoadQualifiedRelease(raw, sha256Hex(raw)); err == nil {
		t.Fatal("earlier source accepted for a new box")
	}
	for name, mutate := range map[string]func(*ReleasePins){
		"current upstream commit": func(p *ReleasePins) { p.UpstreamCommit = qualifiedUpstreamCommit },
		"current fixture version": func(p *ReleasePins) { p.ProtocolFixtureVersion = protocolFixtureVersion },
		"current first patch": func(p *ReleasePins) {
			p.Patches[0] = SourcePatch{"unmediated-network.patch", qualifiedPatch1SHA256, p.UpstreamTree, qualifiedPatch1Tree}
			p.Patches[1].ParentTree = qualifiedPatch1Tree
		},
		"patch beyond its stack": func(p *ReleasePins) {
			p.Patches[2].Tree = strings.Repeat("2", 40)
			p.Patches = append(p.Patches, SourcePatch{qualifiedPatch4Name, sha256Hex([]byte("p4")), strings.Repeat("2", 40), p.ForkTree})
		},
	} {
		raw := manifest(mutate)
		if _, err := LoadRecordedRelease(raw, sha256Hex(raw)); err == nil {
			t.Fatalf("%s: mixed source accepted", name)
		}
	}
	current := releaseFixture(t)
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecordedRelease(raw, sha256Hex(raw)); err != nil {
		t.Fatalf("current release refused for an existing box: %v", err)
	}
}

// A box created by v0.4.x recorded this real nine-patch 0.1.6 manifest (a
// `make build` qualification); boxes from v0.1.0–v0.3.0 recorded four 0.1.6
// patches (pins at tag v0.3.0). Both stay readable for existing boxes and are
// refused for new ones.
func TestRecordedOpenShell016ManifestsStayReadableForExistingBoxes(t *testing.T) {
	raw, err := os.ReadFile("testdata/recorded-0.1.6-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadRecordedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatalf("nine-patch 0.1.6 manifest refused for an existing box: %v", err)
	}
	if r.document.Pins.Version != "0.1.6" || len(r.document.Pins.Patches) != 9 {
		t.Fatalf("unexpected recorded fixture %s/%d", r.document.Pins.Version, len(r.document.Pins.Patches))
	}
	if _, err := LoadQualifiedRelease(raw, sha256Hex(raw)); err == nil {
		t.Fatal("0.1.6 manifest accepted for a new box")
	}

	old := recordedReleases[0]
	d := releaseFixture(t)
	p := &d.Pins
	p.Version, p.UpstreamCommit, p.UpstreamTree = old.version, old.upstreamCommit, old.upstreamTree
	p.ProtocolFixtureVersion = old.protocolFixtureVersion
	// Patch hashes and result trees as pinned at tag v0.3.0.
	p.Patches = []SourcePatch{
		{"unmediated-network.patch", old.patch1SHA256, old.upstreamTree, old.patch1Tree},
		{"workspace-read-only.patch", "77019fe849082581402dc21b366e22afe633973a40aa16d5eba1d66ef8a23b0e", old.patch1Tree, "9112ab178200f6751290ac2c2a20150e31c8708b"},
		{qualifiedPatch3Name, "d927c47d8ed8f6ca95e147a65614acc064d9dea1d1067495838da4c2049eec69", "9112ab178200f6751290ac2c2a20150e31c8708b", "c5f248c7970e0ea23736f242b70668a135af3785"},
		{qualifiedPatch4Name, "2c6c4d4005cf4dec51f02769bf6319f26b736c36b224c6b9007f372d5566ed55", "c5f248c7970e0ea23736f242b70668a135af3785", "a95cfcec31a2627091f4141253c89ef0f88fedae"},
	}
	p.ForkTree = "a95cfcec31a2627091f4141253c89ef0f88fedae"
	subject, err := json.Marshal(d.Pins)
	if err != nil {
		t.Fatal(err)
	}
	d.Qualification.SubjectSHA256 = sha256Hex(subject)
	four, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecordedRelease(four, sha256Hex(four)); err != nil {
		t.Fatalf("four-patch 0.1.6 manifest refused for an existing box: %v", err)
	}
	if _, err := LoadQualifiedRelease(four, sha256Hex(four)); err == nil {
		t.Fatal("four-patch 0.1.6 manifest accepted for a new box")
	}
}
