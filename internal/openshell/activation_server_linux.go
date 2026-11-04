package openshell

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const activationSocketName = "activation.sock"

// awaitNativeActivation is called exactly once by the verified container's
// root PID1, before any child or public endpoint exists. It does not outlive
// initial activation and cannot act as a controller liveness channel.
func awaitNativeActivation(ctx context.Context, record RuntimeRecord) (_ activationProof, err error) {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return activationProof{}, errors.New("native activation server requires the container's root PID 1")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	generationRoot := filepath.Join(NativeProjection, "generations", record.Generation)
	status, err := openActivationDirectory(NativeProjection, record.Generation)
	if err != nil {
		return activationProof{}, err
	}
	defer func() { _ = unix.Close(status) }()
	key, err := loadActivationKey(generationRoot, record)
	if err != nil {
		return activationProof{}, err
	}
	var previous unix.Stat_t
	if err := unix.Fstatat(status, activationSocketName, &previous, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return activationProof{}, errors.New("initial activation refuses an existing status socket or uncertain directory")
	}
	// The native data-disk path can exceed sockaddr_un's limit. This short
	// descriptor alias retains the verified directory and follows no user path.
	address := fmt.Sprintf("/proc/self/fd/%d/%s", status, activationSocketName)
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: address, Net: "unixpacket"})
	if err != nil {
		return activationProof{}, err
	}
	listener.SetUnlinkOnClose(false)
	defer func() { _ = listener.Close() }()
	var socket unix.Stat_t
	if err := unix.Fstatat(status, activationSocketName, &socket, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return activationProof{}, err
	}
	defer func() {
		var current unix.Stat_t
		if cleanup := unix.Fstatat(status, activationSocketName, &current, unix.AT_SYMLINK_NOFOLLOW); cleanup != nil {
			err = errors.Join(err, cleanup)
			return
		}
		if current.Dev != socket.Dev || current.Ino != socket.Ino || current.Mode&unix.S_IFMT != unix.S_IFSOCK || current.Uid != 0 {
			err = errors.Join(err, errors.New("owned activation socket changed before cleanup"))
			return
		}
		if cleanup := unix.Unlinkat(status, activationSocketName, 0); cleanup != nil {
			err = errors.Join(err, cleanup)
			return
		}
		err = errors.Join(err, unix.Fsync(status))
	}()
	if err := unix.Fchmodat(status, activationSocketName, 0o600, 0); err != nil {
		return activationProof{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	conn, err := listener.AcceptUnix()
	if err != nil {
		return activationProof{}, err
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return activationProof{}, err
	}
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	credentials, err := activationCredentials(conn)
	// The native PID can be outside the container's PID namespace (Pid=0).
	// Root credentials plus the per-generation authenticator prove this end;
	// the native client separately pins the helper's actual VM-visible PID.
	if err != nil || credentials == nil || credentials.Uid != 0 || credentials.Gid != 0 {
		return activationProof{}, errors.New("initial activation peer lacks native root credentials")
	}
	var hello activationEnvelope
	if _, err := activationPacket(conn, &hello, 0); err != nil {
		return activationProof{}, err
	}
	expected, err := record.activationHello(hello.Message.ClientNonce)
	if err != nil {
		return activationProof{}, err
	}
	if err := key.verify(hello, expected); err != nil {
		return activationProof{}, err
	}
	netns, err := unix.Open("/proc/self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return activationProof{}, err
	}
	namespaceFile := os.NewFile(uintptr(netns), "activation-own-netns")
	defer func() { _ = namespaceFile.Close() }()
	identity, err := networkNamespaceIdentity(netns)
	if err != nil {
		return activationProof{}, err
	}
	expected.Kind = "offer"
	expected.ServerNonce, err = activationNonce()
	if err != nil {
		return activationProof{}, err
	}
	expected.Namespace = identity
	if err := sendActivationPacket(conn, key.authenticate(expected), namespaceFile); err != nil {
		return activationProof{}, err
	}
	var authorization activationEnvelope
	if _, err := activationPacket(conn, &authorization, 0); err != nil {
		return activationProof{}, err
	}
	proof, raw, err := loadActivationProof(generationRoot, record, identity)
	if err != nil {
		return activationProof{}, err
	}
	expected.Kind = "authorize"
	expected.ProofSHA256 = sha256Hex(raw)
	if err := key.verify(authorization, expected); err != nil {
		return activationProof{}, err
	}
	if err := ctx.Err(); err != nil {
		return activationProof{}, err
	}
	expected.Kind = "activated"
	if err := sendActivationPacket(conn, key.authenticate(expected), nil); err != nil {
		return activationProof{}, err
	}
	return proof, nil
}

func openActivationDirectory(runtimeRoot, generation string) (int, error) {
	if len(generation) != 49 || generation[0] != 'g' || !lowerHex(generation[1:], 24) {
		return -1, errors.New("invalid activation generation directory")
	}
	fd, err := unix.Open(runtimeRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range []string{"", "generations", generation, "status"} {
		if component != "" {
			next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			_ = unix.Close(fd)
			if err != nil {
				return -1, err
			}
			fd = next
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0o7777 != 0o700 {
			_ = unix.Close(fd)
			return -1, errors.New("activation directory is not private to native root")
		}
	}
	return fd, nil
}
