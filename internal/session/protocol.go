package session

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/terminal"
)

// Attach protocol: the framed stream spoken between the holder (server
// side of the attach socket) and attach clients. Both directions carry
// frames of `type byte | uint16 BE payload length | payload`.
// Version 5 first negotiates an exact build/core and view type. Managed views
// receive a native snapshot followed by sequenced output/resize/theme/effect
// events; compatibility views receive a filtered transcript. Both send DATA
// input and RESIZE. EXIT preserves the known agent status. Closing detaches.
// Snapshot chunks are private binary data, never application terminal output.
const (
	// frameHello is the holder's first frame; payload is a single byte,
	// the protocol version. Clients refuse a version they don't speak —
	// the holder binary on the VM and the host CLI can skew across
	// upgrades, and a silent mismatch would corrupt the byte stream.
	frameHello = byte('h')

	// frameData carries PTY bytes. Managed holder-to-view events prepend sequence.
	frameData = byte('d')

	// frameResize carries uint16 BE cols, rows, cell width and cell height.
	// Both cell pixel dimensions are zero if no measurement is available.
	// Canonical holder-to-view events prepend the sequence number.
	frameResize = byte('r')

	// frameExit (holder → client) carries the agent's exit status as a
	// 4-byte big-endian two's-complement integer. The holder closes the
	// connection after sending it.
	frameExit = byte('e')
)

// protocolVersion is the current attach-protocol version, carried in
// HELLO.
const protocolVersion = 6

const (
	frameSubscribe    = byte('v')
	frameState        = byte('b')
	frameSnapshot     = byte('s')
	frameSnapshotEnd  = byte('S')
	frameColors       = byte('c')
	frameHoldEnd      = byte('f')
	frameClipboard    = byte('o')
	frameClipboardEnd = byte('O')
	frameError        = byte('!')
	frameActivity     = byte('a') // accepted peer input metadata; never key contents
	// Host actions are targeted live messages, outside canonical VT sequencing.
	frameBrowser       = byte('u')
	frameBrowserResult = byte('U')
	frameBrowserCancel = byte('x')
	frameImageStart    = byte('p')
	frameImageChunk    = byte('i')
	frameImageEnd      = byte('I')
	frameImageResult   = byte('P')
	frameImageProbe    = byte('j')
	frameImageProbed   = byte('J')
	// frameStats replaces SUBSCRIBE for a read-only activity sample: the
	// holder answers with one Stats frame and closes, admitting no viewer.
	frameStats = byte('g')
)

// maxPayload is the largest frame payload writers may produce. It must
// fit the uint16 length field; staying at 32 KiB matches the holder's
// PTY read-chunk size.
const maxPayload = 32 * 1024

// frame is one decoded protocol frame.
type frame struct {
	Type    byte
	Payload []byte
}

// writeFrame writes one frame. The header and payload go out as a single
// Write so a frame is never interleaved mid-header when the caller
// serializes writers with a lock.
func writeFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > maxPayload {
		return fmt.Errorf("session: frame payload %d exceeds max %d", len(payload), maxPayload)
	}
	buf := make([]byte, 3+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(payload)))
	copy(buf[3:], payload)
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(buf) {
			return io.ErrShortWrite
		}
		buf = buf[n:]
	}
	return nil
}

// writeDataFrames writes p as one or more DATA frames, chunking at
// maxPayload.
func writeDataFrames(w io.Writer, p []byte) error { return writeFrameChunks(w, frameData, p) }

func writeFrameChunks(w io.Writer, kind byte, p []byte) error {
	for len(p) > 0 {
		n := min(len(p), maxPayload)
		if err := writeFrame(w, kind, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// readFrame reads one frame. io.EOF is returned untouched on a clean
// close between frames; a close mid-frame surfaces as
// io.ErrUnexpectedEOF.
func readFrame(r io.Reader) (frame, error) {
	var hdr [3]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	n := int(binary.BigEndian.Uint16(hdr[1:3]))
	if n > maxPayload {
		return frame{}, fmt.Errorf("session: oversized frame (%d)", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return frame{}, err
	}
	return frame{Type: hdr[0], Payload: payload}, nil
}

// encodeResize encodes a RESIZE payload.
func encodeResize(g terminal.Geometry) ([]byte, error) {
	if !g.Valid() {
		if g.Cols == 0 || g.Rows == 0 {
			return nil, fmt.Errorf("session: invalid local terminal size %d columns x %d rows; set a nonzero terminal size and attach again", g.Cols, g.Rows)
		}
		return nil, fmt.Errorf("session: invalid resize geometry %+v", g)
	}
	var p [8]byte
	for i, value := range []int{g.Cols, g.Rows, g.CellWidth, g.CellHeight} {
		binary.BigEndian.PutUint16(p[i*2:], uint16(value))
	}
	return p[:], nil
}

// decodeResize decodes a RESIZE payload.
func decodeResize(p []byte) (g terminal.Geometry, err error) {
	if len(p) != 8 {
		return g, fmt.Errorf("session: resize payload is %d bytes, want 8", len(p))
	}
	g = terminal.Geometry{Cols: int(binary.BigEndian.Uint16(p)), Rows: int(binary.BigEndian.Uint16(p[2:])),
		CellWidth: int(binary.BigEndian.Uint16(p[4:])), CellHeight: int(binary.BigEndian.Uint16(p[6:]))}
	if !g.Valid() {
		return terminal.Geometry{}, fmt.Errorf("session: invalid resize geometry %+v", g)
	}
	return g, nil
}

// encodeExit encodes an EXIT payload.
func encodeExit(status int) []byte {
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], uint32(int32(status)))
	return p[:]
}

// decodeExit decodes an EXIT payload.
func decodeExit(p []byte) (int, error) {
	if len(p) != 4 {
		return 0, fmt.Errorf("session: exit payload is %d bytes, want 4", len(p))
	}
	return int(int32(binary.BigEndian.Uint32(p))), nil
}
