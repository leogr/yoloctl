package term

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	maxInputBytes   = 4 << 20
	maxInputPackets = 4096
)

var errInputFull = errors.New("terminal input buffer is full; this input was not queued")

// inputQueue is the sole writer to a live PTY. Enqueuing is all-or-nothing and
// never waits for the child to read. Both bytes and packet count are bounded,
// including the packet in flight. The UI must surface rejection and write errors;
// queued input is never retried after an uncertain partial write.
type inputQueue struct {
	mu      sync.Mutex
	target  io.WriteCloser // Close must interrupt Write (see startPTY)
	packets []inputPacket
	bytes   int
	count   int
	closed  bool
	failed  error
	notice  error
	wake    func()
	ready   chan struct{}
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

type inputPacket struct {
	data    []byte
	budget  int
	prepare func(context.Context) ([]byte, error)
}

func newInputQueue(target io.WriteCloser, wake func()) *inputQueue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &inputQueue{target: target, wake: wake, ready: make(chan struct{}, 1), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go q.run()
	return q
}

func (q *inputQueue) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	err := q.enqueue(inputPacket{data: append([]byte(nil), p...), budget: len(p)})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Transform reserves one position in the same queue as keystrokes, paste and
// replies. A clipboard read/transfer can wait here without blocking rendering
// or allowing subsequently typed text to overtake the image path.
func (q *inputQueue) Transform(prepare func(context.Context) ([]byte, error)) error {
	// Enough for a 4096-byte path with shell quoting and paste delimiters.
	return q.enqueue(inputPacket{budget: 32 << 10, prepare: prepare})
}

func (q *inputQueue) enqueue(packet inputPacket) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return io.ErrClosedPipe
	}
	if q.failed != nil {
		err := q.failed
		q.mu.Unlock()
		return err
	}
	if packet.budget > maxInputBytes-q.bytes || q.count == maxInputPackets {
		q.notice = errInputFull
		q.mu.Unlock()
		q.notify()
		return errInputFull
	}
	q.packets = append(q.packets, packet)
	q.bytes += packet.budget
	q.count++
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
	return nil
}

func (q *inputQueue) run() {
	defer close(q.done)
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return
		}
		if len(q.packets) == 0 {
			q.mu.Unlock()
			<-q.ready
			continue
		}
		p := q.packets[0]
		q.packets[0] = inputPacket{}
		q.packets = q.packets[1:]
		q.mu.Unlock()

		data := p.data
		var prepareErr error
		if p.prepare != nil {
			data, prepareErr = p.prepare(q.ctx)
			if len(data) > p.budget {
				prepareErr = errors.New("prepared terminal input exceeds limit")
			}
		}
		var err error
		if prepareErr == nil {
			err = writeInput(q.target, data)
		}
		q.mu.Lock()
		q.bytes -= p.budget
		q.count--
		if prepareErr != nil && !q.closed {
			q.notice = prepareErr
		}
		if err != nil && !q.closed {
			q.failed = fmt.Errorf("terminal input stopped; some queued input may not have reached the process: %w", err)
			q.notice = q.failed
			q.packets = nil
			q.bytes, q.count = 0, 0
		}
		q.mu.Unlock()
		if prepareErr != nil {
			q.notify()
		}
		if err != nil {
			q.notify()
			return
		}
	}
}

func writeInput(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func (q *inputQueue) notify() {
	if q.wake != nil {
		q.wake() // the PTY onData callback is nonblocking
	}
}

func (q *inputQueue) takeError() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	err := q.notice
	q.notice = nil
	return err
}

func (q *inputQueue) Close() {
	q.cancel()
	q.mu.Lock()
	q.closed = true
	q.packets = nil
	q.mu.Unlock()
	_ = q.target.Close()
	select {
	case q.ready <- struct{}{}:
	default:
	}
	<-q.done
}
