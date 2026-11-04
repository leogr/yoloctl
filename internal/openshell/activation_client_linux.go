package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// ActivateNative is one initial authorization inside the caller's existing
// native mutation admission/receipt. inspect must validate the selected
// container ID/incarnation, full helper argv, labels and exact live binds on
// every call. This function adds independent peer/process/namespace binding.
// It never reconnects or retries an activation after any successful connect.
func ActivateNative(ctx context.Context, prepared PreparedRuntime, inspect func(context.Context) (ActivationTarget, error)) error {
	activation, err := ActivateNativeOwned(ctx, prepared, inspect)
	// Compatibility entry cannot give its caller the original partial handle.
	// Never present a discarded reserved attempt as an ordinary cleanup/retry.
	if err != nil && activation != nil && (activation.reservation != nil || activation.lock != nil) {
		err = &NativeEffectsUncertain{Cause: err}
	}
	if closeErr := activation.Close(); closeErr != nil {
		err = &NativeEffectsUncertain{Cause: errors.Join(err, closeErr)}
	}
	return err
}

// ActivateNativeOwned retains original namespace attempts for the enclosing
// creation transaction. The caller must Close it after readiness/owned cleanup;
// a typed unknown prohibits automatic cleanup and authorization replay.
func ActivateNativeOwned(ctx context.Context, prepared PreparedRuntime, inspect func(context.Context) (ActivationTarget, error)) (*NativeActivation, error) {
	activation := &NativeActivation{prepared: prepared}
	err := activation.activate(ctx, inspect)
	activation.failure = err
	return activation, err
}

func (a *NativeActivation) activate(ctx context.Context, inspect func(context.Context) (ActivationTarget, error)) (err error) {
	prepared := a.prepared
	authorizationAttempted := false
	defer func() {
		if authorizationAttempted && err != nil {
			err = &NativeEffectsUncertain{Cause: err}
		}
	}()

	if err := nativePreparedIdentity(prepared); err != nil {
		return err
	}
	if inspect == nil {
		return errors.New("native activation requires a live container verifier")
	}
	if err := rejectNoInitializationCompletion(prepared); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	status, err := openActivationDirectory(prepared.ContainerRoot, prepared.Record.Generation)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(status) }()
	var previous unix.Stat_t
	if err := unix.Fstatat(status, "activation.json", &previous, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return &NativeEffectsUncertain{Cause: errors.New("native activation was already attempted or its outcome is uncertain; inspect it without replay")}
	}
	key, err := loadActivationKey(prepared.GenerationRoot, prepared.Record)
	if err != nil {
		return err
	}
	var socket unix.Stat_t
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Fstatat(status, activationSocketName, &socket, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			if socket.Mode&unix.S_IFMT != unix.S_IFSOCK || socket.Uid != 0 || socket.Gid != 0 || socket.Nlink != 1 {
				return errors.New("native activation endpoint is not an owned socket")
			}
			if socket.Mode&0o7777 == 0o600 {
				break
			}
		} else if !errors.Is(err, unix.ENOENT) {
			return err
		}
		// This is read-only startup readiness, before any connection or
		// authentication packet. It is never an authorization retry.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	address := fmt.Sprintf("/proc/self/fd/%d/%s", status, activationSocketName)
	connection, err := (&net.Dialer{}).DialContext(ctx, "unixpacket", address)
	if err != nil {
		return err
	}
	conn := connection.(*net.UnixConn)
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var current unix.Stat_t
	if unix.Fstatat(status, activationSocketName, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != socket.Dev || current.Ino != socket.Ino || current.Mode&0o7777 != 0o600 {
		return errors.New("native activation socket changed during connect")
	}
	nonce, err := activationNonce()
	if err != nil {
		return err
	}
	hello, err := prepared.Record.activationHello(nonce)
	if err != nil {
		return err
	}
	if err := sendActivationPacket(conn, key.authenticate(hello), nil); err != nil {
		return err
	}
	var offer activationEnvelope
	descriptors, err := activationPacket(conn, &offer, 1)
	if err != nil {
		return err
	}
	namespace := descriptors[0]
	defer func() { _ = namespace.Close() }()
	if !lowerHex(offer.Message.ServerNonce, 32) || offer.Message.Namespace.Inode == 0 {
		return errors.New("activation offer lacks a fresh peer/namespace identity")
	}
	expected := hello
	expected.Kind = "offer"
	expected.ServerNonce = offer.Message.ServerNonce
	expected.Namespace = offer.Message.Namespace
	if err := key.verify(offer, expected); err != nil {
		return err
	}
	peer, err := pinActivationPeer(ctx, conn, namespace, prepared, inspect)
	if err != nil {
		return err
	}
	defer peer.close()
	if peer.identity != expected.Namespace {
		return errors.New("offered namespace identity does not match the independently pinned descriptor")
	}
	proof, err := a.createOwnedEgress(ctx, peer, namespace)
	if err != nil {
		return err
	}
	if err := peer.revalidate(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	if err := publishPrivateOnce(prepared.GenerationRoot+"/status", "activation.json", raw, 0, 0); err != nil {
		return &NativeEffectsUncertain{Cause: err}
	}
	if err := peer.revalidate(ctx); err != nil {
		return err
	}
	expected.Kind = "authorize"
	expected.ProofSHA256 = sha256Hex(raw)
	authorizationAttempted = true
	if err := sendActivationPacket(conn, key.authenticate(expected), nil); err != nil {
		return err
	}
	var acknowledgement activationEnvelope
	if _, err := activationPacket(conn, &acknowledgement, 0); err != nil {
		return err
	}
	expected.Kind = "activated"
	if err := key.verify(acknowledgement, expected); err != nil {
		return err
	}
	// A positive exchange is launch authorization, not SSH readiness or proof
	// of the complete enclosing transaction. The native caller still checks
	// readiness and completes its original durable operation receipt.
	return peer.revalidate(ctx)
}
