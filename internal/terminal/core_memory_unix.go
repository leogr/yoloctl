//go:build unix

package terminal

import (
	"context"
	"math"
	"os"

	"github.com/tetratelabs/wazero/experimental"
	"golang.org/x/sys/unix"
)

// Each core instance keeps its linear memory outside the Go heap, in an
// address reservation of its maximum size that uses no memory until the core
// grows into it. wazero's own growth appends a zeroed block and copies the
// whole memory whenever its capacity runs out, so a core that grows to 65 MiB
// allocates hundreds of MiB of Go heap on the way and leaves the collector
// that much headroom afterwards.
//
// This uses wazero's experimental allocator API (experimental.
// WithMemoryAllocator, MemoryAllocator and LinearMemory) at the pinned
// v1.12.0; third_party/README.md says what to check when upgrading. When the
// reservation or a later commit fails, the instance continues with today's
// heap growth.

// The system calls are seams for tests.
var (
	reserveCore = func(n int) ([]byte, error) {
		return unix.Mmap(-1, 0, n, unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON|unix.MAP_NORESERVE)
	}
	commitCore  = func(b []byte) error { return unix.Mprotect(b, unix.PROT_READ|unix.PROT_WRITE) }
	releaseCore = unix.Munmap
)

// withCoreMemory selects the allocator for one instantiation; a seam for
// tests that compare with wazero's own growth.
var withCoreMemory = func(ctx context.Context) context.Context {
	return experimental.WithMemoryAllocator(ctx, experimental.MemoryAllocatorFunc(newCoreMemory))
}

func newCoreMemory(capacity, max uint64) experimental.LinearMemory {
	if max == 0 || max > math.MaxInt {
		return &heapMemory{buf: make([]byte, 0, capacity)}
	}
	reserved, err := reserveCore(int(max))
	if err != nil {
		return &heapMemory{buf: make([]byte, 0, capacity)}
	}
	return &coreMemory{reserved: reserved}
}

// coreMemory backs the core with the committed start of its reservation.
// After a failed commit it moves the memory to the Go heap and grows there.
type coreMemory struct {
	reserved        []byte // nil once released
	committed, size int
	heap            heapMemory
}

func (m *coreMemory) Reallocate(size uint64) []byte {
	if m.reserved == nil {
		return m.heap.Reallocate(size)
	}
	if size > uint64(len(m.reserved)) {
		return nil // past the maximum: wazero refuses the growth
	}
	n := int(size)
	if n > m.committed {
		page := os.Getpagesize()
		end := min((n+page-1)/page*page, len(m.reserved))
		if err := commitCore(m.reserved[m.committed:end]); err != nil {
			// wazero assigns the returned slice before the core runs
			// again, and every reader copies, so the reservation can go.
			m.heap.buf = append(make([]byte, 0, n), m.reserved[:m.size]...)
			_ = releaseCore(m.reserved)
			m.reserved = nil
			return m.heap.Reallocate(size)
		}
		m.committed = end
	}
	m.size = n
	// The capacity ends at the length: nothing can extend this slice into
	// pages that are not committed.
	return m.reserved[:n:n]
}

func (m *coreMemory) Free() {
	if m.reserved != nil {
		_ = releaseCore(m.reserved)
		m.reserved = nil
	}
	m.heap.Free()
}

// heapMemory is wazero's own growth: append a zeroed block.
type heapMemory struct{ buf []byte }

func (m *heapMemory) Reallocate(size uint64) []byte {
	if n := int(size) - len(m.buf); n > 0 {
		m.buf = append(m.buf, make([]byte, n)...)
	}
	return m.buf[:size]
}

func (m *heapMemory) Free() { m.buf = nil }
