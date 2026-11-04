package port

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/openshell"
)

// ServeOpenShellForward is the transport body of the native infrastructure
// worker. verify must independently check current native storage, the fixed
// container/record binding and protected SSH endpoint; this function never
// interprets a caller-provided descriptor as authority. bound publishes only
// this attempt's owned listener evidence, after an exclusive bind succeeds.
// No application connection or workload command is used as a startup probe.
func ServeOpenShellForward(ctx context.Context, spec Spec, verify func(context.Context) (openshell.ForwardTarget, error), bound func(net.Listener) error) error {
	if _, err := OpenShellUnitName(spec); err != nil {
		return err
	}
	if verify == nil || bound == nil {
		return errors.New("OpenShell forward requires live native verification and owned listener publication")
	}
	setup, cancel := context.WithTimeout(ctx, 5*time.Second)
	target, err := verify(setup)
	cancel()
	if err != nil {
		return err
	}
	if target.Port != uint16(spec.ContainerPort) || target.HostKey == nil {
		return errors.New("native forward verifier changed the fixed target")
	}
	if err := target.Validate(); err != nil {
		return err
	}
	listen := net.ListenConfig{}
	listener, err := listen.Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(spec.HostPort)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("%w: bind OpenShell forward for %s: %w", ErrPortInUse, spec.Agentbox, err)
		}
		return fmt.Errorf("bind OpenShell forward for %s: %w", spec.Agentbox, err)
	}
	defer func() { _ = listener.Close() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := bound(listener); err != nil {
		return err
	}
	return openshell.ServeForward(ctx, listener, target, func(setup context.Context) error {
		current, err := verify(setup)
		if err != nil {
			return err
		}
		// A later generation may share the box name or socket pathname. It
		// cannot silently become this fixed unit's new transport target.
		if current.Port != target.Port || current.SocketPath != target.SocketPath || current.HostKey == nil || !bytes.Equal(current.HostKey.Marshal(), target.HostKey.Marshal()) {
			return errors.New("OpenShell forward target changed; replace its owned service explicitly")
		}
		return nil
	})
}
