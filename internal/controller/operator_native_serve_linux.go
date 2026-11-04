package controller

import (
	"errors"
	"net"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/operator"
)

func defaultNativeOperatorListener(identity operator.RegistrationIdentity) (nativeServedListener, error) {
	path := filepath.Join(identity.Directory(), "control.sock")
	l, err := operator.NewNativeListener(identity, func() (*net.UnixListener, error) {
		// As for a legacy registration, a stale socket left by an earlier
		// controller is replaced; the listener never unlinks it on Close.
		created, err := Listen(path, 0o666)
		if err != nil {
			return nil, err
		}
		unix, ok := created.(*net.UnixListener)
		if !ok {
			_ = created.Close()
			return nil, errors.New("native operator listener is not a Unix socket")
		}
		unix.SetUnlinkOnClose(false)
		return unix, nil
	})
	if l == nil {
		return nil, errors.Join(err, errors.New("native operator listener was not created"))
	}
	return l, err
}
