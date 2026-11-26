package firecracker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const bootAPIWait = 2 * time.Minute

func API(ctx context.Context, path, method, route string, value any) error {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer t.CloseIdleConnections()
	return apiRequest(ctx, t, method, route, value)
}

func apiRequest(ctx context.Context, transport http.RoundTripper, method, route string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, bootAPIWait)
	defer cancel()
	c := http.Client{Transport: transport}
	req, e := http.NewRequestWithContext(ctx, method, "http://firecracker"+route, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	r, e := c.Do(req)
	if e != nil {
		return e
	}
	defer func() { _ = r.Body.Close() }()
	b, e := io.ReadAll(io.LimitReader(r.Body, 65536))
	if e != nil {
		return e
	}
	if r.StatusCode != 204 {
		return fmt.Errorf("firecracker %s %s: %s: %s", method, route, r.Status, b)
	}
	return nil
}
func DialGuest(ctx context.Context, path string, port uint32) (net.Conn, error) {
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if e != nil {
		return nil, e
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if _, e = fmt.Fprintf(c, "CONNECT %d\n", port); e != nil {
		_ = c.Close()
		if cause := ctx.Err(); cause != nil {
			return nil, cause
		}
		return nil, e
	}
	// Read exactly through the newline: do not discard SSH greeting bytes that
	// can arrive in the same Unix stream read as Firecracker's acknowledgment.
	r := bufio.NewReaderSize(c, 128)
	raw, e := r.ReadSlice('\n')
	if cause := ctx.Err(); cause != nil {
		_ = c.Close()
		return nil, cause
	}
	line := string(raw)
	if e != nil || len(line) > 128 || len(line) < 4 || line[:3] != "OK " {
		_ = c.Close()
		return nil, fmt.Errorf("vsock connect: %q: %v", line, e)
	}
	_ = c.SetDeadline(time.Time{})
	return &bufferedConn{Conn: c, reader: r}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c *bufferedConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return nil
}
