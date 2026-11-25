//go:build unix

package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

// coreStream fills a 50×160 terminal's history with styled lines, so its core
// grows its linear memory many times.
func coreStream(lines int) []byte {
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "\x1b[3%dm%06d \x1b[1m%s\x1b[0m %s\r\n", i%8, i, strings.Repeat("·", i%50), strings.Repeat("x", 90))
	}
	return []byte(b.String())
}

// wazeroGrowth builds one terminal with wazero's own memory growth: the
// control every allocator path is compared with.
func wazeroGrowth(t *testing.T, rows, cols, history int) *Terminal {
	t.Helper()
	old := withCoreMemory
	withCoreMemory = func(ctx context.Context) context.Context { return ctx }
	defer func() { withCoreMemory = old }()
	return newTestTerminal(t, rows, cols, history, nil)
}

type coreCalls struct{ reserves, commits, releases int }

// countCore wraps the system calls; failCommit makes the commit with that
// 1-based index fail, and later ones too (0: none).
func countCore(t *testing.T, failReserve bool, failCommit int) *coreCalls {
	t.Helper()
	calls := &coreCalls{}
	reserve, commit, release := reserveCore, commitCore, releaseCore
	t.Cleanup(func() { reserveCore, commitCore, releaseCore = reserve, commit, release })
	reserveCore = func(n int) ([]byte, error) {
		calls.reserves++
		if failReserve {
			return nil, errors.New("no address space")
		}
		return reserve(n)
	}
	commitCore = func(b []byte) error {
		calls.commits++
		if failCommit > 0 && calls.commits >= failCommit {
			return errors.New("no memory")
		}
		return commit(b)
	}
	releaseCore = func(b []byte) error {
		calls.releases++
		return release(b)
	}
	return calls
}

func writeBoth(t *testing.T, want, got *Terminal, p []byte) {
	t.Helper()
	for _, vt := range []*Terminal{want, got} {
		if _, err := vt.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	assertSameTerminal(t, want, got)
}

func sameSnapshots(t *testing.T, want, got *Terminal) {
	t.Helper()
	if a, b := captureTestSnapshot(t, want), captureTestSnapshot(t, got); !bytes.Equal(a, b) {
		t.Fatal("snapshots differ from wazero's own growth")
	}
}

func TestCoreMemoryMatchesWazeroGrowth(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failReserve bool
		failCommit  int
		reserved    bool // the reservation still backs the memory at the end
	}{
		{"reserved", false, 0, true},
		{"reservation fails", true, 0, false},
		{"first commit fails", false, 1, false},
		{"commit fails mid-session", false, 13, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := wazeroGrowth(t, 50, 160, 10_000)
			calls := countCore(t, tc.failReserve, tc.failCommit)
			got := newTestTerminal(t, 50, 160, 10_000, nil)
			if calls.reserves != 1 {
				t.Fatalf("reserves = %d, want one per instance", calls.reserves)
			}
			stream := coreStream(2000)
			for len(stream) > 0 {
				n := min(len(stream), 64<<10)
				writeBoth(t, want, got, stream[:n])
				stream = stream[n:]
			}
			sameSnapshots(t, want, got)
			if tc.failCommit > 0 && calls.commits < tc.failCommit {
				t.Fatalf("only %d commits: the stream did not reach the failing one", calls.commits)
			}
			if !tc.failReserve && tc.failCommit == 0 && calls.commits < 13 {
				t.Fatalf("commits = %d: the core did not grow", calls.commits)
			}
			wantReleases := 0
			if !tc.failReserve && !tc.reserved {
				wantReleases = 1 // the failed commit released the reservation
			}
			if calls.releases != wantReleases {
				t.Fatalf("releases before close = %d, want %d", calls.releases, wantReleases)
			}
			snapshot, wantSnapshot := captureTestSnapshot(t, got), captureTestSnapshot(t, want)
			if err := got.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.reserved {
				wantReleases = 1
			}
			if calls.releases != wantReleases {
				t.Fatalf("releases after close = %d, want %d", calls.releases, wantReleases)
			}
			// A restore gets its own instance and memory on the same path;
			// compare it with a restore on wazero's own growth.
			old := withCoreMemory
			withCoreMemory = func(ctx context.Context) context.Context { return ctx }
			wantRestored := restoreTestSnapshot(t, wantSnapshot, nil)
			withCoreMemory = old
			restored := restoreTestSnapshot(t, snapshot, nil)
			writeBoth(t, wantRestored, restored, []byte("\x1b[31mafter restore\r\n"))
			sameSnapshots(t, wantRestored, restored)
		})
	}
}

func TestCoreMemoryGrowsInPlace(t *testing.T) {
	calls := countCore(t, false, 0)
	m := newCoreMemory(0, 4<<20)
	defer m.Free()
	a := m.Reallocate(64 << 10)
	if len(a) != 64<<10 || cap(a) != len(a) {
		t.Fatalf("len %d cap %d: the capacity must end at the committed length", len(a), cap(a))
	}
	a[0], a[len(a)-1] = 7, 9
	b := m.Reallocate(3 << 20)
	if unsafe.SliceData(a) != unsafe.SliceData(b) || b[0] != 7 || b[64<<10-1] != 9 || b[len(b)-1] != 0 || cap(b) != len(b) {
		t.Fatal("growth moved, lost or did not zero the memory")
	}
	if m.Reallocate(4<<20+1) != nil {
		t.Fatal("growth past the maximum must be refused")
	}
	if calls.commits != 2 {
		t.Fatalf("commits = %d, want one per growth", calls.commits)
	}
	// A failed commit keeps the contents, now on the Go heap.
	commitCore = func([]byte) error { return errors.New("no memory") }
	c := m.Reallocate(4 << 20)
	if unsafe.SliceData(c) == unsafe.SliceData(b) || len(c) != 4<<20 || c[0] != 7 || c[64<<10-1] != 9 || c[len(c)-1] != 0 || calls.releases != 1 {
		t.Fatal("the heap fallback lost the contents or kept the reservation")
	}
	m.Free()
	if calls.releases != 1 {
		t.Fatal("the reservation was released twice")
	}
}
