package openshell

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type namespaceIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type activationMessage struct {
	Kind         string            `json:"kind"`
	Schema       int               `json:"schema"`
	RecordSHA256 string            `json:"record_sha256"`
	ClientNonce  string            `json:"client_nonce"`
	ServerNonce  string            `json:"server_nonce"`
	Namespace    namespaceIdentity `json:"namespace"`
	ProofSHA256  string            `json:"proof_sha256"`
}
type activationEnvelope struct {
	Message activationMessage `json:"message"`
	MAC     string            `json:"mac"`
}

type activationKey [32]byte

func (activationKey) String() string       { return "OpenShell activation key [redacted]" }
func (key activationKey) GoString() string { return key.String() }

func activationNonce() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (key activationKey) authenticate(message activationMessage) activationEnvelope {
	raw, _ := json.Marshal(message)
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("yoloctl-openshell-native-activation-v2\x00"))
	_, _ = mac.Write(raw)
	return activationEnvelope{message, hex.EncodeToString(mac.Sum(nil))}
}

func (key activationKey) verify(envelope activationEnvelope, expected activationMessage) error {
	if envelope.Message != expected || !lowerHex(envelope.MAC, 32) {
		return errors.New("activation phase, generation or peer challenge mismatch")
	}
	want := key.authenticate(expected)
	if !hmac.Equal([]byte(envelope.MAC), []byte(want.MAC)) {
		return errors.New("activation peer authentication failed")
	}
	return nil
}

func (r RuntimeRecord) activationHello(nonce string) (activationMessage, error) {
	if !lowerHex(nonce, 32) || len(r.Generation) != 49 || !canonicalUUID(r.SessionID) {
		return activationMessage{}, errors.New("invalid native activation identity")
	}
	if err := r.Launch.Validate(); err != nil {
		return activationMessage{}, err
	}
	digest, err := r.Launch.Binding.digest()
	if err != nil {
		return activationMessage{}, err
	}
	generation, err := generationForSession(digest, r.SessionID)
	if err != nil || generation != r.Generation {
		return activationMessage{}, errors.New("activation generation does not match native runtime binding")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return activationMessage{}, err
	}
	return activationMessage{Kind: "hello", Schema: BootstrapSchema, RecordSHA256: sha256Hex(raw), ClientNonce: nonce}, nil
}
