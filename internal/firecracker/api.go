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

func API(ctx context.Context, path, method, route string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer t.CloseIdleConnections()
	c := http.Client{Transport: t, Timeout: 30 * time.Second}
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
	until := time.Now().Add(5 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(until) {
		until = limit
	}
	_ = c.SetDeadline(until)
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if _, e = fmt.Fprintf(c, "CONNECT %d\n", port); e != nil {
		_ = c.Close()
		return nil, e
	}
	// Read exactly through the newline: do not discard SSH greeting bytes that
	// can arrive in the same Unix stream read as Firecracker's acknowledgment.
	r := bufio.NewReaderSize(c, 128)
	raw, e := r.ReadSlice('\n')
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
