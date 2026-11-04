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
	qualifiedVersion        = "0.1.6"
	qualifiedUpstreamCommit = "ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47"
	qualifiedUpstreamTree   = "c9c1f358cd6f68445c88b65a5e4983a5213e6bab"
	qualifiedPatch1Tree     = "c7c8c2847caee9c477f23fc76242298f1ac2eff5"
	qualifiedPatch1SHA256   = "3a1b73769d14d5d17af7d1a867db530870fbbcaf5f2b75e9a8328442d0c8e034"
	// Optional third patch restoring the workload calls v0.0.72 allowed, and
	// optional fourth restoring process-group signals.
	qualifiedPatch3Name    = "v0072-syscall-parity.patch"
	qualifiedPatch4Name    = "v0072-process-group-signals.patch"
	protocolFixtureVersion = "workspace-read-only-v1"
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
	if err := validateQualification(document); err != nil {
		return QualifiedRelease{}, err
	}
	return QualifiedRelease{document: document, manifestSHA256: reviewedSHA256, manifest: bytes.Clone(raw)}, nil
}

// Manifest returns the exact reviewed bytes for a read-only runtime projection.
// Callers cannot mutate the retained qualification or reconstruct another JSON
// encoding that merely describes the same pins.
// qualifiedPatchStack accepts the fixed first patch, the second, and the
// optional third and fourth (v0.0.72 parity), each parented on the previous tree
// and the last ending at the fork tree. Two-patch releases stay valid, so
// boxes created from them keep loading their recorded qualification.
func qualifiedPatchStack(p ReleasePins) bool {
	names := []string{"unmediated-network.patch", "workspace-read-only.patch", qualifiedPatch3Name, qualifiedPatch4Name}
	if len(p.Patches) < 2 || len(p.Patches) > len(names) {
		return false
	}
	if p.Patches[0].SHA256 != qualifiedPatch1SHA256 || p.Patches[0].ParentTree != p.UpstreamTree || p.Patches[0].Tree != qualifiedPatch1Tree {
		return false
	}
	for i, patch := range p.Patches {
		if patch.Name != names[i] || !lowerHex(patch.SHA256, 32) || !lowerHex(patch.Tree, 20) {
			return false
		}
		if i > 0 && patch.ParentTree != p.Patches[i-1].Tree {
			return false
		}
	}
	return p.Patches[len(p.Patches)-1].Tree == p.ForkTree
}

func (r QualifiedRelease) Manifest() []byte { return bytes.Clone(r.manifest) }

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateQualification(document releaseDocument) error {
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
	if p.Version != qualifiedVersion || p.UpstreamCommit != qualifiedUpstreamCommit || p.UpstreamTree != qualifiedUpstreamTree ||
		!lowerHex(p.ForkCommit, 20) || !lowerHex(p.ForkTree, 20) || p.BootstrapSchema != BootstrapSchema ||
		p.ProtocolFixtureVersion != protocolFixtureVersion || !lowerHex(p.ProtocolFixtureSHA256, 32) || !lowerHex(p.RegoSHA256, 32) {
		return errors.New("unqualified OpenShell source, bootstrap or protocol fixture version")
	}
	if !qualifiedPatchStack(p) {
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
