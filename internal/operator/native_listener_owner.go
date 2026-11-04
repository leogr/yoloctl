package operator

import (
	"context"
	"net"
)

// NativeRetirement is the portable view of an acknowledged native listener
// retirement (see NativeListener.Retire). It is not a Stop/rm/withdrawal ACK.
type NativeRetirement interface {
	SelfPending() bool
	WaitSelf(context.Context) error
}

// NativeListenerOwner is the controller's handle on the single original
// owner of one native registration's listener. Identity is metadata only.
type NativeListenerOwner interface {
	Identity() RegistrationIdentity
	RetireListener(ctx context.Context, self net.Conn) (NativeRetirement, error)
}
