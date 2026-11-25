package openshell

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/layout"
)

const (
	qualifiedVersion        = "0.1.10"
	qualifiedUpstreamCommit = "152c59d3178039e3fbb4a5595f9fff7216a6be92"
	qualifiedUpstreamTree   = "7b67b7ce2816789aff4d334e1c4a9a0d7a7c1c9a"
	qualifiedPatch1Tree     = "46bcd43d1ee03890d995f6605b5986d07818730c"
	qualifiedPatch1SHA256   = "3c5a5700f4ec525b53c7584d5c78d01df3196595707a614609cb3c69f4cee1fa"
	// Optional third patch restoring the workload calls v0.0.72 allowed, and
	// optional fourth restoring process-group signals.
	qualifiedPatch3Name    = "v0072-syscall-parity.patch"
	qualifiedPatch4Name    = "v0072-process-group-signals.patch"
	qualifiedPatch5Name    = "main-raw-input.patch"
	qualifiedPatch6Name    = "main-attachments-ended.patch"
	qualifiedPatch7Name    = "main-output-backpressure.patch"
	qualifiedPatch8Name    = "cargo-ca-bundle.patch"
	qualifiedPatch9Name    = "dns-mapping-refresh.patch"
	protocolFixtureVersion = "workspace-read-only-v2"
)

type SourcePatch struct {
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	ParentTree string `json:"parent_tree"`
	Tree       string `json:"tree"`
}

type PlatformArtifacts struct {
	Boundary   Artifact `json:"boundary"`
	Supervisor Artifact `json:"supervisor"`
	// Loader records the qualified ELF/libc contract, including static linkage
	// when used. It is evidence to check against custom bases, not a fallback.
	BoundaryLoader   string `json:"boundary_loader"`
	SupervisorLoader string `json:"supervisor_loader"`
}

// ReleasePins is the complete reviewed supply-chain subject. Ordinary builds
// consume these fixed assets; they never compile Rust or refresh qualification.
type ReleasePins struct {
	Version                string                       `json:"version"`
	UpstreamCommit         string                       `json:"upstream_commit"`
	UpstreamTree           string                       `json:"upstream_tree"`
	ForkCommit             string                       `json:"fork_commit"`
	ForkTree               string                       `json:"fork_tree"`
	Patches                []SourcePatch                `json:"patches"`
	Platforms              map[string]PlatformArtifacts `json:"platforms"`
	RegoSHA256             string                       `json:"rego_sha256"`
	BootstrapSchema        int                          `json:"bootstrap_schema"`
	ProtocolFixtureVersion string                       `json:"protocol_fixture_version"`
	ProtocolFixtureSHA256  string                       `json:"protocol_fixture_sha256"`
	Helper                 HelperPins                   `json:"helper"`
}

// QualificationRecord pins previously reviewed evidence. A subject digest is
// not proof by itself: source reconstruction and two-build evidence must be
// independently verified before this record and its outer hash are selected.
type QualificationRecord struct {
	Revision          string            `json:"revision"`
	SubjectSHA256     string            `json:"subject_sha256"`
	SourceProofSHA256 string            `json:"source_proof_sha256"`
	BuildProofSHA256  map[string]string `json:"build_proof_sha256"`
}

type releaseDocument struct {
	Pins          ReleasePins         `json:"pins"`
	Qualification QualificationRecord `json:"qualification"`
}

// QualifiedRelease is opaque so a runtime cannot turn arbitrary caller pins
// into a qualified release by setting a boolean. Its zero value is unusable.
type QualifiedRelease struct {
	document       releaseDocument
	manifestSHA256 string
	manifest       []byte
}

func (r QualifiedRelease) Identity() string { return r.manifestSHA256 }

// LoadQualifiedRelease verifies the complete checked-in manifest against the
// binary's independently selected hash, then its qualification subject. Check
// mode and runtime never fill missing fields or bless a modified subject.
func LoadQualifiedRelease(raw []byte, reviewedSHA256 string) (QualifiedRelease, error) {
	return loadRelease(raw, reviewedSHA256, []sourceRelease{currentRelease})
}

// LoadRecordedRelease is LoadQualifiedRelease for the manifest an existing box
// recorded: it also accepts the earlier sources in recordedReleases, each
// under its own complete rules. New boxes and helper installs never use it.
func LoadRecordedRelease(raw []byte, reviewedSHA256 string) (QualifiedRelease, error) {
	return loadRelease(raw, reviewedSHA256, append([]sourceRelease{currentRelease}, recordedReleases...))
}

func loadRelease(raw []byte, reviewedSHA256 string, sources []sourceRelease) (QualifiedRelease, error) {
	if len(raw) > 1<<20 || !lowerHex(reviewedSHA256, 32) || sha256Hex(raw) != reviewedSHA256 {
		return QualifiedRelease{}, errors.New("OpenShell manifest differs from the reviewed qualification")
	}
	if err := checkJSONKeys(raw); err != nil {
		return QualifiedRelease{}, err
	}
	var document releaseDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return QualifiedRelease{}, fmt.Errorf("OpenShell manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return QualifiedRelease{}, errors.New("trailing OpenShell manifest data")
	}
	if err := validateQualification(document, sources); err != nil {
		return QualifiedRelease{}, err
	}
	return QualifiedRelease{document: document, manifestSHA256: reviewedSHA256, manifest: bytes.Clone(raw)}, nil
}

// sourceRelease is one OpenShell source this build can read: its version, the
// pinned upstream, the fixed first patch, the ordered patch names (the first
// two always present, then any contiguous prefix of the rest), and the
// bootstrap schema and protocol fixture its launches used.
type sourceRelease struct {
	version, upstreamCommit, upstreamTree string
	patch1Tree, patch1SHA256              string
	patchNames                            []string
	bootstrapSchema                       int
	protocolFixtureVersion                string
}

// currentRelease is the only source new boxes and helper installs accept.
var currentRelease = sourceRelease{
	version: qualifiedVersion, upstreamCommit: qualifiedUpstreamCommit, upstreamTree: qualifiedUpstreamTree,
	patch1Tree: qualifiedPatch1Tree, patch1SHA256: qualifiedPatch1SHA256,
	patchNames: []string{
		"unmediated-network.patch", "workspace-read-only.patch",
		qualifiedPatch3Name, qualifiedPatch4Name, qualifiedPatch5Name,
		qualifiedPatch6Name, qualifiedPatch7Name, qualifiedPatch8Name,
	},
	bootstrapSchema: BootstrapSchema, protocolFixtureVersion: protocolFixtureVersion,
}

// recordedReleases are earlier sources that existing boxes may have recorded.
// A box keeps the runtime it was created with until it is recreated, so the
// build reads it, and stops, starts and restarts it, under that box's own
// source rules. They are never offered to a new box or helper install.
var recordedReleases = []sourceRelease{{
	// OpenShell 0.1.6 (upstream ac7f615c): releases v0.1.0–v0.3.0 recorded
	// four patches, v0.4.x nine. Patch 9 is upstream's #5650, part of v0.1.10.
	version: "0.1.6", upstreamCommit: "ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47", upstreamTree: "c9c1f358cd6f68445c88b65a5e4983a5213e6bab",
	patch1Tree: "c7c8c2847caee9c477f23fc76242298f1ac2eff5", patch1SHA256: "3a1b73769d14d5d17af7d1a867db530870fbbcaf5f2b75e9a8328442d0c8e034",
	patchNames: []string{
		"unmediated-network.patch", "workspace-read-only.patch",
		qualifiedPatch3Name, qualifiedPatch4Name, qualifiedPatch5Name,
		qualifiedPatch6Name, qualifiedPatch7Name, qualifiedPatch8Name,
		qualifiedPatch9Name,
	},
	bootstrapSchema: 2, protocolFixtureVersion: "workspace-read-only-v1",
}}

func (r sourceRelease) matches(p ReleasePins) bool {
	return p.Version == r.version && p.UpstreamCommit == r.upstreamCommit && p.UpstreamTree == r.upstreamTree &&
		p.BootstrapSchema == r.bootstrapSchema && p.ProtocolFixtureVersion == r.protocolFixtureVersion
}

// patchStack accepts the first two patches and a contiguous prefix of the
// later patches. Each patch follows the previous tree and the last ends at the
// fork tree.
func (r sourceRelease) patchStack(p ReleasePins) bool {
	if len(p.Patches) < 2 || len(p.Patches) > len(r.patchNames) {
		return false
	}
	if p.Patches[0].SHA256 != r.patch1SHA256 || p.Patches[0].ParentTree != p.UpstreamTree || p.Patches[0].Tree != r.patch1Tree {
		return false
	}
	for i, patch := range p.Patches {
		if patch.Name != r.patchNames[i] || !lowerHex(patch.SHA256, 32) || !lowerHex(patch.Tree, 20) {
			return false
		}
		if i > 0 && patch.ParentTree != p.Patches[i-1].Tree {
			return false
		}
	}
	return p.Patches[len(p.Patches)-1].Tree == p.ForkTree
}

// qualifiedPatchStack is the current source's patch rule.
func qualifiedPatchStack(p ReleasePins) bool { return currentRelease.patchStack(p) }

// Manifest returns the exact reviewed bytes for a read-only runtime projection.
// Callers cannot mutate the retained qualification or reconstruct another JSON
// encoding that merely describes the same pins.
func (r QualifiedRelease) Manifest() []byte { return bytes.Clone(r.manifest) }

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateQualification(document releaseDocument, sources []sourceRelease) error {
	p, q := document.Pins, document.Qualification
	subject, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if !lowerHex(q.SubjectSHA256, 32) || sha256Hex(subject) != q.SubjectSHA256 {
		return errors.New("OpenShell pins changed without a matching reviewed qualification record")
	}
	if err := layout.ValidateName("qualification", q.Revision); err != nil {
		return err
	}
	if !lowerHex(q.SourceProofSHA256, 32) || len(q.BuildProofSHA256) != 2 || len(p.Platforms) != 2 {
		return errors.New("OpenShell source/platform qualification is incomplete")
	}
	var source *sourceRelease
	for i := range sources {
		if sources[i].matches(p) {
			source = &sources[i]
			break
		}
	}
	if source == nil || !lowerHex(p.ForkCommit, 20) || !lowerHex(p.ForkTree, 20) ||
		!lowerHex(p.ProtocolFixtureSHA256, 32) || !lowerHex(p.RegoSHA256, 32) {
		return errors.New("unqualified OpenShell source, bootstrap or protocol fixture version")
	}
	if !source.patchStack(p) {
		return errors.New("unqualified or reordered OpenShell patch stack")
	}
	if err := p.Helper.validate(); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		platform, ok := p.Platforms[arch]
		if !ok || !lowerHex(q.BuildProofSHA256[arch], 32) || platform.BoundaryLoader == "" || platform.SupervisorLoader == "" {
			return errors.New("OpenShell architecture or loader qualification is incomplete")
		}
		if platform.Boundary.Member != "openshell-sandbox" || platform.Supervisor.Member != "openshell-supervisor" || platform.Boundary.URL == platform.Supervisor.URL {
			return errors.New("OpenShell supervisor/boundary pair is missing or mixed")
		}
		for _, artifact := range []Artifact{platform.Boundary, platform.Supervisor} {
			if err := artifact.validateHashes(); err != nil {
				return err
			}
			if _, err := embeddedArchiveName(artifact.URL); err != nil {
				return errors.New("OpenShell artifact must name an embedded release archive")
			}
		}
	}
	return nil
}

// Platform returns a copy of the validated, immutable pin pair. The selected
// architecture never falls back to another platform or to a legacy binary.
func (r QualifiedRelease) Platform(arch string) (PlatformArtifacts, error) {
	if r.manifestSHA256 == "" {
		return PlatformArtifacts{}, errors.New("OpenShell release is not qualified")
	}
	p, ok := r.document.Pins.Platforms[arch]
	if !ok {
		return PlatformArtifacts{}, fmt.Errorf("OpenShell architecture %q is not qualified", arch)
	}
	return p, nil
}
