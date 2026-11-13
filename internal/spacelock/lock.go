// Package spacelock serializes destructive space operations against clients.
package spacelock

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
)

func Acquire(ctx context.Context, space string, exclusive bool) (func(), error) {
	return AcquireWithWait(ctx, space, exclusive, nil)
}

// ErrBusy is TryAcquire's answer when another holder has the lock.
var ErrBusy = errors.New("space lock is held by another operation")

// TryAcquire takes the lock once without waiting. Only real contention
// returns ErrBusy; any other failure is returned as it is.
func TryAcquire(ctx context.Context, space string, exclusive bool) (func(), error) {
	return lock(ctx, space, exclusive, nil, true, true)
}

// AcquireWithWait reports prolonged contention once and remains cancellable.
func AcquireWithWait(ctx context.Context, space string, exclusive bool, waiting func()) (func(), error) {
	return lock(ctx, space, exclusive, waiting, true, false)
}

// AcquireExistingWithWait never creates storage: it uses the original stable
// lock and fails when it is absent.
func AcquireExistingWithWait(ctx context.Context, space string, exclusive bool, waiting func()) (func(), error) {
	return lock(ctx, space, exclusive, waiting, false, false)
}

func lock(ctx context.Context, space string, exclusive bool, waiting func(), create, try bool) (func(), error) {
	path, err := layout.SpaceLockPath(space)
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	flags := syscall.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if create {
		flags |= syscall.O_CREAT
	}
	fd, err := syscall.Open(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	op := syscall.LOCK_SH
	if exclusive {
		op = syscall.LOCK_EX
	}
	started := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			_ = syscall.Close(fd)
			return nil, err
		}
		err = syscall.Flock(fd, op|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = syscall.Close(fd)
			return nil, err
		}
		if try {
			_ = syscall.Close(fd)
			return nil, ErrBusy
		}
		if waiting != nil && time.Since(started) >= time.Second {
			waiting()
			waiting = nil
		}
		select {
		case <-ctx.Done():
			_ = syscall.Close(fd)
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	event := Event{Space: space, Token: rand.Text(), Exclusive: exclusive, Held: true}
	observe(ctx, event)
	var once sync.Once
	return func() {
		once.Do(func() {
			event.Held = false
			observe(ctx, event)
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = syscall.Close(fd)
		})
	}, nil
}
