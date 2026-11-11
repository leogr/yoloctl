package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// The internal view retains the resolver's richer state so aggregation uses
// the same formatters as a single-space command.
type controllerBoxView struct {
	State     boxstate.Box         `json:"state"`
	Config    *agentbox.Config     `json:"config,omitempty"`
	Container *container.Container `json:"container,omitempty"`
}

func controllerJSON(ctx context.Context, boot *spaceconfig.Bootstrap, method string, args []string, flags map[string][]string, dst any, errw io.Writer) error {
	raw := boundedBuffer{Limit: 64 << 20}
	req := controller.Request{ID: rand.Text(), Method: method, Space: boot.Name, Args: args, Flags: flags, Dash: -1}
	if err := callHostController(ctx, boot, req, controller.ClientIO{Out: &raw, Err: errw}); err != nil {
		return err
	}
	return decodeBoundedJSON(&raw.Buffer, dst, raw.Limit)
}

type boundedBuffer struct {
	bytes.Buffer
	Limit int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.Limit {
		return 0, errors.New("controller view exceeds size limit; select a narrower view")
	}
	return b.Buffer.Write(p)
}

func controllerAllSpaces(cmd *cobra.Command, method string) error {
	if flagChanged(cmd, "space") {
		return asUserError(errors.New("--space and --all-spaces are mutually exclusive"))
	}
	if operator, err := loadOperatorClient(); err != nil {
		return err
	} else if operator != nil {
		return asUserError(errors.New("box operator cannot access all spaces"))
	}
	names, err := spaceconfig.List()
	if err != nil {
		return err
	}
	var boxes []boxstate.Box
	var images []imageRow
	var agents []agentStatusRow
	var unavailable []error
	for _, name := range names {
		readCtx, cancel := context.WithTimeout(cmd.Context(), 2*vmhost.GuestProbeTimeout)
		boot, err := checkControllerSpace(readCtx, name)
		if err == nil {
			switch method {
			case "box.list":
				var view []controllerBoxView
				err = controllerJSON(readCtx, boot, "box.inventory", nil, nil, &view, cmd.ErrOrStderr())
				for _, b := range view {
					b.State.Config, b.State.Container = b.Config, b.Container
					boxes = append(boxes, b.State)
				}
			case "image.list":
				all, _ := cmd.Flags().GetBool("all")
				flags := map[string][]string{"all": {fmt.Sprint(all)}}
				var view []container.Image
				err = controllerJSON(readCtx, boot, "image.inventory", nil, flags, &view, cmd.ErrOrStderr())
				images = append(images, tagImageRows(name, view)...)
			case "agent.status":
				noLatest, _ := cmd.Flags().GetBool("no-check-latest")
				flags := map[string][]string{"json": {"true"}, "no-check-latest": {fmt.Sprint(noLatest)}}
				var view []agentStatusRow
				err = controllerJSON(readCtx, boot, method, nil, flags, &view, cmd.ErrOrStderr())
				for i := range view {
					view[i].Space = name
				}
				agents = append(agents, view...)
			default:
				err = errors.New("this command cannot aggregate spaces")
			}
		}
		cancel()
		if err != nil {
			unavailable = append(unavailable, fmt.Errorf("space %s unavailable: %w", name, err))
			cliWarn(logging.Host, "space %s unavailable: %v", name, err)
		}
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	switch method {
	case "box.list":
		workdir, _ := cmd.Flags().GetString("workdir")
		if workdir != "" {
			canonical, _, err := canonicalizeMountArg(workdir)
			if err != nil {
				return err
			}
			boxes = filterBoxStatesByWorkdir(boxes, canonical)
		}
		if value, _ := cmd.Flags().GetBool("running"); value {
			boxes = filterBoxStatesRunning(boxes)
		}
		if value, _ := cmd.Flags().GetBool("runtime"); value {
			boxes = filterBoxStatesRuntime(boxes)
		}
		if value, _ := cmd.Flags().GetString("state"); value != "" {
			boxes = filterBoxStatesByState(boxes, value)
		}
		if asJSON {
			err = writeBoxStatesJSON(cmd.OutOrStdout(), boxes)
		} else {
			err = printBoxStates(cmd.OutOrStdout(), boxes, true)
		}
	case "image.list":
		if asJSON {
			err = writeImagesJSON(cmd.OutOrStdout(), images)
		} else {
			err = writeImagesTable(cmd.OutOrStdout(), images)
		}
	case "agent.status":
		if asJSON {
			err = writeAgentStatusJSON(cmd.OutOrStdout(), agents)
		} else {
			err = printAgentStatus(cmd.OutOrStdout(), agents, true)
		}
	}
	if err != nil {
		return err
	}
	if len(unavailable) > 0 {
		return fmt.Errorf("partial results: %d space(s) unavailable; start them or select one running space", len(unavailable))
	}
	return nil
}

func controllerSpaceShow(cmd *cobra.Command, args []string) error {
	space, err := resolveSpacePositional(args)
	if err != nil {
		return err
	}
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return err
	}
	view := spaceShowData{AccountDiskID: boot.DiskID, AccountDiskUUID: boot.DiskUUID, MetadataOnly: true}
	readCtx, cancel := context.WithTimeout(cmd.Context(), 2*vmhost.GuestProbeTimeout)
	defer cancel()
	view.Instance, _, err = findSpaceInstance(readCtx, space)
	if err != nil {
		return err
	}
	if view.Instance.Name == "" {
		dir, err := vmhost.InstanceDir(space)
		if err != nil {
			return err
		}
		view.Instance = vmhost.Instance{Name: space, Status: "Absent", CPUs: boot.VM.CPUs, Dir: dir}
	}
	selected, selectedErr := checkControllerSpace(readCtx, space)
	if selectedErr == nil {
		var native spaceShowData
		if err := controllerJSON(readCtx, selected, "space.inventory", nil, nil, &native, cmd.ErrOrStderr()); err == nil {
			native.Instance = view.Instance
			view = native
		} else {
			selectedErr = err
		}
	}
	if selectedErr != nil {
		view.RuntimeStatus, view.RuntimeError = "unavailable", selectedErr.Error()
	}
	view.ConfigPath, err = vmhost.InstanceConfigPath(space)
	if err != nil {
		return err
	}
	view.HostAgentLogPath, err = vmhost.LogPath(space)
	if err != nil {
		return err
	}
	view.SavedDisk = boot.VM.Disk
	supported := false
	if g, ok := dataDiskGrower(boot.VM.Backend); ok {
		supported, _ = g.DataDiskGrowSupported(boot.Name, boot.VM.Image)
	}
	view.DataDisk = dataDiskShown(boot, supported)
	view.DataDiskBytes, _ = spaceconfig.DataDiskBytes(boot.VM.DataDisk)
	if boot.DataDiskGrow != nil {
		view.DataDiskGrow = boot.DataDiskGrow.Phase
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return writeSpaceShowJSON(cmd.OutOrStdout(), &view)
	}
	return writeSpaceShow(cmd.OutOrStdout(), &view)
}

func controllerSpaceList(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), vmhost.GuestProbeTimeout)
	defer cancel()
	instances, err := vmhost.New().List(ctx)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, inst := range instances {
		present[inst.Name] = true
	}
	names, err := spaceconfig.List()
	if err != nil {
		return err
	}
	saved := map[string]string{}
	for _, name := range names {
		if present[name] {
			if boot, err := spaceconfig.LoadBootstrap(name); err == nil {
				saved[name] = boot.VM.Disk
			}
			continue
		}
		boot, err := spaceconfig.LoadBootstrap(name)
		if err != nil {
			cliWarn(logging.Host, "space %s: %v", name, err)
			instances = append(instances, vmhost.Instance{Name: name, Status: "Unconfigured"})
			continue
		}
		dir, err := vmhost.InstanceDir(name)
		if err != nil {
			return err
		}
		instances = append(instances, vmhost.Instance{Name: name, Status: "Absent", CPUs: boot.VM.CPUs, Dir: dir})
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return writeInstancesJSON(cmd.OutOrStdout(), instances)
	}
	return printInstances(cmd.OutOrStdout(), instances, saved)
}

// Native output is serialized as a typed internal view, never a host pathname
// to read or write. This helper is shared by host and completion queries.
func runControllerView(ctx context.Context, req controller.Request, out io.Writer) (bool, error) {
	switch req.Method {
	case "space.inventory":
		view, err := collectSpaceShow(ctx, req.Space)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(view)
	case spaceTelemetryMethod:
		interval, err := parseTelemetryInterval(req.Flags["interval"])
		if err != nil {
			return true, err
		}
		return true, streamTelemetry(ctx, out, nativeTelemetrySource(req.Space), interval)
	case spaceStorageMethod:
		return true, runSpaceStorageView(ctx, req.Space, out)
	case boxDriftMethod:
		view, err := collectBoxDrift(ctx, req.Space, req.Args)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(view)
	case "box.inventory":
		boxes, err := newBoxStateResolver().ResolveSpace(ctx, req.Space, boxstate.RuntimeSummary)
		if err != nil {
			return true, err
		}
		view := make([]controllerBoxView, 0, len(boxes))
		for _, box := range boxes {
			view = append(view, controllerBoxView{box, box.Config, box.Container})
		}
		return true, json.NewEncoder(out).Encode(view)
	case "image.inventory":
		filter := "yoloctl/*"
		if values := req.Flags["all"]; len(values) > 0 && values[0] == "true" {
			filter = ""
		}
		images, err := container.New(req.Space).ImagesList(ctx, filter)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(images)
	}
	return false, nil
}
