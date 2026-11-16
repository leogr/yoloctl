package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
)

type boxFileContainer interface {
	Inspect(context.Context, string) (*container.Details, error)
	CopyArchive(context.Context, string, string, io.Writer) error
}

func runControllerBoxFiles(ctx context.Context, req controller.Request, out, errw io.Writer) error {
	if !currentControllerPrincipal(ctx).Admin() {
		return errors.New("box file export requires the human host endpoint")
	}
	r, err := decodeBoxFilesRequest(req)
	if err != nil {
		return err
	}
	// Declaration reads are strict and unavailable/missing is not an empty box.
	if _, err := agentbox.Load(req.Space, r.Box); err != nil {
		return fmt.Errorf("read export box declaration: %w", err)
	}
	client := container.NewWithOutput(req.Space, io.Discard, errw)
	return exportRetainedBox(ctx, req.Space, r, client, func(ctx context.Context, d *container.Details) error {
		return agentbox.VerifyFileExportRuntime(ctx, client, r.Box, d)
	}, out)
}
func checkBoxFileContainer(space, box, cid string, d *container.Details) error {
	if d == nil || container.ValidateContainerID(d.ID) != nil || cid != "" && d.ID != cid || d.Name != box || d.Labels[agentbox.LabelSpace] != space || d.Labels[agentbox.LabelAgentbox] != box || d.Labels[agentbox.LabelKind] != agentbox.KindAgentbox {
		return errors.New("export container identity is missing, ambiguous or not owned by the selected box")
	}
	if d.AmbiguousTaskStateObserved {
		return errors.New("export container runtime state is ambiguous")
	}
	if d.Running {
		if d.Status != "running" || d.PID <= 1 {
			return errors.New("export running state is unavailable")
		}
		return nil
	}
	if d.PID != 0 {
		return errors.New("export inactive state has a live process")
	}
	switch d.Status {
	case "created", "exited", "stopped":
		if d.InactiveTaskFieldsObserved {
			return nil
		}
	case "":
		if d.EmptyTaskStateObserved || d.InactiveTaskFieldsObserved {
			return nil
		}
	}
	return errors.New("export container runtime state is unavailable or changing")
}
func exportRetainedBox(ctx context.Context, space string, r boxFilesRequest, client boxFileContainer, verify func(context.Context, *container.Details) error, out io.Writer) error {
	selected, err := client.Inspect(ctx, r.Box)
	if err != nil {
		return err
	}
	if err := checkBoxFileContainer(space, r.Box, "", selected); err != nil {
		return err
	}
	cid := selected.ID
	if err := verify(ctx, selected); err != nil {
		return err
	}
	// Only the selected CID is inspected and passed to the producer from here.
	// A concurrent start/stop is allowed; replacement by name is never adopted.
	current, err := client.Inspect(ctx, cid)
	if err != nil {
		return err
	}
	if err := checkBoxFileContainer(space, r.Box, cid, current); err != nil {
		return err
	}
	if err := verify(ctx, current); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return client.CopyArchive(ctx, cid, r.Source, out)
}
