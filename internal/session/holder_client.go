package session

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
)

const (
	clientWriteTimeout     = 5 * time.Second
	maxClientOutputBytes   = 4 << 20
	maxClientOutputPackets = 4096
)

// holderClient has one writer, independent of the holder's output/state lock.
// Its bootstrap is a private replay snapshot; the live queue is bounded in both
// bytes and packets, including the write in flight. Exceeding either bound
// disconnects this client, never silently drops part of the terminal stream.
type holderClient struct {
	conn      net.Conn
	managed   bool
	admitted  bool
	bootstrap []frame
	colors    *terminal.Colors
	browser   bool
	activity  uint64 // protected by holder.mu
	accepted  uint64 // input payload bytes queued for the agent; protected by holder.mu
	id        string // ephemeral attachment identity, assigned before admission
	ctx       context.Context
	cancel    context.CancelFunc

	mu           sync.Mutex
	packets      []frame
	bytes, count int
	closed       bool
	finishing    bool
	exitPayload  []byte
	drainBy      time.Time
	ready        chan struct{}
	done         chan struct{} // closed by the writer after holder cleanup
}

func newHolderClient(conn net.Conn) *holderClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &holderClient{conn: conn, ctx: ctx, cancel: cancel, ready: make(chan struct{}, 1), done: make(chan struct{})}
}

// enqueue borrows immutable bytes owned by broadcast, shared by all clients.
// It never waits for a socket write. The caller must abort on rejection.
func (c *holderClient) enqueue(p []byte) bool {
	return c.enqueueFrame(frameData, p)
}

func (c *holderClient) enqueueFrame(kind byte, p []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.finishing {
		return false
	}
	if len(p) == 0 {
		return true
	}
	if len(p) > maxClientOutputBytes-c.bytes || c.count == maxClientOutputPackets {
		return false
	}
	c.packets = append(c.packets, frame{kind, p})
	c.bytes += len(p)
	c.count++
	c.notify()
	return true
}

func (c *holderClient) notify() {
	select {
	case c.ready <- struct{}{}:
	default:
	}
}

// finish drains accepted output before EXIT (or a statusless close). All
// clients receive the same deadline, so shutdown costs one budget, not one
// timeout per client. Setting it here also bounds a bootstrap/write in flight.
func (c *holderClient) finish(exitPayload []byte, deadline time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.finishing {
		return
	}
	c.finishing = true
	c.exitPayload = exitPayload
	c.drainBy = deadline
	_ = c.conn.SetWriteDeadline(deadline)
	c.notify()
}

func (c *holderClient) abort() {
	c.cancel()
	c.mu.Lock()
	c.closed = true
	c.packets = nil
	c.bytes, c.count = 0, 0
	c.mu.Unlock()
	// net.Conn.Close interrupts a Write in flight; never wait on c.done here
	// because abort is also called by the writer itself.
	_ = c.conn.Close()
	c.notify()
}

func (c *holderClient) writeDeadline() {
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := time.Now().Add(clientWriteTimeout)
	if !c.drainBy.IsZero() && c.drainBy.Before(deadline) {
		deadline = c.drainBy
	}
	// Serialize with finish: a writer must not extend the shutdown deadline.
	_ = c.conn.SetWriteDeadline(deadline)
}

// writeLoop owns all socket writes: HELLO, replay, ordered live DATA, EXIT.
// The replay is already captured and the client registered before this starts.
func (c *holderClient) writeLoop(snapshot []byte) error {
	defer c.abort()
	c.writeDeadline()
	if !c.admitted {
		if err := writeFrame(c.conn, frameHello, []byte{protocolVersion}); err != nil {
			return err
		}
	}
	for _, f := range c.bootstrap {
		if err := writeFrame(c.conn, f.Type, f.Payload); err != nil {
			return err
		}
	}
	c.bootstrap = nil
	if err := writeDataFrames(c.conn, snapshot); err != nil {
		return err
	}
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil
		}
		if len(c.packets) == 0 {
			finishing, exitPayload := c.finishing, c.exitPayload
			c.mu.Unlock()
			if finishing {
				if exitPayload != nil {
					c.writeDeadline()
					return writeFrame(c.conn, frameExit, exitPayload)
				}
				return nil
			}
			<-c.ready
			continue
		}
		p := c.packets[0]
		c.packets[0] = frame{}
		c.packets = c.packets[1:]
		c.mu.Unlock()

		c.writeDeadline()
		if err := writeFrameChunks(c.conn, p.Type, p.Payload); err != nil {
			return err
		}
		c.mu.Lock()
		if !c.closed {
			c.bytes -= len(p.Payload)
			c.count--
		}
		c.mu.Unlock()
	}
}
