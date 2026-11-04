package vmwire

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
)

// ForwardRequest negotiates framing without breaking older guest images, which
// ignore TCPStream and reply with ForwardOK. UDP keeps its datagram protocol.
type ForwardRequest struct {
	Port
	TCPStream bool `json:"tcp_stream,omitempty"`
}

const (
	ForwardOK       = 0
	ForwardStreamOK = 2
	maxTCPFrame     = 64 << 10
)

// TCPStream carries each direction's EOF in-band. Firecracker's vsock carrier
// turns a Unix socket EOF into a full shutdown, so carrier CloseWrite loses
// replies from services that wait for the client's EOF before responding.
// Like net.Conn, it permits one concurrent reader and multiple writers.
type TCPStream struct {
	net.Conn
	reader   io.Reader
	left     uint32
	eof      bool
	readErr  error
	mu       sync.Mutex
	ended    bool
	writeErr error
}

func NewTCPStream(c net.Conn, reader io.Reader) *TCPStream {
	return &TCPStream{Conn: c, reader: reader}
}

func (c *TCPStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.eof {
		return 0, io.EOF
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	if c.left == 0 {
		var header [4]byte
		if _, err := io.ReadFull(c.reader, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			c.readErr = err
			return 0, err
		}
		c.left = binary.BigEndian.Uint32(header[:])
		if c.left > maxTCPFrame {
			c.readErr = errors.New("oversize TCP frame")
			return 0, c.readErr
		}
		if c.left == 0 {
			c.eof = true
			return 0, io.EOF
		}
	}
	n, err := c.reader.Read(p[:min(len(p), int(c.left))])
	c.left -= uint32(n)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	c.readErr = err
	return n, err
}

func (c *TCPStream) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return 0, net.ErrClosed
	}
	total := 0
	for len(p) > 0 {
		n := min(len(p), maxTCPFrame)
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(n))
		if err := writeFrame(c.Conn, header[:]); err != nil {
			c.ended, c.writeErr = true, err
			return total, err
		}
		written, err := c.Conn.Write(p[:n])
		total += written
		if err == nil && written != n {
			err = io.ErrShortWrite
		}
		if err != nil {
			c.ended, c.writeErr = true, err
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

func writeFrame(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return err
}

func (c *TCPStream) CloseWrite() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return c.writeErr
	}
	c.ended = true
	c.writeErr = writeFrame(c.Conn, []byte{0, 0, 0, 0})
	return c.writeErr
}

// BridgeTCP waits for both directions to finish, including replies after EOF.
// It closes both connections on completion or error. Callers cancel a blocked
// bridge by closing either connection (no extra timeout or background retries).
func BridgeTCP(a, b net.Conn) {
	defer func() { _ = a.Close(); _ = b.Close() }()
	done := make(chan error, 2)
	copyDirection := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if half, ok := dst.(interface{ CloseWrite() error }); ok {
				err = half.CloseWrite()
			} else {
				err = errors.New("TCP bridge requires half-close")
			}
		}
		done <- err
	}
	go copyDirection(a, b)
	go copyDirection(b, a)
	if err := <-done; err != nil {
		_ = a.Close()
		_ = b.Close()
	}
	<-done
}
