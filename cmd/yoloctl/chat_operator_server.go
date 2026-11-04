package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

var operatorManagedServerTargetFn = operatorManagedServerTarget

const operatorTargetSetupTimeout = 10 * time.Second

var dialOperatorSSH = func(ctx context.Context, name string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, operatorTargetSetupTimeout)
	defer cancel()
	return (&net.Dialer{}).DialContext(ctx, "unix", sandbox.SocketHostPath(name))
}

// Call only while the native setup lock is held. Finite lifecycle operations
// remain available; arbitrary execution and terminal input must not reach the
// server's human credential/config/control mounts. The fixed managed service
// commands use runManagedChatExec and never enter this user-command path.
func operatorManagedServerTarget(ctx context.Context, space, name string) (bool, error) {
	p := currentControllerPrincipal(ctx)
	if p.Admin() {
		return false, nil
	}
	if !spaceenv.Native() || ctx.Value(spaceReleaseKey{}) == nil {
		return false, errors.New("operator target selection requires the native space setup lock")
	}
	if err := spaceenv.RequireSpace(space); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, operatorTargetSetupTimeout)
	defer cancel()
	if err := checkOperatorPrincipal(ctx, p); err != nil {
		return false, err
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return false, err
	}
	r, err := store.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("cannot establish managed server reservation: %w", err)
	}
	if protectedManagedServerTarget(r, name, nil) {
		return true, nil
	}
	details, err := inspectOperatorChatTarget(ctx, name, container.New(space))
	if err != nil {
		return false, err
	}
	return protectedManagedServerTarget(r, name, details), nil
}

type operatorChatRuntime interface {
	List(context.Context, string) ([]container.Container, error)
	Inspect(context.Context, string) (*container.Details, error)
}

func inspectOperatorChatTarget(ctx context.Context, name string, runtime operatorChatRuntime) (*container.Details, error) {
	// The ordinary box resolver filters by the primary box label. This access
	// boundary must also inspect stale/foreign runtimes that lost that label
	// while retaining privileged chat mounts or the server role.
	containers, err := runtime.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("operator target runtime is unavailable: %w", err)
	}
	for _, target := range containers {
		if target.Name != name {
			continue
		}
		details, err := runtime.Inspect(ctx, name)
		if err != nil {
			return nil, err
		}
		if details == nil || details.Name != name {
			return nil, errors.New("operator target runtime identity is unavailable")
		}
		return details, nil
	}
	return nil, nil // absence is proved by the unfiltered runtime inventory
}

func protectedManagedServerTarget(r *managedchat.Room, name string, details *container.Details) bool {
	// A dormant reservation or a missing/replaced declaration never turns the
	// reserved server name into an operator execution target.
	if r != nil && r.Server.Name == name {
		return true
	}
	if details == nil {
		return false
	}
	if details.Labels[managedchat.RoleLabel] == "server" {
		return true
	}
	for _, mount := range details.Mounts {
		for _, protected := range []string{managedchat.HumanMount, managedchat.ServerConfigMount, managedchat.ServerDataMount, managedchat.ControlMount} {
			if chatOperatorPathsOverlap(mount.Destination, protected) {
				return true
			}
		}
	}
	return false
}

func chatOperatorPathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
}

func guardOperatorChatExecution(ctx context.Context, space, name string) error {
	protected, err := operatorManagedServerTargetFn(ctx, space, name)
	if err != nil {
		return err
	}
	if protected {
		return errors.New("operators cannot execute commands or attach to the managed chat server's human identity; use finite chat administration or server lifecycle operations")
	}
	return nil
}
