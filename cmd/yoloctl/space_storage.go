package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// spaceStorageMethod is the read behind `space storage` and the space view's
// system disk details. It takes no lock and never changes anything.
const spaceStorageMethod = "space.storage"

// spaceStorageTimeout bounds one measurement inside the VM, below the space
// view's popup deadline. containerd walks only the boxes' writable layers.
const spaceStorageTimeout = 20 * time.Second

// Disposable runtime directories on the system disk, beside the snapshots.
const (
	containerdContentDir = "/var/lib/containerd/io.containerd.content.v1.content"
	buildkitStateDir     = "/var/lib/buildkit"
)

// storageDisk is one filesystem. Used counts the filesystem reserve, as the
// space view's gauges do.
type storageDisk struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Used  uint64 `json:"used"`
}

// storageBox is one container's writable layer: everything the box wrote
// outside its mounts, stopped containers included.
type storageBox struct {
	Name      string `json:"name"`
	Container string `json:"container"`
	Running   bool   `json:"running"`
	Declared  bool   `json:"declared"` // a box of this space, not another container
	Bytes     uint64 `json:"bytes"`
}

// storageImages is unpacked image layers plus the downloaded and built
// blobs they came from.
type storageImages struct {
	Bytes  uint64 `json:"bytes"`
	Layers uint64 `json:"layers"`
	Blobs  uint64 `json:"blobs"`
}

// storageBuild is BuildKit's cache: its snapshots in containerd (it runs the
// containerd worker) and its own state directory.
type storageBuild struct {
	Bytes     uint64 `json:"bytes"`
	Snapshots uint64 `json:"snapshots"`
	State     uint64 `json:"state"`
}

type storageError struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
}

// storageReport partitions the system disk. Each snapshot is counted once,
// so boxes + images + buildkit + other == system.used. An unavailable part
// is null with its reason in errors; other is then null too, never a
// remainder that silently absorbs it.
type storageReport struct {
	MeasuredAt time.Time      `json:"measured_at"`
	System     *storageDisk   `json:"system"`
	Data       *storageDisk   `json:"data"`
	Boxes      []storageBox   `json:"boxes"`
	Images     *storageImages `json:"images"`
	BuildKit   *storageBuild  `json:"buildkit"`
	Other      *uint64        `json:"other"`
	Errors     []storageError `json:"errors"`
}

// storageSource reads the VM. Its functions are replaceable so the
// partition can be tested without one.
type storageSource struct {
	dataMount    string
	statfs       func(string) (spacetelemetry.Usage, error)
	declared     func() ([]string, error)
	containers   func(context.Context) ([]container.Container, error)
	snapshotKeys func(context.Context, []string) (map[string]string, error)
	snapshots    func(context.Context) (map[string]uint64, error)
	dirUsage     func(context.Context, string) (uint64, error)
}

func nativeStorageSource(space string) storageSource {
	c := container.NewWithOutput(space, io.Discard, io.Discard)
	dataMount := ""
	if current := spaceenv.Current(); current != nil {
		dataMount = current.DiskMount
	}
	return storageSource{
		dataMount:    dataMount,
		statfs:       spacetelemetry.StatfsUsage,
		declared:     func() ([]string, error) { return agentbox.ListNames(space) },
		containers:   func(ctx context.Context) ([]container.Container, error) { return c.List(ctx, "") },
		snapshotKeys: c.ContainerSnapshots,
		snapshots:    c.SnapshotUsages,
		dirUsage:     diskUsage,
	}
}

func storageDiskOf(u spacetelemetry.Usage) *storageDisk {
	return &storageDisk{Total: u.Total, Free: min(u.Free, u.Total), Used: u.Used()}
}

func collectSpaceStorage(ctx context.Context, src storageSource, now time.Time) *storageReport {
	r := &storageReport{MeasuredAt: now.UTC(), Boxes: []storageBox{}, Errors: []storageError{}}
	fail := func(item string, err error) { r.Errors = append(r.Errors, storageError{item, err.Error()}) }
	if u, err := src.statfs("/"); err != nil {
		fail("system", err)
	} else {
		r.System = storageDiskOf(u)
	}
	if src.dataMount == "" {
		fail("data", errors.New("data disk mount unknown"))
	} else if u, err := src.statfs(src.dataMount); err != nil {
		fail("data", err)
	} else {
		r.Data = storageDiskOf(u)
	}

	complete := true
	usages, err := src.snapshots(ctx)
	if err != nil {
		fail("snapshots", err)
		complete = false
	}
	owned := map[string]bool{}
	boxesKnown, allKeyed := false, true
	if containers, err := src.containers(ctx); err != nil {
		fail("boxes", err)
	} else if keys, err := src.snapshotKeys(ctx, containerNames(containers)); err != nil {
		fail("boxes", err)
	} else {
		boxesKnown = true
		declared := map[string]bool{}
		// Declarations only label containers; they never change the sums.
		if names, err := src.declared(); err != nil {
			fail("declarations", err)
		} else {
			for _, n := range names {
				declared[n] = true
			}
		}
		for _, c := range containers {
			key, ok := container.SnapshotFor(keys, c.ID)
			if !ok {
				fail("box "+c.Name, errors.New("no overlayfs writable layer found"))
				complete, allKeyed = false, false
				continue
			}
			owned[key] = true
			if usages == nil {
				continue
			}
			size, ok := usages[key]
			if !ok {
				fail("box "+c.Name, errors.New("writable layer was not measured"))
				complete = false
				continue
			}
			r.Boxes = append(r.Boxes, storageBox{Name: c.Name, Container: c.ID, Running: strings.HasPrefix(c.Status, "Up"),
				Declared: declared[c.Name], Bytes: size})
		}
		slices.SortStableFunc(r.Boxes, func(a, b storageBox) int {
			if a.Bytes != b.Bytes {
				if a.Bytes > b.Bytes {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Name, b.Name)
		})
	}
	if !boxesKnown {
		complete = false
	}

	blobs, blobsErr := src.dirUsage(ctx, containerdContentDir)
	if blobsErr != nil {
		fail("images", blobsErr)
		complete = false
	}
	state, stateErr := src.dirUsage(ctx, buildkitStateDir)
	if stateErr != nil {
		fail("buildkit", stateErr)
		complete = false
	}
	// Without the containers' keys, a writable layer would be counted as
	// build cache; leave both unavailable instead.
	if usages != nil && boxesKnown {
		var layers, cache uint64
		for key, size := range usages {
			switch {
			case owned[key]:
			case strings.HasPrefix(key, "sha256:"): // committed image layers are named by ChainID
				layers += size
			default: // BuildKit records, views and transient unpacks
				cache += size
			}
		}
		if blobsErr == nil {
			r.Images = &storageImages{Bytes: layers + blobs, Layers: layers, Blobs: blobs}
		}
		// An unkeyed container's layer would land in the build cache.
		if stateErr == nil && allKeyed {
			r.BuildKit = &storageBuild{Bytes: cache + state, Snapshots: cache, State: state}
		}
	}
	if complete && r.System != nil && r.Images != nil && r.BuildKit != nil {
		sum := r.Images.Bytes + r.BuildKit.Bytes
		for _, b := range r.Boxes {
			sum += b.Bytes
		}
		// Parts measured a moment apart can exceed the filesystem's total
		// while files change; the remainder is then nothing, not negative.
		other := r.System.Used - min(sum, r.System.Used)
		r.Other = &other
	}
	return r
}

func containerNames(cs []container.Container) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func runSpaceStorageView(ctx context.Context, space string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, spaceStorageTimeout)
	defer cancel()
	return json.NewEncoder(out).Encode(collectSpaceStorage(ctx, nativeStorageSource(space), time.Now()))
}

func newSpaceStorageCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "storage [SPACE]",
		Short: "Show what fills the VM's system and data disks",
		Long: `Measures the space's two VM disks and what fills the system disk.

The system disk holds boxes, images and build caches: the guest OS, image
layers, BuildKit's cache and every box's own files (its home, /tmp, /var/tmp
and anything else written outside its mounts). It is rebuilt with the VM.
The data disk holds accounts and box settings and is kept across VM rebuilds.

Each box's writable layer is measured by containerd, stopped boxes included.
The rest is image layers and their downloaded blobs, BuildKit's cache, and
"guest OS and other" (the remainder, including the filesystem reserve and
ephemeral workdirs). A part that cannot be measured is reported as
unavailable rather than folded into the remainder.

Space can be freed by removing files inside a box, recreating a box (which
discards its writable layer), or yoloctl image prune. The space view warns
below 4 GiB free (15% of a smaller disk) or from 85% used, and marks the disk
critical below 2 GiB (10%) or from 95%.

  yoloctl space storage
  yoloctl space storage dev --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if operator, err := loadOperatorClient(); err != nil {
				return err
			} else if operator != nil {
				return asUserError(errors.New("box operator cannot read space storage"))
			}
			space, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), spaceStorageTimeout+2*vmhost.GuestProbeTimeout)
			defer cancel()
			boot, err := checkControllerSpace(ctx, space)
			if err != nil {
				return err
			}
			var report storageReport
			if err := controllerJSON(ctx, boot, spaceStorageMethod, nil, nil, &report, cmd.ErrOrStderr()); err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			return writeSpaceStorage(cmd.OutOrStdout(), space, &report)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the human-readable view")
	return cmd
}

func writeSpaceStorage(w io.Writer, space string, r *storageReport) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	disk := func(label string, d *storageDisk, about string) {
		if d == nil || d.Total == 0 {
			_, _ = fmt.Fprintf(tw, "%s\tunavailable\t%s\n", label, about)
			return
		}
		u := spacetelemetry.Usage{Total: d.Total, Free: d.Free}
		mark := ""
		switch u.Pressure() {
		case spacetelemetry.DiskWarn:
			mark = "  ⚠ low"
		case spacetelemetry.DiskCritical:
			mark = "  ✖ almost full"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s of %s used, %s free (%.0f%%)%s\t%s\n", label, humanBytes(d.Used), humanBytes(d.Total), humanBytes(d.Free),
			100*float64(d.Used)/float64(d.Total), mark, about)
	}
	_, _ = fmt.Fprintf(tw, "SPACE\t%s\n", space)
	disk("SYSTEM DISK", r.System, "boxes, images, build caches")
	disk("DATA DISK", r.Data, "accounts & box settings (kept across VM rebuilds)")
	_, _ = fmt.Fprintln(tw)
	_, _ = fmt.Fprintln(tw, "SYSTEM DISK\tSIZE\tWHAT")
	labelled := !slices.ContainsFunc(r.Errors, func(e storageError) bool { return e.Item == "declarations" })
	for _, b := range r.Boxes {
		what := "box"
		switch {
		case !labelled:
			what = "container"
		case !b.Declared:
			what = "container (not a box of this space)"
		}
		if b.Running {
			what += ", running"
		} else {
			what += ", stopped"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", b.Name, humanBytes(b.Bytes), what)
	}
	if r.Images != nil {
		_, _ = fmt.Fprintf(tw, "  images\t%s\tlayers %s, downloaded blobs %s\n", humanBytes(r.Images.Bytes), humanBytes(r.Images.Layers), humanBytes(r.Images.Blobs))
	} else {
		_, _ = fmt.Fprintf(tw, "  images\tunavailable\t\n")
	}
	if r.BuildKit != nil {
		_, _ = fmt.Fprintf(tw, "  build cache\t%s\tBuildKit\n", humanBytes(r.BuildKit.Bytes))
	} else {
		_, _ = fmt.Fprintf(tw, "  build cache\tunavailable\t\n")
	}
	if r.Other != nil {
		_, _ = fmt.Fprintf(tw, "  guest OS and other\t%s\tremainder, filesystem reserve included\n", humanBytes(*r.Other))
	} else {
		_, _ = fmt.Fprintf(tw, "  guest OS and other\tunavailable\tnot every part could be measured\n")
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, e := range r.Errors {
		_, _ = fmt.Fprintf(w, "unavailable: %s: %s\n", e.Item, e.Reason)
	}
	_, err := fmt.Fprintf(w, "measured %s\n", r.MeasuredAt.Local().Format("15:04:05"))
	return err
}
