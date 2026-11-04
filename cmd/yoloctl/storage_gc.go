package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/storagegc"
)

type gcImageClient interface {
	ImagesList(context.Context, string) ([]container.Image, error)
	InspectImages(context.Context, []string) ([]container.ImageDetails, error)
	List(context.Context, string) ([]container.Container, error)
	Inspect(context.Context, string) (*container.Details, error)
	SnapshotParent(context.Context, string) (string, error)
	ImageRemove(context.Context, string) error
}

func declaredImageReferences(space string) ([]string, error) {
	names, err := agentbox.ListNames(space)
	if err != nil {
		return nil, err
	}
	refs := []string{agentboximage.RefAgentbox}
	for _, name := range names {
		cfg, err := agentbox.Load(space, name)
		if err != nil {
			return nil, fmt.Errorf("load box %s: %w", name, err)
		}
		resolved, _, err := agentbox.ResolvedImage(cfg)
		if err != nil {
			return nil, fmt.Errorf("resolve box %s: %w", name, err)
		}
		refs = append(refs, cfg.Image, resolved)
	}
	return refs, nil
}

// collectImages obtains every declaration and container identity before any
// removal. Callers hold the native space lock, including manual image prune.
func collectImages(ctx context.Context, c gcImageClient, refs []string, out io.Writer, dryRun bool) (int, error) {
	boxes, err := c.List(ctx, "") // includes stopped and non-yoloctl containers
	if err != nil {
		return 0, fmt.Errorf("inventory containers: %w", err)
	}
	parents := map[string]bool{}
	for _, box := range boxes {
		if box.Name == "" {
			return 0, fmt.Errorf("container inventory has an unnamed entry (ID %q)", box.ID)
		}
		details, err := c.Inspect(ctx, box.Name)
		if err != nil {
			return 0, err
		}
		if details == nil || details.Image == "" {
			return 0, fmt.Errorf("cannot identify image used by container %s", box.Name)
		}
		refs = append(refs, details.Image)
		if storagegc.IsDigestID(details.ImageID) {
			refs = append(refs, details.ImageID)
		} else {
			parent, err := c.SnapshotParent(ctx, box.Name)
			if err != nil {
				return 0, fmt.Errorf("inspect snapshot for container %s: %w", box.Name, err)
			}
			if parent != "" && !storagegc.IsDigestID(parent) {
				return 0, fmt.Errorf("container %s has an unknown snapshot parent", box.Name)
			}
			parents[parent] = true
		}
	}
	listed, err := c.ImagesList(ctx, "")
	if err != nil {
		return 0, err
	}
	var tagged []container.Image
	var imageRefs []string
	for _, img := range listed {
		if img.Repository == "" || img.Tag == "" || img.ID == "" {
			return 0, fmt.Errorf("incomplete image inventory")
		}
		if img.Repository == "<none>" || img.Tag == "<none>" {
			continue
		}
		tagged = append(tagged, img)
		imageRefs = append(imageRefs, img.Repository+":"+img.Tag)
	}
	details, err := c.InspectImages(ctx, imageRefs)
	if err != nil {
		return 0, err
	}
	if len(details) != len(tagged) {
		return 0, fmt.Errorf("incomplete image inspection")
	}
	var images []storagegc.Image
	managed := 0
	for i, img := range tagged {
		ref := img.Repository + ":" + img.Tag
		detail := details[i]
		if len(parents) > 0 {
			if detail.RootFS.Type != "layers" {
				return 0, fmt.Errorf("image %s has unknown rootfs metadata", ref)
			}
			chain, err := storagegc.ChainID(detail.RootFS.Layers)
			if err != nil {
				return 0, err
			}
			if parents[chain] {
				refs = append(refs, detail.ID)
			}
		}
		isManaged := strings.TrimPrefix(img.Repository, "docker.io/") == agentboximage.ComposedImageRepo && detail.Config.Labels["yoloctl.kind"] == "composed"
		if isManaged {
			managed++
		}
		// nerdctl may return only the inspected tag in RepoTags. Inventory
		// foreign aliases too, so their protected IDs retain composed tags.
		images = append(images, storagegc.Image{Ref: ref, ID: detail.ID,
			Names:   append(detail.RepoTags, detail.RepoDigests...),
			Managed: isManaged})
	}
	remove, err := storagegc.Plan(images, refs)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, ref := range remove {
		if dryRun {
			_, _ = fmt.Fprintf(out, "would remove %s\n", ref)
			continue
		}
		if err := c.ImageRemove(ctx, ref); err != nil {
			return removed, fmt.Errorf("remove image %s: %w", ref, err)
		}
		removed++
		_, _ = fmt.Fprintf(out, "removed unreferenced composed image %s\n", ref)
	}
	if dryRun {
		_, _ = fmt.Fprintf(out, "would prune %d composed image(s); kept %d. No images removed.\n", len(remove), managed-len(remove))
	}
	return removed, nil
}

type storageCollector struct {
	disk   func() (storagegc.Disk, error)
	images func(context.Context, []string) (int, error)
	cache  func(context.Context, uint64) error
}

func nativeStorageCollector(space string, out io.Writer) storageCollector {
	return storageCollector{
		disk: storagegc.RootDisk,
		images: func(ctx context.Context, extra []string) (int, error) {
			refs, err := declaredImageReferences(space)
			if err != nil {
				return 0, err
			}
			// Build preflight can run inside an operation with structured stdout.
			// Route nerdctl removal output through the same diagnostic stream.
			return collectImages(ctx, container.NewWithOutput(space, out, out), append(refs, extra...), out, false)
		},
		cache: func(ctx context.Context, keep uint64) error {
			if current := spaceenv.Current(); current != nil && current.ServiceManager == "microvm" {
				// A lazy builder with no socket cannot hold reclaimable leases.
				// Cache maintenance must not boot the daemon just to prune it.
				if _, err := os.Lstat("/run/buildkit/buildkitd.sock"); errors.Is(err, os.ErrNotExist) {
					return nil
				} else if err != nil {
					return err
				}
			}
			cmd := exec.CommandContext(ctx, "buildctl", "prune", "--all", "--keep-storage", strconv.FormatUint(keep/1_000_000, 10))
			// BuildKit's byte totals include shared data. Report measured free
			// space instead; do not imply that its output is physical recovery.
			cmd.Stdout, cmd.Stderr = io.Discard, out
			return cmd.Run()
		},
	}
}

func (c storageCollector) collect(ctx context.Context, extra []string, out io.Writer) error {
	before, err := c.disk()
	if err != nil {
		return err
	}
	removed, err := c.images(ctx, extra)
	if err != nil {
		return fmt.Errorf("storage cleanup stopped: %w", err)
	}
	if removed == 0 && !before.Low() {
		return nil
	}
	keep := before.CacheBudget()
	if before.Low() {
		keep = 0 // pressure pass may drop all unused cache, never live resources
	}
	if err := c.cache(ctx, keep); err != nil {
		return fmt.Errorf("prune unused build cache: %w", err)
	}
	after, err := c.disk()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "storage cleanup: %d obsolete image(s) removed; root free %.1f GiB (was %.1f GiB)\n", removed, gib(after.Available), gib(before.Available))
	return nil
}

func (c storageCollector) beforeBuild(ctx context.Context, refs []string, out io.Writer) (result error) {
	disk, err := c.disk()
	if err != nil {
		return fmt.Errorf("measure VM storage before build: %w", err)
	}
	if !disk.Low() {
		return nil
	}
	_, _ = fmt.Fprintln(out, "notice: VM disk space is low; cleaning unused images and build cache before the build")
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	defer func() {
		if result == nil {
			return
		}
		if parent.Err() != nil {
			result = parent.Err()
		} else if ctx.Err() != nil {
			result = fmt.Errorf("VM system disk has %.1f GiB free (20%% reserve: %.1f GiB); storage cleanup exceeded its 30s preflight budget; build/pull not started; allow idle cleanup to finish or free space: %w",
				gib(disk.Available), gib(disk.TargetFree()), ctx.Err())
		}
	}()
	if err := c.collect(ctx, refs, out); err != nil {
		return err
	}
	disk, err = c.disk()
	if err != nil {
		return err
	}
	// containerd can release snapshots shortly after tag/cache deletion. Give
	// that reclamation a bounded chance to show up in actual filesystem usage.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
waiting:
	for disk.Low() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			break waiting
		case <-tick.C:
			disk, err = c.disk()
			if err != nil {
				return err
			}
		}
	}
	if disk.Low() {
		return fmt.Errorf("VM system disk has %.1f GiB free after cleanup; at least %.1f GiB (20%%) is required before a build or pull; retained images and box data were preserved; free space or increase the VM system disk", gib(disk.Available), gib(disk.TargetFree()))
	}
	return nil
}

func gib(n uint64) float64 { return float64(n) / float64(1024*storagegc.MiB) }
