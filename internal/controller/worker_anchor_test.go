package controller

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Source/transport fixture only, never a VM-launch producer.
type fixtureWorkerPIDAnchor struct {
	check     func() error
	duplicate func() (*os.File, error)
}

func (a fixtureWorkerPIDAnchor) Check() error { return a.check() }
func (a fixtureWorkerPIDAnchor) DuplicatePIDNamespace() (*os.File, error) {
	if a.duplicate == nil {
		return nil, errors.New("fixture has no namespace duplicate")
	}
	return a.duplicate()
}

func TestWorkerPIDAnchorCheckRequiresOriginalScopedReceiptAndLiveSource(t *testing.T) {
	for _, mode := range []string{"valid", "missing-source", "source-lost", "owner-closed", "wrong-box", "changed-receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, receipt, owner := nativeNonceOwnerFixture(t)
			calls := 0
			s.PIDAnchor = fixtureWorkerPIDAnchor{check: func() error {
				calls++
				// The source can use its own service metadata mutex; the
				// controller must not hold Server.mu across this kernel check.
				s.mu.Lock()
				defer s.mu.Unlock()
				if mode == "changed-receipt" {
					receipt.SetupComplete = true
					if err := s.Receipts.Save(*receipt); err != nil {
						return err
					}
				}
				if mode == "source-lost" {
					return errors.New("original launch source lost")
				}
				return nil
			}}
			box := "box"
			if mode == "missing-source" {
				s.PIDAnchor = nil
			}
			// Actual ServeConn constructs the execution owner AFTER its
			// immutable startup source is configured. Rebuild only this test
			// fixture's unexposed owner to model that original ordering.
			owner.close()
			owner = s.newNativeNonceOwner(t.Context(), Principal{}, Request{ID: receipt.ID, Method: receipt.Method}, receipt, Operation{Mutates: true, EffectGated: true})
			t.Cleanup(owner.close)
			switch mode {
			case "owner-closed":
				owner.close()
			case "wrong-box":
				box = "other"
			}
			err := owner.checkPIDAnchor(t.Context(), box)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
			if (mode == "owner-closed" || mode == "wrong-box" || mode == "missing-source") && calls != 0 {
				t.Fatal("invalid original scope reached source")
			}
		})
	}
}

func TestWorkerPIDAnchorOwnerKeepsOriginalSourceAcrossTransferAndRecheck(t *testing.T) {
	s, receipt, old := nativeNonceOwnerFixture(t)
	old.close()
	originalChecks, originalDuplicates, replacementChecks, replacementDuplicates := 0, 0, 0, 0
	var originalFailure error
	s.PIDAnchor = fixtureWorkerPIDAnchor{
		check:     func() error { originalChecks++; return originalFailure },
		duplicate: func() (*os.File, error) { originalDuplicates++; return nil, errors.New("fixture duplicate only") },
	}
	owner := s.newNativeNonceOwner(t.Context(), Principal{}, Request{ID: receipt.ID, Method: receipt.Method}, receipt, Operation{Mutates: true, EffectGated: true})
	defer owner.close()
	// Even an unexpected later server-field replacement cannot make this
	// original worker transfer one source and recheck a different source.
	s.PIDAnchor = fixtureWorkerPIDAnchor{
		check:     func() error { replacementChecks++; return nil },
		duplicate: func() (*os.File, error) { replacementDuplicates++; return nil, errors.New("replacement duplicate") },
	}
	_, _ = owner.pidAnchor.DuplicatePIDNamespace()
	if err := owner.checkPIDAnchor(t.Context(), "box"); err != nil {
		t.Fatal(err)
	}
	originalFailure = errors.New("original anchor lost")
	if err := owner.checkPIDAnchor(t.Context(), "box"); !errors.Is(err, originalFailure) {
		t.Fatal("new source repaired original proof", err)
	}
	if originalDuplicates != 1 || originalChecks != 2 || replacementChecks != 0 || replacementDuplicates != 0 {
		t.Fatal("original source continuity changed", originalDuplicates, originalChecks, replacementChecks, replacementDuplicates)
	}
	owner.close()
	if err := owner.checkPIDAnchor(t.Context(), "box"); err == nil || originalChecks != 2 {
		t.Fatal("expired execution consulted a source", err)
	}
}

func TestWorkerPIDAnchorCheckpointCannotPrecedeEffects(t *testing.T) {
	c := &WorkerCheckpoints{}
	if err := c.CheckPIDAnchor(context.Background(), "box"); err == nil {
		t.Fatal("ungated namespace check accepted")
	}
	c.Gate()
	if err := c.CheckPIDAnchor(context.Background(), "box"); err == nil {
		t.Fatal("unbegun namespace check accepted")
	}
}
