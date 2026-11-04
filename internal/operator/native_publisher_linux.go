package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/safepath"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// NativeRegistration is a native operator registration published by the
// controller: its exclusive custody of the registration directory and the
// atomically published identity. It is not a listener, a routing record or
// any request authority, and closing it never unlinks the registration.
type NativeRegistration struct {
	custody *nativeRegistrationCustody
}

func (*NativeRegistration) String() string     { return "<native operator registration>" }
func (r *NativeRegistration) GoString() string { return r.String() }

// publishNativeInfrastructure is replaced by tests: the VM-root precondition
// and the fixed registration root.
var publishNativeInfrastructure = func() (func() (*os.File, error), int, int, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 {
		return nil, 0, 0, errors.New("native operator publication requires VM infrastructure")
	}
	if err := prepareRegistrationRoot(); err != nil {
		return nil, 0, 0, err
	}
	return func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, Root, false) }, 0, 0, nil
}

// PublishNativeIdentity exclusively creates identity's registration directory
// under the fixed root and publishes its native identity atomically. admitted
// is the original publisher's live admission (its reserved claim), checked
// before and during publication. After any directory creation attempt the
// original partial registration is returned with the error, so the caller
// keeps custody of whatever exists: nothing is retried, adopted or erased.
func PublishNativeIdentity(ctx context.Context, identity RegistrationIdentity, admitted func(context.Context) error) (*NativeRegistration, error) {
	if identity.Mode != RegistrationOpenShellNative || admitted == nil {
		return nil, errors.New("native operator publication needs a native identity and its original admission")
	}
	openRoot, uid, gid, err := publishNativeInfrastructure()
	if err != nil {
		return nil, err
	}
	custody, err := createNativeRegistrationCustody(ctx, openRoot, identity, uid, gid, admitted)
	if custody == nil {
		return nil, errors.Join(err, errors.New("native operator registration was not created"))
	}
	r := &NativeRegistration{custody: custody}
	if err != nil {
		return r, err
	}
	return r, custody.publishIdentity(ctx)
}

// ProvisionNative publishes a native box's operator registration at the old
// path's safety level (leogr's relaxed decision): its native identity, then the
// same routing record a legacy registration carries, then it waits, as
// Provision does, for the controller to serve the socket. The incarnation is
// the box's planned one, never a fresh random value.
func ProvisionNative(ctx context.Context, identity RegistrationIdentity, admitted func(context.Context) error) (Endpoint, error) {
	r, err := PublishNativeIdentity(ctx, identity, admitted)
	if r != nil {
		defer func() { _ = r.Close() }()
	}
	if err != nil {
		return Endpoint{}, err
	}
	c := spaceenv.Current()
	raw, err := json.Marshal(Client{Space: c.Space, Generation: c.Generation})
	if err != nil {
		return Endpoint{}, err
	}
	if err := safepath.WriteFile(identity.Directory(), "client.json", raw, 0o644); err != nil {
		return Endpoint{}, err
	}
	if err := waitForEndpoint(ctx, identity.Endpoint); err != nil {
		return Endpoint{}, err
	}
	return identity.Endpoint, nil
}

// RestoreNative re-publishes a running or stopped native operator box's
// registration at controller startup, as legacy Prepare restores a legacy
// one: after a VM reboot erased the volatile registration root it publishes
// the box's native identity again, and an existing identical identity (a
// controller restart without a reboot) is kept. Either way the routing record
// is rewritten for the current generation. It never waits for the socket: the
// controller's scanner serves the registration once it starts.
func RestoreNative(ctx context.Context, identity RegistrationIdentity) error {
	c := spaceenv.Current()
	if c == nil {
		return errors.New("native operator restoration requires VM infrastructure")
	}
	return restoreNativeAt(ctx, Root, identity, Client{Space: c.Space, Generation: c.Generation})
}

func restoreNativeAt(ctx context.Context, root string, identity RegistrationIdentity, routing Client) error {
	if identity.Mode != RegistrationOpenShellNative || identity.Validate() != nil {
		return errors.New("native operator restoration requires a native registration identity")
	}
	raw, err := safepath.ReadFile(root, filepath.Join(identity.Incarnation, "identity.json"), 4096)
	switch {
	case err == nil:
		existing, err := DecodeRegistrationIdentity(raw)
		if err != nil {
			return err
		}
		if existing != identity {
			return errors.New("existing operator registration differs from the box's native identity")
		}
	case errors.Is(err, os.ErrNotExist):
		r, err := PublishNativeIdentity(ctx, identity, func(context.Context) error { return nil })
		if r != nil {
			defer func() { _ = r.Close() }()
		}
		if err != nil {
			return err
		}
	default:
		return err
	}
	client, err := json.Marshal(routing)
	if err != nil {
		return err
	}
	return safepath.WriteFile(filepath.Join(root, identity.Incarnation), "client.json", client, 0o644)
}

// Check rechecks the retained directory, published identity and admission.
func (r *NativeRegistration) Check(ctx context.Context) error {
	if r == nil || r.custody == nil {
		return errors.New("native operator registration is unavailable")
	}
	return r.custody.Check(ctx)
}

// Close releases the retained handles; the registration stays published.
func (r *NativeRegistration) Close() error {
	if r == nil || r.custody == nil {
		return nil
	}
	return r.custody.Close()
}

// prepareRegistrationRoot creates the root-owned infrastructure directories
// above every registration, as legacy preparation does, and refuses unsafe
// existing ones.
func prepareRegistrationRoot() error {
	for _, path := range []string{"/run/yoloctl", Root} {
		if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		var info unix.Stat_t
		if err := unix.Lstat(path, &info); err != nil {
			return err
		}
		if info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Mode&0o022 != 0 {
			return fmt.Errorf("operator infrastructure directory is unsafe: %s", path)
		}
		if info.Uid != 0 {
			return errors.New("operator infrastructure must belong to root")
		}
	}
	return nil
}
