package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/storagegc"
)

func configureBuildkitAt(ctx context.Context, dir, unitsDir string, totalMemory uint64, run func(context.Context, ...string) ([]byte, error)) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	raw, err := safepath.ReadFile(dir, "buildkitd.toml", 1<<20)
	if err != nil {
		return fmt.Errorf("read Lima BuildKit configuration: %w", err)
	}
	version, err := run(ctx, "buildkitd", "--version")
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
	next, err = resources.BuildkitWorkloads(next, true)
	if err != nil {
		return err
	}
	budget, err := resources.Workloads(totalMemory)
	if err != nil {
		return err
	}
	files := []buildkitPolicyFile{
		{root: dir, name: "buildkitd.toml", next: next},
		{root: unitsDir, name: resources.WorkloadSlice, next: budget.SliceUnit()},
		{root: unitsDir, name: "buildkit.service.d/50-yoloctl-workloads.conf", next: []byte(resources.BuilderDropIn)},
	}
	unchanged := true
	hash := sha256.New()
	for i := range files {
		f := &files[i]
		f.old, err = safepath.ReadFile(f.root, f.name, 1<<20)
		f.existed = err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		unchanged = unchanged && f.existed && bytes.Equal(f.old, f.next)
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", f.name, len(f.next))
		_, _ = hash.Write(f.next)
	}
	digest := []byte(fmt.Sprintf("%x", hash.Sum(nil)))
	applied, err := safepath.ReadFile(dir, ".yoloctl-gc-applied", 128)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if unchanged && bytes.Equal(applied, digest) {
		return nil
	}
	restart := func(ctx context.Context, activate bool) error {
		if _, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		if activate {
			if _, err := run(ctx, "systemctl", "start", resources.WorkloadSlice); err != nil {
				return err
			}
			raw, err := run(ctx, append([]string{"cat"}, resources.PoolFiles(true)...)...)
			if err != nil {
				return err
			}
			if err := resources.ValidatePool(string(raw)); err != nil {
				return err
			}
		}
		if _, err := run(ctx, "systemctl", "restart", "buildkit.service"); err != nil {
			return err
		}
		// Lima's BuildKit unit uses Type=notify: restart waits for readiness.
		// This separate bounded probe verifies that a worker is reachable.
		_, err := run(ctx, "buildctl", "debug", "workers")
		return err
	}
	activate := func() error {
		for _, f := range files {
			// Private mode also preserves registry-setting confidentiality.
			if err := safepath.WriteFile(f.root, f.name, f.next, 0600); err != nil {
				return err
			}
		}
		return restart(ctx, true)
	}
	if err := activate(); err != nil {
		// A failed deployment must not leave a broken BuildKit configuration.
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var restoreErr error
		for _, f := range files {
			if f.existed {
				restoreErr = errors.Join(restoreErr, safepath.WriteFile(f.root, f.name, f.old, 0600))
			} else if err := safepath.RemoveFile(f.root, f.name); !errors.Is(err, os.ErrNotExist) {
				restoreErr = errors.Join(restoreErr, err)
			}
		}
		if restoreErr == nil {
			restoreErr = restart(rollback, false)
		}
		return errors.Join(fmt.Errorf("activate BuildKit storage/memory policy: %w", err), restoreErr)
	}
	// Publication alone does not prove a live daemon loaded these settings.
	// An interrupted write/restart sequence must be retried on the next start.
	return safepath.WriteFile(dir, ".yoloctl-gc-applied", digest, 0o600)
}

type buildkitPolicyFile struct {
	root, name string
	old, next  []byte
	existed    bool
}
