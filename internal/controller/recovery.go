package controller

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/distribution/reference"
	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

// ProofVersion is the newest receipt evidence schema this build accepts. A newer
// or malformed version fails closed; version 0 is the legacy unscoped record.
const ProofVersion = 2

// Document-only and unnamed-domain receipts retain their original contract.
const documentProofVersion = 1

// Resource kinds form a closed set. Box and account names are reserved as
// names, so a held name cannot be evaded by destroying and recreating it.
// Chat and ports are shared domains. Images additionally have named resources
// in proof version 2; an unnamed image write covers the entire image store.
const (
	ResourceBox     = "box"
	ResourceAccount = "account"
	ResourceChat    = "chat"
	ResourceImages  = "images"
	ResourcePorts   = "ports"
)

// Resource is one typed member of an operation's footprint.
type Resource struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

func (r Resource) String() string {
	if r.Name == "" {
		return r.Kind
	}
	return r.Kind + " " + r.Name
}

func (r Resource) validate() error {
	switch r.Kind {
	case ResourceBox:
		return layout.ValidateName("agentbox", r.Name)
	case ResourceAccount:
		return account.ValidateName(r.Name)
	case ResourceImages:
		if r.Name == "" {
			return nil
		}
		if len(r.Name) > 512 {
			return errors.New("image resource name exceeds its limit")
		}
		if _, err := reference.ParseNormalizedNamed(r.Name); err != nil {
			return errors.New("invalid image resource reference")
		}
		return nil
	case ResourceChat, ResourcePorts:
		if r.Name != "" {
			return errors.New("shared resource domains have no name")
		}
		return nil
	default:
		return errors.New("unknown operation resource kind")
	}
}

// Scope is the durable footprint recorded before an effect is permitted.
// Writes are held while the outcome is unresolved; Reads are dependencies a
// new effect checks against existing holds but never holds itself. A missing
// scope, or Global, holds the whole space.
type Scope struct {
	Global bool       `json:"global,omitempty"`
	Writes []Resource `json:"writes,omitempty"`
	Reads  []Resource `json:"reads,omitempty"`
}

const maxScopeResources = 256

func (s *Scope) validate() error {
	if s == nil {
		return nil
	}
	if len(s.Writes)+len(s.Reads) > maxScopeResources {
		return errors.New("operation scope exceeds its limit")
	}
	if !s.Global && len(s.Writes) == 0 {
		return errors.New("operation scope declares no written resource")
	}
	for _, list := range [][]Resource{s.Writes, s.Reads} {
		for _, r := range list {
			if err := r.validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// proofVersion returns the minimum schema needed to interpret this footprint.
func (s *Scope) proofVersion() int {
	if s != nil {
		for _, list := range [][]Resource{s.Writes, s.Reads} {
			for _, r := range list {
				if r.Kind == ResourceImages && r.Name != "" {
					return 2
				}
			}
		}
	}
	return documentProofVersion
}

// Verifier kinds are a reviewed allowlist. Each names one read-only proof of
// a complete declared transaction; nothing else can mark an outcome applied.
const (
	VerifierAccountDocument = "account-document"
	VerifierChatRevoke      = "chat-revoke"
)

// EffectIntent is what a trusted worker declares at its effect gate. It comes
// only over the private checkpoint descriptor, never from request input.
type EffectIntent struct {
	Scope    *Scope `json:"scope,omitempty"`
	Verifier string `json:"verifier,omitempty"`
}

func (i EffectIntent) validate() error {
	switch i.Verifier {
	case "", VerifierAccountDocument, VerifierChatRevoke:
	default:
		return errors.New("unknown operation verifier")
	}
	if i.Verifier != "" && (i.Scope == nil || i.Scope.Global) {
		return errors.New("a verified operation needs a bounded scope")
	}
	return i.Scope.validate()
}

// Document kinds a verifier can read back.
const (
	DocumentAccount = "account"
	DocumentChat    = "chat"
)

// DocumentIntent is the exact postimage a worker is about to publish
// atomically. Data is fingerprinted by the controller and never persisted.
type DocumentIntent struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Present bool   `json:"present"`
	Data    []byte `json:"data,omitempty"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
	Mode    uint32 `json:"mode"`
}

func (d DocumentIntent) validate(verifier string) error {
	switch {
	case verifier == VerifierAccountDocument && d.Kind == DocumentAccount:
		if err := account.ValidateName(d.Name); err != nil {
			return err
		}
	case verifier == VerifierChatRevoke && d.Kind == DocumentChat:
		if d.Name != "" {
			return errors.New("the managed chat document has no name")
		}
	default:
		return errors.New("document does not match the operation verifier")
	}
	if len(d.Data) > MaxDocument || !d.Present && (len(d.Data) != 0 || d.Mode != 0) || d.Present && d.Mode&^0o777 != 0 || d.UID < 0 || d.GID < 0 {
		return errors.New("invalid document intent")
	}
	return nil
}

// Proof is the persisted, nonsecret form of a DocumentIntent: a keyed
// fingerprint bound to this disk, receipt and verifier version.
type Proof struct {
	Kind        string `json:"kind"`
	Name        string `json:"name,omitempty"`
	Present     bool   `json:"present"`
	Fingerprint string `json:"fingerprint,omitempty"`
	KeyID       string `json:"key_id"`
}

const maxProofs = 8

func (p Proof) validate(verifier string) error {
	if err := (DocumentIntent{Kind: p.Kind, Name: p.Name, Present: true}).validate(verifier); err != nil {
		return err
	}
	if err := layout.ValidateIdentity(p.KeyID); err != nil {
		return errors.New("invalid proof key identity")
	}
	if p.Present {
		if raw, err := hex.DecodeString(p.Fingerprint); err != nil || len(raw) != sha256.Size {
			return errors.New("invalid proof fingerprint")
		}
	} else if p.Fingerprint != "" {
		return errors.New("an absent document has no fingerprint")
	}
	return nil
}

// Completion is the durable effects-complete checkpoint: every effect and
// mutating cleanup ended, with this known result. It never claims success.
type Completion struct {
	Status int       `json:"status"`
	At     time.Time `json:"at"`
}

// Recovery dispositions. ConsistentResultUnknown is reserved vocabulary with
// no approved producer; a receipt claiming it is rejected.
const (
	DispositionNotApplied              = "not-applied"
	DispositionApplied                 = "applied"
	DispositionRecordedCompletion      = "recorded-completion"
	DispositionConsistentResultUnknown = "consistent-result-unknown"

	ProofGateWaiting   = "effect-gate-waiting"
	ProofEffectsDone   = "effects-complete"
	ProofDocumentImage = "document-postimage"
)

// Recovery explains how a receipt became terminal without its normal
// finalization. It is evidence, not a replay or a success report.
type Recovery struct {
	Disposition string    `json:"disposition"`
	Proof       string    `json:"proof"`
	Version     int       `json:"version"`
	At          time.Time `json:"at"`
}

func (r *Receipt) validateEvidence() error {
	if r.Image != nil {
		if err := r.Image.validate(*r); err != nil {
			return err
		}
	}
	if r.ProofVersion < 0 || r.ProofVersion > ProofVersion {
		return errors.New("unsupported operation receipt proof version")
	}
	if r.ProofVersion == 0 {
		// No older writer emits the recovered state; it is never proof.
		if r.State == "recovered" || r.DiskID != "" || r.Scope != nil || r.Verifier != "" || len(r.Proofs) != 0 || r.Completion != nil || r.Recovery != nil {
			return errors.New("legacy operation receipt carries recovery evidence")
		}
		return nil
	}
	if err := layout.ValidateIdentity(r.DiskID); err != nil {
		return errors.New("invalid operation receipt disk identity")
	}
	if r.ProofVersion < r.Scope.proofVersion() {
		return errors.New("named image resource requires proof version 2")
	}
	if err := (EffectIntent{Scope: r.Scope, Verifier: r.Verifier}).validate(); err != nil {
		return err
	}
	if len(r.Proofs) > maxProofs || len(r.Proofs) != 0 && r.EffectPhase != EffectStarted {
		return errors.New("invalid operation receipt proofs")
	}
	for _, p := range r.Proofs {
		if err := p.validate(r.Verifier); err != nil {
			return err
		}
	}
	if r.Completion != nil && (r.Completion.Status < 0 || r.Completion.Status > 255 || r.EffectPhase == EffectWaiting || r.Completion.At.IsZero()) {
		return errors.New("invalid operation completion checkpoint")
	}
	if (r.State == "recovered") != (r.Recovery != nil) {
		return errors.New("recovered operation receipt lacks its disposition")
	}
	if r.Recovery == nil {
		return nil
	}
	if r.Recovery.Version < 1 || r.Recovery.Version > ProofVersion || r.Recovery.At.IsZero() {
		return errors.New("unsupported operation recovery proof version")
	}
	switch r.Recovery.Disposition + "/" + r.Recovery.Proof {
	case DispositionNotApplied + "/" + ProofGateWaiting:
		if r.EffectPhase != EffectWaiting {
			return errors.New("not-applied recovery without a waiting effect gate")
		}
	case DispositionRecordedCompletion + "/" + ProofEffectsDone:
		if r.Completion == nil || r.Status != r.Completion.Status {
			return errors.New("recorded completion without its checkpoint")
		}
	case DispositionApplied + "/" + ProofDocumentImage:
		if r.Verifier == "" || len(r.Proofs) == 0 {
			return errors.New("applied recovery without document proofs")
		}
	default:
		return errors.New("unsupported operation recovery disposition")
	}
	return nil
}

// recoveryKeySize is the HMAC-SHA256 key; the file adds one version byte.
const recoveryKeySize = 32
const recoveryKeyVersion = 1

// RecoveryKey fingerprints document postimages. It stays in the controller
// process: workers send bytes over private descriptors and the key, its file
// and comparison results are never exposed to clients or operators.
type RecoveryKey struct {
	ID  string
	key []byte
}

// LoadRecoveryKey opens the root-private key on the verified native disk,
// creating one only when none exists. A malformed or foreign-version file is
// an error, never silently replaced: fingerprints made with it stay unknown.
func LoadRecoveryKey(meta string) (*RecoveryKey, error) {
	dir, err := PrivateDir(meta, "recovery")
	if err != nil {
		return nil, err
	}
	raw, info, err := safepath.ReadFileInfo(dir, "key", 4096)
	if errors.Is(err, fs.ErrNotExist) {
		fresh := make([]byte, 1+recoveryKeySize)
		fresh[0] = recoveryKeyVersion
		if _, err := rand.Read(fresh[1:]); err != nil {
			return nil, err
		}
		if err := safepath.WriteFile(dir, "key", fresh, 0o600); err != nil {
			return nil, err
		}
		raw, info, err = safepath.ReadFileInfo(dir, "key", 4096)
	}
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o600 || st.Nlink != 1 {
		return nil, errors.New("controller recovery key is not private to its owner")
	}
	if len(raw) != 1+recoveryKeySize || raw[0] != recoveryKeyVersion {
		return nil, errors.New("controller recovery key has an unsupported format")
	}
	return newRecoveryKey(raw[1:]), nil
}

func newRecoveryKey(key []byte) *RecoveryKey {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("yoloctl recovery key identity"))
	return &RecoveryKey{ID: hex.EncodeToString(mac.Sum(nil)[:16]), key: append([]byte(nil), key...)}
}

// fingerprint is domain-separated by disk, receipt, verifier version and the
// document's logical identity and required metadata.
func (k *RecoveryKey) fingerprint(version int, disk, receipt, verifier string, d DocumentIntent) string {
	mac := hmac.New(sha256.New, k.key)
	field := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		_, _ = mac.Write(n[:])
		_, _ = mac.Write(b)
	}
	field([]byte(fmt.Sprintf("yoloctl document proof v%d", version)))
	for _, s := range []string{disk, receipt, verifier, d.Kind, d.Name, fmt.Sprintf("%d:%d:%o", d.UID, d.GID, d.Mode)} {
		field([]byte(s))
	}
	field(d.Data)
	return hex.EncodeToString(mac.Sum(nil))
}

// Proof converts an intent into its persisted form.
func (k *RecoveryKey) proof(disk, receipt, verifier string, d DocumentIntent) Proof {
	return k.proofVersion(documentProofVersion, disk, receipt, verifier, d)
}

func (k *RecoveryKey) proofVersion(version int, disk, receipt, verifier string, d DocumentIntent) Proof {
	p := Proof{Kind: d.Kind, Name: d.Name, Present: d.Present, KeyID: k.ID}
	if d.Present {
		p.Fingerprint = k.fingerprint(version, disk, receipt, verifier, d)
	}
	return p
}

// matches compares an observed document with its recorded proof in constant
// time. A different key identity is unknown, never a match.
func (k *RecoveryKey) matches(disk, receipt, verifier string, p Proof, observed DocumentIntent) bool {
	return k.matchesVersion(documentProofVersion, disk, receipt, verifier, p, observed)
}

func (k *RecoveryKey) matchesVersion(version int, disk, receipt, verifier string, p Proof, observed DocumentIntent) bool {
	if version < 1 || version > ProofVersion || k == nil || p.KeyID != k.ID || p.Kind != observed.Kind || p.Name != observed.Name || p.Present != observed.Present {
		return false
	}
	if !p.Present {
		return true
	}
	want, err := hex.DecodeString(p.Fingerprint)
	if err != nil {
		return false
	}
	got, _ := hex.DecodeString(k.fingerprint(version, disk, receipt, verifier, observed))
	return hmac.Equal(want, got)
}
