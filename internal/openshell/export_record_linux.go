package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
)

// DecodeExportRecord checks only retained record identity for a read-only file
// export. It grants no launch, activation, storage or process authority, and
// deliberately does not require today's helper protocol, patch set or launcher.
// Its caller independently verifies native ownership, current/generation bytes,
// the selected full container ID and actual binds.
func DecodeExportRecord(raw []byte, launch HelperLaunch) (RuntimeRecord, error) {
	var r RuntimeRecord
	if len(raw) == 0 || len(raw) > 1<<20 {
		return r, errors.New("missing or unbounded retained export record")
	}
	if err := checkJSONKeys(raw); err != nil {
		return r, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, err
	}
	// Schema is the retained record format, not executable compatibility.
	if r.Launch.Schema != BootstrapSchema || r.Launch.Binding.Validate() != nil || !lowerHex(r.Launch.HelperSHA256, 32) || !path.IsAbs(r.Launch.Workdir) || path.Clean(r.Launch.Workdir) != r.Launch.Workdir {
		return r, errors.New("invalid retained export binding")
	}
	expected := r.Launch.HelperLaunch()
	expected.Protocol = launch.Protocol
	if expected != launch {
		return r, errors.New("retained export record differs from inspected helper binding")
	}
	if len(r.Generation) != 49 || r.Generation[0] != 'g' || !lowerHex(r.Generation[1:], 24) || !canonicalUUID(r.SessionID) {
		return r, errors.New("invalid retained export generation")
	}
	digest, err := r.Launch.Binding.digest()
	if err != nil {
		return r, err
	}
	generation, err := generationForSession(digest, r.SessionID)
	if err != nil || generation != r.Generation {
		return r, errors.New("retained export generation does not match its binding")
	}
	return r, nil
}
