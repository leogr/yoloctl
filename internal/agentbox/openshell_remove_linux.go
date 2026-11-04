//go:build linux

package agentbox

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Removal uses the original container's Stop/rm results. Cleanup failures name
// the resource and continue; global absence and retained inode proofs are not
// prerequisites for removing a box or later creating the same name.
func (a LiveApplier) prepareNativeContainerRemoval(ctx context.Context, name, cid string, force bool) (_ *NativeContainerRemoval, err error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.Client == nil || a.VerifyStorage == nil {
		return nil, errors.New("native removal requires admitted VM-root storage")
	}
	if err = layout.ValidateName("agentbox", name); err != nil {
		return nil, err
	}
	if err = spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return nil, err
	}
	if err = a.VerifyStorage(ctx); err != nil {
		return nil, err
	}
	selected, err := inspectOpenShellRuntime(ctx, a.Client, name, cid)
	if err != nil {
		return nil, err
	}
	projection, err := a.Client.NativeOperatorProjection(ctx, cid, name)
	if err != nil {
		return nil, err
	}
	choice, err := projection.Selection()
	if err != nil {
		return nil, err
	}
	// Keep the selected box/CID association before any lifecycle effects.
	if choice.ContainerID != cid || choice.Box != name {
		return nil, errors.New("native operator projection differs from original CID/box")
	}
	var registrations *operator.RegistrationInventory
	var retained *nativeOperatorRegistrationRemoval
	var live *operator.RegistrationIdentity
	listenerRetired := false
	removed := false
	warn := func(resource string, failure error) {
		if failure != nil {
			_, _ = fmt.Fprintf(os.Stderr, "warning: box %s: %s: %v\n", name, resource, failure)
		}
	}
	closeOwned := func() error {
		if registrations != nil {
			warn("operator inventory observer could not be closed", registrations.Close())
		}
		if retained != nil {
			warn("operator registration cleanup could not be closed", retained.Close())
		}
		return nil
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeOwned())
		}
	}()
	owned := func(check context.Context) error {
		if removed {
			return errors.New("cannot re-inspect a removed native container")
		}
		if err := a.VerifyStorage(check); err != nil {
			return err
		}
		current, err := inspectOpenShellRuntime(check, a.Client, name, cid)
		if err != nil {
			return err
		}
		if !sameNativeStopBinding(current, selected) {
			return errors.New("native removal original runtime binding changed")
		}
		return nil
	}
	inactive := func(check context.Context) error {
		if err := owned(check); err != nil {
			return err
		}
		d, err := a.Client.Inspect(check, cid)
		if err != nil {
			return err
		}
		if d.ID != cid || !nativeInactiveCleanupObservation(d) {
			return errors.New("native removal lacks its acknowledged fixed-CID inactive observation")
		}
		return nil
	}
	// Discover registrations to withdraw, without turning unavailable or missing
	// bookkeeping into a cleanup fence. Each actual withdrawal still reports
	// its own result; no post-cleanup census is required.
	registrations, err = operator.ObserveRegistrationInventory(ctx, a.VerifyStorage)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			warn("operator registrations could not be listed for cleanup", err)
		}
		err = nil
	}
	if registrations != nil {
		identities, e := registrations.IdentitiesForBox(name, "")
		if e != nil {
			warn("operator registrations could not be selected for cleanup", e)
		} else {
			if choice.Enabled {
				live = liveNativeOperatorIdentity(identities, choice.Endpoint)
			}
			if len(identities) != 0 {
				retained, e = prepareNativeOperatorRegistrationRemoval(ctx, identities, owned)
				warn("operator registrations could not be prepared for cleanup", e)
			}
		}
	}
	// The SDK callback checks current storage, not a universe-wide absence
	// claim. Stop/inactive/fixed-CID rm retain their actual acknowledgements.
	remaining := a.VerifyStorage
	if err = owned(ctx); err != nil {
		return nil, err
	}
	result := &NativeContainerRemoval{close: closeOwned}
	result.remove = func(removeCtx context.Context) error {
		return openshell.RemoveNativeContainer(removeCtx, selected.Record, selected.Release, cid, openshell.NativeContainerRemoval{
			VerifyOwned: owned,
			Stop: func(stop context.Context) error {
				if live != nil && !listenerRetired {
					// One use, never retried. As on the old path, this step's
					// own error is a notice and the removal continues.
					listenerRetired = true
					if err := operator.RetireNativeListener(stop, *live); err != nil {
						_, _ = fmt.Fprintf(os.Stderr, "warning: box %s: operator listener %s was not retired: %v\n", name, live.Incarnation, err)
					}
				}
				if err := a.Client.StopContainerID(stop, cid, force); err != nil {
					return err
				}
				if err := inactive(stop); err != nil {
					return err
				}
				return nil
			},
			VerifyInactive: inactive,
			RemoveContainerID: func(remove context.Context, actual string) error {
				if actual != cid || removed {
					return errors.New("native removal CID changed or acknowledgement replayed")
				}
				if err := a.Client.RemoveContainerID(remove, cid); err != nil {
					return err
				}
				removed = true // ONLY this original successful rm response permits absence.
				return nil
			},
			VerifyRemaining: remaining,
			WithdrawEndpoints: func(withdraw context.Context) error {
				if !removed {
					return errors.New("native endpoint withdrawal lacks original rm acknowledgement")
				}
				if err := remaining(withdraw); err != nil {
					return err
				}
				if retained != nil {
					// Each registration's own withdrawal result decides; a
					// leftover is named, never a re-scan condition. A later
					// create gets a fresh incarnation.
					if err := retained.withdraw(withdraw, removed, remaining); err != nil {
						_, _ = fmt.Fprintf(os.Stderr, "warning: box %s: operator registration was not fully removed: %v\n", name, err)
					}
				}
				warn("public SSH socket could not be removed", removeNativePublicSSHSocket(sandbox.SocketHostPath(name)))
				return nil
			},
		})
	}
	return result, nil
}

// Remove the named public endpoint itself. Missing endpoints already satisfy
// this cleanup step; no original inode, mount or global-absence proof is needed.
func removeNativePublicSSHSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a socket; left untouched", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
