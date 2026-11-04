package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
)

// namespaceRetiredBindingFromMetadata validates only the stored metadata join.
// record must already have its independent recorded-release qualification;
// recordedCID names only stored scope, never an actual/current engine proof.
// No FD, namespacePinFiles/lock, current mount identity, callback or effect token
// is accepted or created. Terminal state alone grants no retirement/Stop/rm ACK.
// The caller retains and checks the ORIGINAL source files and their mappings.
func namespaceRetiredBindingFromMetadata(originRaw, stateRaw []byte, record RuntimeRecord, recordedCID string) (namespacePinRetired, error) {
	origin, err := decodeNamespacePinOrigin(originRaw, record, recordedCID)
	if err != nil {
		return namespacePinRetired{}, err
	}
	if err := namespaceRetiredCanonicalMetadata(originRaw, origin); err != nil {
		return namespacePinRetired{}, err
	}
	state, err := decodeNamespacePinRecord(stateRaw, record, recordedCID, origin.Record.OriginSHA256)
	if err != nil {
		return namespacePinRetired{}, err
	}
	if err := namespaceRetiredCanonicalMetadata(stateRaw, state); err != nil {
		return namespacePinRetired{}, err
	}
	schema := 1 // Existing full-policy/pin/directory marker, unchanged.
	if state.Phase != pinDirectoryUnmounted {
		if !knownUnboundTerminalObservation(state) {
			return namespacePinRetired{}, errors.New("namespace retirement lacks its terminal observation")
		}
		schema = 2 // Existing known-unbound marker, unchanged.
	}
	// Hash exact original producer bytes, never a reconstructed JSON encoding.
	return namespacePinRetired{schema, sha256Hex(originRaw), sha256Hex(stateRaw), recordIdentity(record), state.Scope.KernelBootID}, nil
}

// decodeNamespaceRetiredMetadata accepts only the exact canonical marker for
// the independently computed expected binding. It validates the expected shape
// too, but neither this type nor a matching result grants effect authority.
// Return a copied value only on complete success, with a zero value on ANY error.
func decodeNamespaceRetiredMetadata(raw []byte, expected namespacePinRetired) (namespacePinRetired, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return namespacePinRetired{}, errors.New("invalid retired namespace marker bound")
	}
	if (expected.Schema != 1 && expected.Schema != 2) || !lowerHex(expected.OriginSHA256, 32) || !lowerHex(expected.StateSHA256, 32) || !lowerHex(expected.RecordSHA256, 32) || !canonicalUUID(expected.KernelBootID) {
		return namespacePinRetired{}, errors.New("invalid expected namespace retirement binding")
	}
	if err := checkJSONKeys(raw); err != nil {
		return namespacePinRetired{}, err
	}
	var got namespacePinRetired
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		return namespacePinRetired{}, err
	}
	if got != expected {
		return namespacePinRetired{}, errors.New("namespace retirement marker differs from its protected terminal binding")
	}
	if err := namespaceRetiredCanonicalMetadata(raw, expected); err != nil {
		return namespacePinRetired{}, err
	}
	return got, nil
}

func namespaceRetiredCanonicalMetadata(raw []byte, value any) error {
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.Join(errors.New("namespace retirement metadata differs from canonical producer bytes"), err)
	}
	return nil
}
