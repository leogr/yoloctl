//go:build linux && amd64

package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/vmapi"
	"golang.org/x/sys/unix"
)

// These are host headroom thresholds, not reservations or guest disk limits.
// Hysteresis avoids repeatedly resuming writers at the pause threshold.
const (
	hostStorageStartFree = vmapi.HostStorageStartFree
	hostStoragePauseFree = uint64(2 << 30)
	hostStorageSocket    = "storage.sock"
)

type backingStorage struct {
	files  []*os.File
	sample func(*os.File) (uint64, error)
}

// Pin the actual backing files (or their creation directories). Measuring the
// project share or guest filesystem says nothing about raw-disk host capacity.
func openBackingStorage(paths ...string) (*backingStorage, error) {
	s := &backingStorage{sample: hostAvailable}
	for _, path := range paths {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("inspect host backing storage %q: %w", path, err)
		}
		f := os.NewFile(uintptr(fd), path)
		s.files = append(s.files, f)
		st, err := f.Stat()
		if err != nil || (!st.Mode().IsRegular() && !st.IsDir()) {
			s.close()
			return nil, fmt.Errorf("host backing storage %q must be a regular file or directory: %w", path, errors.Join(err, os.ErrInvalid))
		}
	}
	return s, nil
}

func (s *backingStorage) close() {
	for _, f := range s.files {
		_ = f.Close()
	}
}

func hostAvailable(f *os.File) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	// Use space available to the invoking user, excluding filesystem reserves.
	if st.Bsize <= 0 || st.Bavail > ^uint64(0)/uint64(st.Bsize) {
		return 0, errors.New("invalid host filesystem capacity")
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func (s *backingStorage) require(minimum uint64) error {
	for _, f := range s.files {
		available, err := s.sample(f)
		if err != nil {
			return fmt.Errorf("cannot measure host filesystem backing %q: %w", f.Name(), err)
		}
		if available < minimum {
			return fmt.Errorf("host filesystem backing %q has %.1f GiB available; need %.1f GiB; free host storage (guest free space does not count)", f.Name(), float64(available)/(1<<30), float64(minimum)/(1<<30))
		}
	}
	return nil
}

func requireHostStorage(paths ...string) error {
	return requireHostStorageFor(0, paths...)
}

// requireHostStorageFor requires the start headroom plus format bytes that a
// create is about to allocate, so formatting cannot by itself bring the host
// to the pause threshold.
func requireHostStorageFor(format uint64, paths ...string) error {
	s, err := openBackingStorage(paths...)
	if err != nil {
		return err
	}
	defer s.close()
	return s.require(hostStorageStartFree + format)
}

type hostStorageState struct {
	Paused bool   `json:"paused"`
	Reason string `json:"reason,omitempty"`
}

func (s hostStorageState) err() error {
	if !s.Paused {
		return nil
	}
	return fmt.Errorf("%w: %s; the VM resumes automatically when both backing filesystems have at least 4 GiB available; do not force-stop it", vmapi.ErrHostStoragePaused, s.Reason)
}

type hostStorageGuard struct {
	check  func(uint64) error
	signal func(unix.Signal) error
	log    *slog.Logger
	mu     sync.Mutex
	state  hostStorageState
}

func (g *hostStorageGuard) snapshot() hostStorageState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// One monitor owns transitions. Stop the whole VMM, including synchronous disk
// IO, not just its vCPUs. RAM and pending guest writes survive. This is a sampled
// guard, not a guarantee against faster external consumption, quotas or EIO.
func (g *hostStorageGuard) step() error {
	state := g.snapshot()
	minimum := hostStoragePauseFree
	if state.Paused {
		minimum = hostStorageStartFree
	}
	err := g.check(minimum)
	if err != nil {
		if !state.Paused {
			if signalErr := g.signal(unix.SIGSTOP); signalErr != nil {
				return fmt.Errorf("pause VM for host storage pressure: %w", errors.Join(err, signalErr))
			}
		}
		next := hostStorageState{Paused: true, Reason: err.Error()}
		g.mu.Lock()
		g.state = next
		g.mu.Unlock()
		if !state.Paused {
			g.log.Warn("VM paused for host storage pressure", "err", next.err())
		}
		return nil
	}
	if state.Paused {
		if err := g.signal(unix.SIGCONT); err != nil {
			return fmt.Errorf("resume VM after host storage recovery: %w", err)
		}
		g.mu.Lock()
		g.state = hostStorageState{}
		g.mu.Unlock()
		g.log.Info("Host storage recovered: resumed VM; interrupted operations are not replayed")
	}
	return nil
}

func (g *hostStorageGuard) watch(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := g.step(); err != nil {
			g.log.Warn("Host storage guard", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Read-only, helper-owned status: no requests, guest access or host executor.
// Keeping it in memory also works when publishing another host file fails.
func (g *hostStorageGuard) serve(ctx context.Context, runtime string) error {
	ln, err := net.Listen("unix", filepath.Join(runtime, hostStorageSocket))
	if err != nil {
		return err
	}
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			_ = json.NewEncoder(c).Encode(g.snapshot())
			_ = c.Close()
		}
	}()
	return nil
}

func readHostStorage(ctx context.Context, runtime string) (hostStorageState, error) {
	var state hostStorageState
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runtime, hostStorageSocket))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil // Older running helpers acquire the guard on VM restart.
	}
	if err != nil {
		return state, fmt.Errorf("read host storage guard: %w", err)
	}
	defer func() { _ = c.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	raw, err := io.ReadAll(io.LimitReader(c, 16385))
	if err != nil {
		return state, err
	}
	if len(raw) > 16384 {
		return state, errors.New("host storage status too large")
	}
	var wire struct {
		Paused *bool  `json:"paused"`
		Reason string `json:"reason,omitempty"`
	}
	if err := strictJSON(raw, &wire); err != nil || wire.Paused == nil {
		return state, errors.New("invalid host storage status")
	}
	state = hostStorageState{Paused: *wire.Paused, Reason: wire.Reason}
	if (state.Paused && state.Reason == "") || (!state.Paused && state.Reason != "") {
		return state, errors.New("invalid host storage status")
	}
	return state, nil
}

func checkHostStoragePause(ctx context.Context, s runtimeState) error {
	live, err := s.Helper.alive()
	if err != nil || !live {
		return err
	}
	state, err := readHostStorage(ctx, s.Dir)
	if err != nil {
		return err
	}
	return state.err()
}
