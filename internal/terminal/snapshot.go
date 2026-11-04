package terminal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// MaxSnapshotBytes bounds the complete snapshot, including its adapter header.
// WASM memory is independently capped at 256 MiB; decoding has a time budget.
const MaxSnapshotBytes = 64 << 20

const (
	snapshotMagic      = "YOLOVT\x00\x01"
	snapshotHeaderSize = len(snapshotMagic) + sha256.Size + 4
	snapshotDecodeTime = 5 * time.Second
)

var (
	snapshotCore = sha256.Sum256(artifact)
	// ErrSnapshotUnavailable is recoverable: an oversized unfinished control
	// can prevent capture until more output brings the parser to ground.
	ErrSnapshotUnavailable = errors.New("terminal snapshot unavailable")
	ErrSnapshotTooLarge    = errors.New("terminal snapshot exceeds size limit")
	ErrSnapshotMismatch    = errors.New("terminal snapshot format or core mismatch")
)

// CoreIdentity identifies the exact native parser and private bridge.
func CoreIdentity() string { return fmt.Sprintf("%x", snapshotCore) }

// Snapshot captures both screens, history, modes, colors and parser continuation.
// It is opaque state for this exact core/bridge, not VT output and not a stable
// storage format. The owner must serialize this with every Terminal operation.
// Effects (bells/replies), selection, viewport and callbacks are not captured.
// The pinned native format does not carry Kitty images or glyph registrations.
func (t *Terminal) Snapshot() ([]byte, error) {
	if t.err != nil {
		return nil, t.err
	}
	ptr, capacity := t.scratch+8, uint64(scratchSize-8)
	status := int32(t.call("ghostty_snapshot_encode_buf", t.ptr, ptr, capacity, t.scratch))
	if t.err != nil {
		return nil, t.err
	}
	n, ok := t.mod.Memory().ReadUint32Le(uint32(t.scratch))
	if !ok {
		return nil, errors.New("terminal snapshot size outside memory")
	}
	if n > uint32(MaxSnapshotBytes-snapshotHeaderSize) {
		return nil, ErrSnapshotTooLarge
	}
	// The native fixed-buffer encoder reports the exact required capacity on
	// out_of_space. It does not return a usable partial snapshot.
	if status == -3 && uint64(n) > capacity {
		capacity = uint64(n)
		ptr = t.call("ghostty_wasm_alloc", capacity)
		if ptr == 0 || t.err != nil {
			return nil, fmt.Errorf("allocate terminal snapshot: %w", errors.Join(ErrSnapshotUnavailable, t.err))
		}
		defer t.call("ghostty_wasm_free", ptr, capacity)
		status = int32(t.call("ghostty_snapshot_encode_buf", t.ptr, ptr, capacity, t.scratch))
		n, ok = t.mod.Memory().ReadUint32Le(uint32(t.scratch))
	}
	if t.err != nil {
		return nil, t.err
	}
	if status != 0 {
		return nil, fmt.Errorf("%w (native result %d)", ErrSnapshotUnavailable, status)
	}
	if !ok || n == 0 || uint64(n) > capacity {
		return nil, errors.New("invalid terminal snapshot size")
	}
	p, ok := t.mod.Memory().Read(uint32(ptr), n)
	if !ok {
		return nil, errors.New("terminal snapshot outside memory")
	}
	out := make([]byte, snapshotHeaderSize+int(n))
	copy(out, snapshotMagic)
	copy(out[len(snapshotMagic):], snapshotCore[:])
	binary.LittleEndian.PutUint32(out[snapshotHeaderSize-4:], uint32(t.history))
	copy(out[snapshotHeaderSize:], p)
	return out, nil
}

// RestoreSnapshot validates a complete snapshot in a fresh bounded instance.
// The caller keeps its old terminal until this succeeds, then swaps and closes
// it under the owner's lock. Errors/cancellation never mutate that old instance.
// The supplied callback receives only future replies. Decode emits no host
// effects; local selection/viewport start empty/at bottom and Bells starts at 0.
func RestoreSnapshot(ctx context.Context, data []byte, reply func([]byte)) (*Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > MaxSnapshotBytes {
		return nil, ErrSnapshotTooLarge
	}
	if len(data) <= snapshotHeaderSize || string(data[:len(snapshotMagic)]) != snapshotMagic ||
		!bytes.Equal(data[len(snapshotMagic):snapshotHeaderSize-4], snapshotCore[:]) {
		return nil, ErrSnapshotMismatch
	}
	t, err := newInstance(reply)
	if err != nil {
		return nil, err
	}
	success := false
	baseContext := t.ctx
	defer func() {
		t.ctx = baseContext
		if !success {
			_ = t.Close()
		}
	}()
	decodeContext, cancel := context.WithTimeout(ctx, snapshotDecodeTime)
	defer cancel()
	t.ctx = context.WithValue(decodeContext, contextKey{}, t)
	t.history = int(binary.LittleEndian.Uint32(data[snapshotHeaderSize-4:]))
	t.visibleHistory = t.history
	data = data[snapshotHeaderSize:]
	ptr := t.call("ghostty_wasm_alloc", uint64(len(data)))
	if t.err != nil {
		return nil, fmt.Errorf("allocate terminal snapshot: %w", errors.Join(decodeContext.Err(), t.err))
	}
	if ptr == 0 || !t.mod.Memory().Write(uint32(ptr), data) {
		return nil, errors.New("cannot allocate terminal snapshot")
	}
	status := int32(t.call("yolo_vt_snapshot_restore", ptr, uint64(len(data)), uint64(t.history), t.scratch))
	t.call("ghostty_wasm_free", ptr, uint64(len(data)))
	if t.err != nil {
		return nil, fmt.Errorf("decode terminal snapshot: %w", errors.Join(decodeContext.Err(), t.err))
	}
	if status != 0 {
		return nil, fmt.Errorf("invalid terminal snapshot (native result %d)", status)
	}
	native, ok := t.mod.Memory().ReadUint32Le(uint32(t.scratch))
	if !ok || native == 0 {
		return nil, errors.New("terminal snapshot returned no terminal")
	}
	t.ptr = uint64(native)
	t.hold = t.Mode(2026)
	if t.err != nil {
		return nil, t.err
	}
	success = true
	return t, nil
}
