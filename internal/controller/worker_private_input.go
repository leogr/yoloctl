package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// CanonicalWorkerInput is PRIVATE transport data, never permission or a receipt.
// It hashes json.Marshal of the ACTUAL WorkerInput type, so separately reviewed
// future typed phase/bootstrap fields participate without guessing an allowlist.
// No input digest is embedded into WorkerInput. Digest/raw bytes must not enter
// public logs/diagnostics; request payload may contain secrets.
func CanonicalWorkerInput(work WorkerInput) ([]byte, string, error) {
	raw, err := json.Marshal(work)
	if err != nil || len(raw) == 0 || len(raw) >= MaxFrame {
		return nil, "", errors.New("private worker input is invalid or exceeds its bound")
	}
	// Reserve the one newline emitted by the existing json.Encoder producer.
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}
func WorkerInputDigest(work WorkerInput) (string, error) {
	_, digest, err := CanonicalWorkerInput(work)
	return digest, err
}

// DecodeWorkerInput accepts exactly the actual producer's canonical typed JSON,
// with at most its ONE terminal newline. Copy-on-success prevents duplicate,
// folded, unknown/null-erased envelope fields from becoming routing metadata.
// Opaque Request.Payload is NOT reinterpreted as an operation entity here.
// Its exact canonical RawMessage bytes participate; entity schema stays with
// that operation, avoiding a new blanket case-fold ban on user payload/maps.
func DecodeWorkerInput(raw []byte) (WorkerInput, string, error) {
	if len(raw) == 0 || len(raw) > MaxFrame {
		return WorkerInput{}, "", errors.New("private worker input is missing or oversized")
	}
	wire := raw
	if wire[len(wire)-1] == '\n' {
		wire = wire[:len(wire)-1]
	}
	var work WorkerInput
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&work); err != nil {
		return WorkerInput{}, "", errors.New("invalid private worker input")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return WorkerInput{}, "", errors.New("trailing private worker input")
	}
	canonical, digest, err := CanonicalWorkerInput(work)
	if err != nil || !bytes.Equal(wire, canonical) {
		return WorkerInput{}, "", errors.New("private worker input is not exact closed canonical typed encoding")
	}
	return work, digest, nil
}
