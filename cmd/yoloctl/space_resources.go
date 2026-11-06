package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/strictyaml"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Resource changes are host lifecycle operations. They never start or replace
// a VM, contact its controller, or change controller/disk identity.
var (
	lookupResourceSpace = findSpaceInstance
	applyVMResources    = func(ctx context.Context, name string, r vmapi.Resources) error {
		return vmhost.New().SetResources(ctx, name, r)
	}
	selectResourceMemory = resolveSpaceMemory
)

type spaceResourceDocument struct {
	CPUs   int    `yaml:"cpus"`
	Memory string `yaml:"memory"`
	Disk   string `yaml:"disk"`
	// DataDisk grows the data disk; NoBackup (set only) skips the copy.
	DataDisk string `yaml:"data_disk"`
	NoBackup bool   `yaml:"-"`
	// LayerCompression (set only) is the bootstrap value: "" or "gzip".
	LayerCompression string `yaml:"-"`
}

type spaceResourceSnapshot struct {
	boot *spaceconfig.Bootstrap
	data []byte
	info os.FileInfo
}

func newSpaceSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set [SPACE]",
		Short: "Change a stopped space's CPU, memory, disks or image layer compression",
		Long: `Change VM boot resources without replacing its disks or containers.
The space must already be stopped. Start it explicitly after the change.
--memory auto recalculates RAM from current host headroom and saves a concrete
allocation. --disk only grows the VM system disk and is saved now; it applies
at the next space recreate, which replaces that disk (images, containers and
box files) anyway. --image-layer-compression chooses how newly built image
layers are stored inside the VM: none (default; faster builds and refreshes,
more disk) or gzip (less disk); it applies from the next space start and does
not recompress existing layers. An omitted flag preserves its current value.

  yoloctl space set default --memory auto --cpus 8
  yoloctl space start default
  yoloctl space set default --disk 50GiB
  yoloctl space recreate default`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("cpus") && !cmd.Flags().Changed("memory") && !cmd.Flags().Changed("disk") && !cmd.Flags().Changed("data-disk") && !cmd.Flags().Changed("image-layer-compression") {
				return asUserError(errors.New("specify --cpus, --memory, --disk, --data-disk or --image-layer-compression"))
			}
			compression, _ := cmd.Flags().GetString("image-layer-compression")
			if cmd.Flags().Changed("image-layer-compression") && compression != "none" && compression != spaceenv.ImageLayerGzip {
				return asUserError(fmt.Errorf("--image-layer-compression must be none or gzip, not %q", compression))
			}
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			return changeSpaceResources(cmd, name, nil, func(d *spaceResourceDocument) {
				if cmd.Flags().Changed("cpus") {
					d.CPUs, _ = cmd.Flags().GetInt("cpus")
				}
				if cmd.Flags().Changed("memory") {
					d.Memory, _ = cmd.Flags().GetString("memory")
				}
				if cmd.Flags().Changed("disk") {
					d.Disk, _ = cmd.Flags().GetString("disk")
				}
				if cmd.Flags().Changed("data-disk") {
					d.DataDisk, _ = cmd.Flags().GetString("data-disk")
				}
				d.NoBackup, _ = cmd.Flags().GetBool("no-backup")
				if cmd.Flags().Changed("image-layer-compression") {
					d.LayerCompression = ""
					if compression == spaceenv.ImageLayerGzip {
						d.LayerCompression = spaceenv.ImageLayerGzip
					}
				}
			})
		},
	}
	cmd.Flags().Int("cpus", 0, "VM CPU count (omit to keep the current allocation)")
	cmd.Flags().String("memory", "", "VM RAM: auto (host-aware 4–16 GiB) or an explicit size such as 8GiB")
	cmd.Flags().String("disk", "", "VM system disk size in whole GiB, such as 50GiB; only grows, applies at the next space recreate")
	cmd.Flags().String("data-disk", "", "Data disk size in whole GiB, such as 20GiB; only grows, offline at the next space start, after a cheap copy of the disk (Firecracker)")
	cmd.Flags().Bool("no-backup", false, "With --data-disk, grow without the pre-grow copy when the host filesystem cannot make one; export the space first")
	cmd.Flags().String("image-layer-compression", "", "Newly built image layers inside the VM: none (default; faster, more disk) or gzip (less disk); applies from the next space start")
	return cmd
}

func layerCompressionLabel(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func newSpaceEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit [SPACE]",
		Short: "Edit a stopped space's CPU, memory and system disk allocation in $EDITOR",
		Long: `Open a private temporary YAML containing only cpus, memory and disk in
$EDITOR (default vi). Memory may be auto or an explicit size such as 8GiB;
disk only grows and applies at the next space recreate.
The space must be stopped. Editing does not hold the lifecycle lock; saving
refuses concurrent changes or a VM that started while the editor was open.
Other space settings and identities cannot be changed here.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			return editSpaceResources(cmd, name, runEditor)
		},
	}
}

func readSpaceResourceSnapshot(name string) (spaceResourceSnapshot, error) {
	var s spaceResourceSnapshot
	dir, err := layout.SpaceBundleDir(name)
	if err != nil {
		return s, err
	}
	s.data, s.info, err = safepath.ReadFileInfo(dir, "space.yaml", 1<<20)
	if err != nil {
		return s, err
	}
	s.boot, err = spaceconfig.ParseBootstrap(s.data)
	if err == nil && s.boot.Name != name {
		err = errors.New("bootstrap space identity mismatch")
	}
	return s, err
}

func requireResourceSpaceStopped(ctx context.Context, boot *spaceconfig.Bootstrap) error {
	if boot.Initializing || boot.Import != nil || boot.ImageUpdate != nil {
		return errors.New("finish pending space initialization, import or image recovery before changing resources")
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	instance, found, err := lookupResourceSpace(probe, boot.Name)
	if err != nil {
		return fmt.Errorf("cannot establish stopped VM state: %w", err)
	}
	if !found || instance.Status != "Stopped" {
		return asUserError(fmt.Errorf("space %s must have a stopped VM; run yoloctl space stop %s first", boot.Name, boot.Name))
	}
	return nil
}

func changeSpaceResources(cmd *cobra.Command, name string, expected *spaceResourceSnapshot, edit func(*spaceResourceDocument)) error {
	release, err := lockHostLifecycle(cmd, name)
	if err != nil {
		return err
	}
	defer release()
	current, err := readSpaceResourceSnapshot(name)
	if err != nil {
		return err
	}
	if expected != nil && (!os.SameFile(current.info, expected.info) || !bytes.Equal(current.data, expected.data)) {
		return errors.New("space changed while editing; reopen space edit before saving")
	}
	if err := requireResourceSpaceStopped(cmd.Context(), current.boot); err != nil {
		return err
	}
	// A grow that finished at the last start is recorded first.
	if err := settleDataDiskGrow(current.boot, cmd.OutOrStdout()); err != nil {
		return err
	}
	doc := spaceResourceDocument{CPUs: current.boot.VM.CPUs, Memory: current.boot.VM.Memory, Disk: current.boot.VM.Disk, DataDisk: dataDiskSetting(current.boot), LayerCompression: current.boot.ImageLayerCompression}
	edit(&doc)
	disk, err := growSpaceDisk(current.boot, doc.Disk, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	doc.Memory, err = selectResourceMemory(cmd.Context(), doc.Memory, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	r := vmapi.Resources{CPUs: doc.CPUs, Memory: doc.Memory}
	if err := vmapi.ValidateResources(current.boot.VM.Backend, r); err != nil {
		return asUserError(err)
	}
	diskChanged := disk != current.boot.VM.Disk
	compressionChanged := doc.LayerCompression != current.boot.ImageLayerCompression
	if current.boot.VM.CPUs != r.CPUs || current.boot.VM.Memory != r.Memory || diskChanged || compressionChanged {
		current.boot.VM.CPUs, current.boot.VM.Memory, current.boot.VM.Disk = r.CPUs, r.Memory, disk
		current.boot.ImageLayerCompression = doc.LayerCompression
		// Desired settings are durable before the adapter is updated. Explicit
		// start reapplies them before boot after an interrupted/failed update.
		if err := current.boot.Save(); err != nil {
			return err
		}
	}
	if err := applyVMResources(cmd.Context(), name, r); err != nil {
		return fmt.Errorf("VM resources saved but not applied; inspect the error, then retry space set or run space start %s: %w", name, err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "space %s: %d CPUs, %s RAM; remains stopped (start with yoloctl space start %s)\n", name, r.CPUs, r.Memory, name)
	if err == nil && compressionChanged {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "space %s: image layer compression %s saved; it applies to images built after the next yoloctl space start %s\n", name, layerCompressionLabel(doc.LayerCompression), name)
	}
	if err == nil && diskChanged {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "space %s: disk %s saved; it applies at the next yoloctl space recreate %s (the VM keeps its current disk until then)\n", name, disk, name)
	}
	if err != nil {
		return err
	}
	// A new size, or the same size again to resume an interrupted request.
	g := current.boot.DataDiskGrow
	resume := g != nil && g.Phase == spaceconfig.GrowRequested && doc.DataDisk == spaceconfig.DiskSize(g.To)
	if doc.DataDisk != dataDiskSetting(current.boot) || resume {
		return growSpaceDataDisk(current.boot, doc.DataDisk, doc.NoBackup, cmd.OutOrStdout())
	}
	return nil
}

func editSpaceResources(cmd *cobra.Command, name string, editor func(context.Context, string) error) error {
	release, err := lockHostLifecycle(cmd, name)
	if err != nil {
		return err
	}
	before, err := readSpaceResourceSnapshot(name)
	if err == nil {
		err = requireResourceSpaceStopped(cmd.Context(), before.boot)
	}
	release()
	if err != nil {
		return err
	}
	raw, err := yaml.Marshal(spaceResourceDocument{CPUs: before.boot.VM.CPUs, Memory: before.boot.VM.Memory, Disk: before.boot.VM.Disk, DataDisk: dataDiskSetting(before.boot)})
	if err != nil {
		return err
	}
	raw = append([]byte("# VM boot resources only. The space must remain stopped.\n# memory: auto recalculates host-aware RAM; cpus is a positive integer.\n# disk: whole GiB, only grows; it applies at the next space recreate.\n# data_disk: whole GiB, only grows, offline at the next space start after a\n# cheap copy (use space set --data-disk --no-backup when none is possible).\n"), raw...)
	tmp, err := os.CreateTemp("", "yoloctl-space-edit-*.yaml")
	if err != nil {
		return err
	}
	path, retain := tmp.Name(), false
	defer func() {
		if !retain {
			_ = os.Remove(path)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := editor(cmd.Context(), path); err != nil {
		retain = true
		return fmt.Errorf("edits kept at %s: %w", path, err)
	}
	edited, err := safepath.ReadFile(filepath.Dir(path), filepath.Base(path), 64<<10)
	if err != nil {
		retain = true
		return fmt.Errorf("edits kept at %s: %w", path, err)
	}
	if bytes.Equal(raw, edited) {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no changes")
		return err
	}
	var doc spaceResourceDocument
	if err := strictyaml.Decode(edited, &doc); err != nil {
		retain = true
		return fmt.Errorf("edits kept at %s: %w", path, err)
	}
	if err := changeSpaceResources(cmd, name, &before, func(d *spaceResourceDocument) { *d = doc }); err != nil {
		retain = true
		return fmt.Errorf("edits kept at %s: %w", path, err)
	}
	return nil
}
