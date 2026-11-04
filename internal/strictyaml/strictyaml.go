// Package strictyaml decodes YAML manifests with the repository's fail-closed
// convention: unknown fields are errors (yaml.v3 KnownFields, as in
// agentbox.Open and the capability loader), and trailing documents after the
// first are rejected — preserving yaml.Unmarshal's single-document contract
// for loaders migrating off it. It exists for schemas where a silently
// dropped, misspelled field would fail OPEN: an account manifest whose
// `secrets:` key is mistyped would load as an empty keychain, and an agent
// definition whose `secret_injection:` or `auth:` block is mistyped would
// route credentials differently than its author declared.
package strictyaml

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Decode strictly parses exactly one YAML document from raw into out.
//
//   - A field not present on out's type is an error, never silently dropped.
//   - A second document in the stream (`---`) is an error: the manifest
//     loaders read files that must BE the entity, with no room for stowaway
//     documents a lenient reader would ignore.
//   - Empty (or comment-only) input decodes to the zero value, like
//     yaml.Unmarshal — the caller's schema/name validation then rejects it
//     with its own, better-contextualized error.
//
// Errors are returned bare; callers wrap them with their path context.
func Decode(raw []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty or comment-only input: zero value, like yaml.Unmarshal
		}
		return err
	}
	var trailing yaml.Node
	switch err := dec.Decode(&trailing); {
	case err == nil:
		return errors.New("unexpected trailing YAML document (expected a single document in the stream)")
	case !errors.Is(err, io.EOF):
		return fmt.Errorf("trailing YAML content: %w", err)
	}
	return nil
}
