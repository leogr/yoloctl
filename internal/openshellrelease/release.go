// Package openshellrelease selects the controller's compiled native OpenShell
// release. It stays outside the separately built helper's dependency closure:
// that helper must not embed a manifest containing its own binary hash.
package openshellrelease

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/nativeartifacts"
	"github.com/leogr/yoloctl/internal/openshell"
)

// ErrUnavailable means this build has no complete production native selection.
// A native request must return it before effects, without choosing a fixture,
// changing its expected hash or silently using the legacy runtime.
var ErrUnavailable = errors.New("this yoloctl was built without its native OpenShell selection; run make build")

// Load accepts no caller-selected trust material and performs no I/O or runtime
// mutation. New-create/replacement callers use this before holder/box effects;
// historical-runtime readers keep their separately recorded qualification.
func Load() (openshell.QualifiedRelease, []byte, error) {
	// make build generates the selection next to the embedded archives; the
	// committed default (assets) is empty, so a plain go build refuses.
	manifest, reviewedHash, rego, err := nativeartifacts.Selection()
	if err != nil {
		return openshell.QualifiedRelease{}, nil, err
	}
	if len(manifest) == 0 && reviewedHash == "" && len(rego) == 0 {
		manifest, reviewedHash, rego = assets.NativeOpenShellQualification()
	}
	return loadSelection(manifest, reviewedHash, rego, openshell.QualifiedRelease.ValidateHelperSource)
}

// The injected source checker is private and permits ordinary unit fixtures to
// test ordering/refusal without qualifying their fictional helper. Production
// always supplies the real compiled-source/dependency validator above.
func loadSelection(manifest []byte, reviewedHash string, rego []byte, sourceCheck func(openshell.QualifiedRelease) error) (openshell.QualifiedRelease, []byte, error) {
	if len(manifest) == 0 || reviewedHash == "" || len(rego) == 0 {
		return openshell.QualifiedRelease{}, nil, ErrUnavailable
	}
	if len(rego) > 1<<20 || sourceCheck == nil {
		return openshell.QualifiedRelease{}, nil, errors.New("incomplete or oversized native OpenShell release selection")
	}
	release, err := openshell.LoadQualifiedRelease(manifest, reviewedHash)
	if err != nil {
		return openshell.QualifiedRelease{}, nil, err
	}
	// Strict qualification has already validated every field, duplicate key,
	// patch/platform/helper/schema and subject binding. Read its retained copy
	// to compare the paired Rego without exposing opaque runtime authority.
	var document struct {
		Pins openshell.ReleasePins `json:"pins"`
	}
	if err := json.Unmarshal(release.Manifest(), &document); err != nil {
		return openshell.QualifiedRelease{}, nil, err
	}
	pairedRego := bytes.Clone(rego)
	digest := sha256.Sum256(pairedRego)
	if hex.EncodeToString(digest[:]) != document.Pins.RegoSHA256 {
		return openshell.QualifiedRelease{}, nil, errors.New("compiled native OpenShell Rego differs from its reviewed qualification")
	}
	if err := sourceCheck(release); err != nil {
		return openshell.QualifiedRelease{}, nil, fmt.Errorf("compiled native OpenShell helper qualification: %w", err)
	}
	return release, pairedRego, nil
}
