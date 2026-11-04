package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Portable DTOs ONLY. Linux vmanchor types never enter worker.go/server.go.
// These structs are correlation projections, not source or effect factories.
type WorkerControllerAssociation struct {
	Version           int                        `json:"version"`
	Hello             Hello                      `json:"hello"`
	DiskID            string                     `json:"disk_id"`
	Context           spaceenv.Context           `json:"controller_context"`
	SourceAssociation string                     `json:"source_association"`
	Singleton         WorkerSingletonAssociation `json:"singleton"`
}
type WorkerSingletonAssociation struct {
	Descriptor int    `json:"descriptor"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
	Mount      uint64 `json:"mount"`
}
type workerReceiptAssociationV1 struct {
	ID           string    `json:"id"`
	Method       string    `json:"method"`
	Principal    Principal `json:"principal"`
	Generation   string    `json:"generation"`
	DiskID       string    `json:"disk_id"`
	ProofVersion int       `json:"proof_version"`
	Started      string    `json:"started"`
	InitialPhase string    `json:"initial_phase"`
	InitialScope *Scope    `json:"initial_scope"`
}
type workerAssociationProjectionV1 struct {
	Version    int                         `json:"version"`
	Controller WorkerControllerAssociation `json:"controller"`
	Receipt    workerReceiptAssociationV1  `json:"receipt"`
}

// No request Args/Flags/Payload/secret input enters this projection. The actual
// closed provider must bind SourceAssociation/singleton/context to its SAME
// original source. Hashes/metadata here cannot manufacture that relation.
func projectWorkerAssociationV1(controller WorkerControllerAssociation, receipt Receipt) (string, []byte, error) {
	if controller.Version != 1 || controller.Hello.Protocol != ProtocolVersion || controller.Hello.Bundle == "" || controller.Hello.Space == "" || controller.Hello.Generation == "" || controller.DiskID == "" || controller.SourceAssociation == "" || len(controller.SourceAssociation) > 256 || controller.Singleton.Descriptor < 0 || controller.Singleton.Device == 0 || controller.Singleton.Inode == 0 || controller.Singleton.Mount == 0 {
		return "", nil, errors.New("original worker controller association unavailable")
	}
	if err := controller.Context.Validate(); err != nil {
		return "", nil, err
	}
	if controller.Context.Space != controller.Hello.Space || controller.Context.Generation != controller.Hello.Generation || controller.Context.Bundle != controller.Hello.Bundle || controller.Context.DiskID != controller.DiskID {
		return "", nil, errors.New("worker association controller context differs")
	}
	if receipt.ID == "" || receipt.Method == "" || receipt.State != "pending" || receipt.EffectPhase != EffectWaiting || receipt.SetupComplete || receipt.Completion != nil || receipt.Generation != controller.Hello.Generation || receipt.DiskID != controller.DiskID || receipt.Started.IsZero() || receipt.ProofVersion < documentProofVersion || receipt.ProofVersion > ProofVersion {
		return "", nil, errors.New("worker bootstrap requires original pending waiting receipt")
	}
	if err := receipt.Scope.validate(); err != nil {
		return "", nil, err
	}
	// Clone mutable input fields before freezing. A normal worker starts with a
	// nil initial Scope; later BeginEffects legitimately publishes its scope.
	controller.Context.Mounts = slices.Clone(controller.Context.Mounts)
	projection := workerAssociationProjectionV1{Version: 1, Controller: controller, Receipt: workerReceiptAssociationV1{receipt.ID, receipt.Method, receipt.Principal, receipt.Generation, receipt.DiskID, receipt.ProofVersion, receipt.Started.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), receipt.EffectPhase, cloneScope(receipt.Scope)}}
	raw, err := json.Marshal(projection)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), raw, nil
}

// WorkerInput includes only AssociationDigest. Its canonical typed bytes are
// hashed separately AFTER all private indices/association fields are assigned.
// InputDigest is NEVER added to WorkerInput or recursively hashed into itself.

type workerHandoffFiles struct{ Payload, Checkpoint, View, Phase *os.File }
type workerPreparedHandoff interface {
	Input() WorkerInput
	CanonicalInput() []byte
	ChildFiles() []*os.File
	PublishStarted(*os.Process) error
	Close() error
}
type workerHandoffOwner interface {
	Prepare(WorkerInput, workerHandoffFiles) (workerPreparedHandoff, error)
	CheckPIDAnchor(context.Context, string) error
	Close() error
}
