package agentbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// ForwardBinding records only observations selected by an admitted native
// mutation. Neither the returned descriptor nor its attempt nonce is authority.
func (r OpenShellRuntime) ForwardBinding(attempt string) port.ForwardBinding {
	return port.ForwardBinding{ContainerID: r.ContainerID, Incarnation: r.Launch.Incarnation, Qualification: r.Launch.Qualification,
		HelperSHA256: r.Launch.HelperSHA256, Generation: r.Record.Generation, RecordSHA256: r.RecordSHA256, Attempt: attempt}
}

func (r OpenShellRuntime) matchesForward(binding port.ForwardBinding) bool {
	return r.Running && r.PID > 1 && !r.StartedAt.IsZero() && r.ForwardBinding(binding.Attempt) == binding
}

// ServeNativeOpenShellForward is called only by the fixed VM-root CLI role,
// after controller.LoadRuntime. Storage is independently reverified before
// every connection and bound-status publication. It never launches a workload,
// enters a namespace or accepts an address/command from incoming connections.
func ServeNativeOpenShellForward(ctx context.Context, client *container.Client, spec port.Spec, binding port.ForwardBinding, verifyStorage func(context.Context) error) (err error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || client == nil || verifyStorage == nil {
		return errors.New("OpenShell forwarding requires native root infrastructure")
	}
	if err := spaceenv.RequireSpace(client.Env()); err != nil {
		return err
	}
	if _, err := port.ForwardWorkerArgs(spec, binding); err != nil {
		return err
	}
	process, err := currentForwardProcessIdentity()
	if err != nil {
		return err
	}
	directory, err := openForwardSocketDirectory(spec.Agentbox)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	var selected OpenShellRuntime
	verify := func(setup context.Context) (openshell.ForwardTarget, error) {
		if err := verifyStorage(setup); err != nil {
			return openshell.ForwardTarget{}, err
		}
		observed, err := InspectOpenShellRuntime(setup, client, spec.Agentbox)
		if err != nil {
			return openshell.ForwardTarget{}, err
		}
		if !observed.matchesForward(binding) {
			return openshell.ForwardTarget{}, errors.New("OpenShell forward no longer matches its fixed container/generation")
		}
		if err := verifyForwardSocket(directory); err != nil {
			return openshell.ForwardTarget{}, err
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(observed.Record.SSHHostPublicKey))
		if err != nil {
			return openshell.ForwardTarget{}, err
		}
		return openshell.ForwardTarget{SocketPath: fmt.Sprintf("/proc/self/fd/%d/ssh.sock", directory.Fd()), Port: uint16(spec.ContainerPort), HostKey: key}, nil
	}
	// Obtain the trusted native status path before the listener starts. This
	// read grants no readiness; publication happens only after exclusive bind.
	setup, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := verifyStorage(setup); err != nil {
		cancel()
		return err
	}
	selected, err = InspectOpenShellRuntime(setup, client, spec.Agentbox)
	cancel()
	if err != nil {
		return err
	}
	if !selected.matchesForward(binding) {
		return errors.New("OpenShell forward startup binding changed")
	}
	statusRoot := filepath.Join(selected.ControlRoot, "generations", binding.Generation, "status")
	if err := safepath.VerifyPrivateDirectory(selected.ControlRoot, filepath.Join("generations", binding.Generation, "status"), 0, 0); err != nil {
		return err
	}
	if err := safepath.PrivateDirectory(statusRoot, "forwards", 0, 0); err != nil {
		return err
	}
	statusDirectory := filepath.Join(statusRoot, "forwards")
	var published []byte
	defer func() {
		if published != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			err = errors.Join(err, removeForwardStatus(cleanup, statusDirectory, binding.Attempt+".json", published, 0, 0))
		}
	}()
	return port.ServeOpenShellForward(ctx, spec, verify, func(listener net.Listener) error {
		endpoint, ok := listener.Addr().(*net.TCPAddr)
		if !ok || !endpoint.IP.Equal(net.IPv4(127, 0, 0, 1)) || endpoint.Port != spec.HostPort {
			return errors.New("forward bound an unexpected listener")
		}
		check, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if _, err := verify(check); err != nil {
			return err
		}
		return withForwardListener(listener, spec.HostPort, func(socket forwardListenerIdentity) error {
			raw, err := publishForwardStatus(check, statusDirectory, spec, binding, process, socket, 0, 0)
			if err == nil {
				published = raw
			}
			return err
		})
	})
}

func openForwardSocketDirectory(name string) (*os.File, error) {
	// Every component is opened without following links and checked against
	// infrastructure ownership. Hold the final FD across all connections;
	// recreation cannot silently replace this unit's socket directory.
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range []string{"run", "yoloctl", name} {
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0o7022 != 0 {
			_ = unix.Close(fd)
			return nil, errors.New("forward socket directory has an untrusted owner/writer")
		}
	}
	return os.NewFile(uintptr(fd), sandbox.SocketDirHost(name)), nil
}

func verifyForwardSocket(directory *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), "ssh.sock", &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Mode&0o7777 != 0o600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 {
		return errors.New("forward endpoint is not the protected runtime SSH socket")
	}
	return nil
}
