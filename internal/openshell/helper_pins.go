package openshell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"

	helperinputs "github.com/leogr/yoloctl"
)

// HelperPins is independent of the CLI bundle and OpenShell's Rust pair. The
// helper never embeds this document: native activation binds its external RO
// copy to the controller's compiled qualification and exact selected binary.
type HelperPins struct {
	Protocol          string                `json:"protocol"`
	Inputs            helperinputs.Identity `json:"inputs"`
	GoVersion         string                `json:"go_version"`
	SourceProofSHA256 string                `json:"source_proof_sha256"`
	BuildProofSHA256  map[string]string     `json:"build_proof_sha256"`
	Platforms         map[string]Artifact   `json:"platforms"`
}

func (p HelperPins) validate() error {
	if p.Protocol != HelperProtocol || p.Inputs.Schema != helperinputs.Schema || !lowerHex(p.Inputs.SourceSHA256, 32) ||
		!regexp.MustCompile(`^go[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(p.GoVersion) || !lowerHex(p.SourceProofSHA256, 32) ||
		len(p.Platforms) != 2 || len(p.BuildProofSHA256) != 2 {
		return errors.New("separate static OpenShell helper qualification is incomplete")
	}
	var modules []string
	for _, module := range p.Inputs.Modules {
		if module.Path == "" || !strings.HasPrefix(module.Version, "v") || !strings.HasPrefix(module.Sum, "h1:") {
			return errors.New("incomplete compiled helper module identity")
		}
		modules = append(modules, module.Path)
	}
	if !slices.IsSorted(modules) || len(modules) == 0 || len(slices.Compact(slices.Clone(modules))) != len(modules) {
		return errors.New("helper module identity is not canonical")
	}
	expectedModules, err := helperinputs.ModulePaths()
	if err != nil {
		return err
	}
	if !slices.Equal(modules, expectedModules) {
		return errors.New("helper module inventory differs from its compiled recipe")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		artifact, ok := p.Platforms[arch]
		if !ok || artifact.Member != HelperMember || !lowerHex(p.BuildProofSHA256[arch], 32) {
			return errors.New("helper architecture/build qualification is incomplete")
		}
		if err := artifact.validateHashes(); err != nil {
			return err
		}
		if _, err := embeddedArchiveName(artifact.URL); err != nil {
			return errors.New("helper artifact must name an embedded release archive")
		}
	}
	if p.Platforms["amd64"].URL == p.Platforms["arm64"].URL {
		return errors.New("helper architectures must have separate immutable archives")
	}
	return nil
}

func (r QualifiedRelease) Helper(arch string) (Artifact, error) {
	if r.Identity() == "" {
		return Artifact{}, errors.New("separate OpenShell helper is not qualified")
	}
	artifact, ok := r.document.Pins.Helper.Platforms[arch]
	if !ok {
		return Artifact{}, errors.New("separate OpenShell helper architecture is not qualified")
	}
	return artifact, nil
}

// ValidateHelperSource is required during non-destructive preparation, before
// replacement of an old container. It never asks a valid dynamic development
// CLI to become static and never compiles a helper on the host or in the VM.
func (r QualifiedRelease) ValidateHelperSource() error {
	if r.Identity() == "" {
		return errors.New("separate OpenShell helper qualification is unavailable")
	}
	current, err := helperinputs.Current()
	if err != nil {
		return err
	}
	return r.validateHelperInputs(current)
}

// checkExistingBoxHelper replaces ValidateHelperSource for a box that already
// exists (snapshot, its next generation, generation artifacts). That box keeps
// the helper it was created with, installed and pinned by hash; only new
// creates/installs need helper bytes matching this binary's sources. A box
// made by an earlier build gets one notice, never a refusal (old-path
// parity: existing boxes keep their recorded runtime until recreated).
func (r QualifiedRelease) checkExistingBoxHelper(box string) error {
	if r.Identity() == "" {
		return errors.New("separate OpenShell helper qualification is unavailable")
	}
	current, err := helperinputs.Current()
	if err != nil {
		return err
	}
	if r.validateHelperInputs(current) != nil {
		existingHelperNotice(box)
	}
	return nil
}

// checkHelperSourceFor is the strict ValidateHelperSource for a new box
// (empty name) and checkExistingBoxHelper for an existing one.
func (r QualifiedRelease) checkHelperSourceFor(existingBox string) error {
	if existingBox == "" {
		return r.ValidateHelperSource()
	}
	return r.checkExistingBoxHelper(existingBox)
}

// existingHelperNotice reports, once per box, a box running a helper from an
// earlier build; tests replace it.
var existingHelperNotice = func() func(string) {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(box string) {
		mu.Lock()
		defer mu.Unlock()
		if seen[box] || len(seen) > 1024 {
			return
		}
		seen[box] = true
		_, _ = fmt.Fprintf(os.Stderr, "notice: box %s runs the OpenShell helper of an earlier yoloctl build; recreate it to use this build's helper\n", box)
	}
}()

func (r QualifiedRelease) validateHelperInputs(current helperinputs.Identity) error {
	expected, err := json.Marshal(r.document.Pins.Helper.Inputs)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if string(expected) != string(actual) {
		return errors.New("separate OpenShell helper does not match this compiled source/dependency identity; qualify and publish matching helper bytes before replacing the box")
	}
	return nil
}
