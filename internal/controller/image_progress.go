package controller

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

const imageProgressLimit = 1 << 20
const imageProgressChunk = 32 << 10
const imageProgressInterval = 2 * time.Second

// imageProgress is one request's bounded stderr queue and sole progress sender.
// Producers never perform network I/O. Its context is the public connection's
// lifetime, whose cancellation closes the connection and unblocks a writer.
type imageProgress struct {
	mu       sync.Mutex
	chunks   [][]byte
	bytes    int
	skipped  int
	finished bool
	phase    string
	started  time.Time
	wake     chan struct{}
	done     chan struct{}
}

func newImageProgress(ctx context.Context, out io.Writer) *imageProgress {
	p := &imageProgress{wake: make(chan struct{}, 1), done: make(chan struct{}), started: time.Now()}
	go p.run(ctx, out)
	return p
}

func (p *imageProgress) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *imageProgress) Write(data []byte) (int, error) {
	total := len(data)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return total, nil
	}
	// Copy only the retained tail, even for an unusually large producer write.
	if len(data) > imageProgressLimit {
		p.skipped += len(data) - imageProgressLimit
		data = data[len(data)-imageProgressLimit:]
	}
	for p.bytes+len(data) > imageProgressLimit {
		p.skipped += len(p.chunks[0])
		p.bytes -= len(p.chunks[0])
		p.chunks[0] = nil
		p.chunks = p.chunks[1:]
	}
	for len(data) > 0 {
		n := min(len(data), imageProgressChunk)
		if len(p.chunks) > 0 && len(p.chunks[len(p.chunks)-1]) < imageProgressChunk {
			last := len(p.chunks) - 1
			n = min(len(data), imageProgressChunk-len(p.chunks[last]))
			p.chunks[last] = append(p.chunks[last], data[:n]...)
		} else {
			p.chunks = append(p.chunks, append([]byte(nil), data[:n]...))
		}
		p.bytes += n
		data = data[n:]
	}
	p.notify()
	return total, nil
}
func (p *imageProgress) preparing(description string) {
	p.mu.Lock()
	p.phase = description
	p.mu.Unlock()
	_, _ = fmt.Fprintf(p, "Preparing image %s\n", description)
}
func (p *imageProgress) idle(now, last time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished || p.phase == "" || now.Sub(last) < imageProgressInterval {
		return ""
	}
	return fmt.Sprintf("Still preparing image %s (%s elapsed)\n", p.phase, now.Sub(p.started).Round(time.Second))
}
func (p *imageProgress) next() ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.skipped > 0 {
		n := p.skipped
		p.skipped = 0
		return fmt.Appendf(nil, "[image progress: skipped %d bytes for this slow client]\n", n), false
	}
	if len(p.chunks) > 0 {
		data := p.chunks[0]
		p.chunks[0] = nil
		p.chunks = p.chunks[1:]
		p.bytes -= len(data)
		return data, false
	}
	return nil, p.finished
}
func (p *imageProgress) run(ctx context.Context, out io.Writer) {
	defer func() {
		p.mu.Lock()
		p.finished = true
		p.chunks = nil
		p.bytes = 0
		p.mu.Unlock()
		close(p.done)
	}()
	timer := time.NewTimer(imageProgressInterval)
	defer timer.Stop()
	last := time.Now()
	for {
		if ctx.Err() != nil {
			return
		}
		data, finished := p.next()
		if len(data) > 0 {
			if _, err := out.Write(data); err != nil {
				return
			}
			last = time.Now()
			timer.Reset(imageProgressInterval)
			continue
		}
		if finished {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case now := <-timer.C:
			if text := p.idle(now, last); text != "" {
				if _, err := io.WriteString(out, text); err != nil {
					return
				}
				last = now
			}
			timer.Reset(imageProgressInterval)
		}
	}
}
func (p *imageProgress) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
	p.notify()
	<-p.done
}

// imageBuildLog retains one ordered job tail. Registering a follower and its
// snapshot is atomic with publication; no client I/O happens under this lock.
// Each follower feeds its own independent bounded request queue.
type imageBuildLog struct {
	mu          sync.Mutex
	tail        []byte
	omitted     int
	phase       string
	subscribers map[*imageProgress]struct{}
}

func (l *imageBuildLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := len(data)
	for p := range l.subscribers {
		_, _ = p.Write(data)
	}
	if len(data) >= imageProgressLimit {
		l.omitted += len(l.tail) + len(data) - imageProgressLimit
		l.tail = append(l.tail[:0], data[len(data)-imageProgressLimit:]...)
	} else {
		if n := len(l.tail) + len(data) - imageProgressLimit; n > 0 {
			l.omitted += n
			copy(l.tail, l.tail[n:])
			l.tail = l.tail[:len(l.tail)-n]
		}
		l.tail = append(l.tail, data...)
	}
	return total, nil
}
func (l *imageBuildLog) setPhase(phase string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.phase = phase
	for p := range l.subscribers {
		_, _ = fmt.Fprintln(p, phase)
	}
}
func (l *imageBuildLog) subscribe(p *imageProgress) func() {
	if p == nil {
		return func() {}
	}
	l.mu.Lock()
	if l.subscribers == nil {
		l.subscribers = make(map[*imageProgress]struct{})
	}
	if l.phase != "" {
		_, _ = fmt.Fprintln(p, l.phase)
	}
	if l.omitted > 0 {
		_, _ = fmt.Fprintf(p, "[image progress: %d earlier bytes omitted]\n", l.omitted)
	}
	_, _ = p.Write(l.tail)
	l.subscribers[p] = struct{}{}
	l.mu.Unlock()
	return func() { l.mu.Lock(); delete(l.subscribers, p); l.mu.Unlock() }
}

type imageBuildLogKey struct{}

func imageBuildOutput(ctx context.Context) io.Writer {
	if l, ok := ctx.Value(imageBuildLogKey{}).(*imageBuildLog); ok {
		return l
	}
	return io.Discard
}
