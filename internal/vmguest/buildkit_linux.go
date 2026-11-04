//go:build linux

package vmguest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/storagegc"
	"golang.org/x/sys/unix"
)

const builderConfig = `root = "/var/lib/buildkit/default"
[worker.oci]
enabled = false
[worker.containerd]
enabled = true
namespace = "default"
`

// Configure retention before exposing the VM, without starting BuildKit.
// Each VM boot starts without a builder; it will load this policy on demand.
func configureBuilder() error {
	const dir = "/etc/buildkit"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	raw, err := safepath.ReadFile(dir, "buildkitd.toml", 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		raw = []byte(builderConfig)
	} else if err != nil {
		return err
	}
	version, err := exec.Command("buildkitd", "--version").Output()
	if err != nil {
		return err
	}
	disk, err := storagegc.RootDisk()
	if err != nil {
		return err
	}
	next, err := storagegc.BuildkitConfig(raw, string(version), disk)
	if err != nil {
		return err
	}
	next, err = resources.BuildkitWorkloads(next, false)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, next) {
		return nil
	}
	return safepath.WriteFile(dir, "buildkitd.toml", next, 0600)
}

type builderProcess struct {
	done chan struct{}
	err  error // read only after done closes
}

type lazyBuilder struct {
	sync.Mutex
	process *builderProcess
}

var builder lazyBuilder

func ensureBuilder() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return builder.ensure(ctx, func() (*exec.Cmd, error) {
		group := resources.CgroupPath(false) + "/builder"
		if err := prepareGroup(group); err != nil {
			return nil, err
		}
		fd, err := unix.Open(group, unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		defer func() { _ = unix.Close(fd) }()
		// Keep the default-namespace cache separate from legacy worker state.
		cmd := exec.Command("/usr/local/bin/buildkitd", "--config=/etc/buildkit/buildkitd.toml", "--root=/var/lib/buildkit/default", "--oci-worker=false", "--containerd-worker=true", "--containerd-worker-namespace=default")
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := startOwned(cmd); err != nil {
			return nil, err
		}
		return cmd, nil
	}, func(ctx context.Context) error {
		_, err := outputOwned(exec.CommandContext(ctx, "buildctl", "debug", "workers"))
		return err
	})
}

// An exited (including OOM-killed) builder is restarted only on the next
// explicit request. Concurrent requests share startup. Its exit must not call
// shutdown: the controller, native data and unrelated boxes remain available.
func (b *lazyBuilder) ensure(ctx context.Context, start func() (*exec.Cmd, error), probe func(context.Context) error) error {
	b.Lock()
	defer b.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.process != nil {
		select {
		case <-b.process.done:
			b.process = nil
		default:
		}
	}
	if b.process == nil {
		cmd, err := start()
		if err != nil {
			return err
		}
		p := &builderProcess{done: make(chan struct{})}
		b.process = p
		go func() {
			p.err = cmd.Wait()
			ownedChildren.Delete(cmd.Process.Pid)
			fmt.Fprintln(os.Stderr, "BuildKit exited; next build request may restart it:", p.err)
			close(p.done)
		}()
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-b.process.done:
			return fmt.Errorf("BuildKit exited during startup: %v", b.process.err)
		default:
		}
		if err := probe(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("BuildKit worker unavailable: %w", ctx.Err())
		case <-b.process.done:
			return fmt.Errorf("BuildKit exited during startup: %v", b.process.err)
		case <-tick.C:
		}
	}
}
