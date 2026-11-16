package agentbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// VerifyFileExportRuntime verifies retained native identity for this already
// selected container. Legacy/privileged containers have no native helper record.
// This is not readiness, current-build compatibility or authority to start it.
func VerifyFileExportRuntime(ctx context.Context, client *container.Client, name string, details *container.Details) error {
	if details == nil {
		return errors.New("missing export container observation")
	}
	if !hasOpenShellRuntimeSignals(details) {
		return nil
	}
	current := spaceenv.Current()
	if current == nil || os.Geteuid() != 0 || os.Getegid() != 0 || client == nil {
		return errors.New("export metadata requires verified VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(client.Env()); err != nil {
		return err
	}
	if err := layout.ValidateName("agentbox", name); err != nil {
		return err
	}
	if err := container.ValidateContainerID(details.ID); err != nil {
		return err
	}
	launch, ok := openShellRuntimeEvidence(details)
	if !ok {
		return errors.New("export container lacks complete native helper/bind evidence")
	}
	root, err := layout.OpenShellControlDir()
	if err != nil {
		return err
	}
	control := filepath.Join(root, "containers", launch.Incarnation)
	if details.Labels[openshell.LabelControlRoot] != control {
		return errors.New("export control bind differs from the native disk")
	}
	if err := verifyFileExportRecords(*current, name, details, func(path string) ([]byte, error) { return readRuntimeMetadata(path, 0600) }); err != nil {
		return err
	}
	return ctx.Err()
}

func verifyFileExportRecords(current spaceenv.Context, name string, details *container.Details, read func(string) ([]byte, error)) error {
	launch, ok := openShellRuntimeEvidence(details)
	if !ok {
		return errors.New("export native helper evidence is incomplete")
	}
	control := filepath.Join(current.DiskMount, "runtime", "openshell", "containers", launch.Incarnation)
	raw, err := read(filepath.Join(control, "current.json"))
	if err != nil {
		return err
	}
	record, err := validateFileExportRecord(current, name, details, raw)
	if err != nil {
		return err
	}
	selected, err := read(filepath.Join(control, "generations", record.Generation, "record.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, selected) {
		return errors.New("export current record differs from its retained generation")
	}
	return nil
}

func validateFileExportRecord(current spaceenv.Context, name string, d *container.Details, raw []byte) (openshell.RuntimeRecord, error) {
	var zero openshell.RuntimeRecord
	launch, ok := openShellRuntimeEvidence(d)
	if !ok || d.Privileged == nil || *d.Privileged || container.ValidateContainerID(d.ID) != nil || d.Name != name || d.Labels[LabelSpace] != current.Space || d.Labels[LabelAgentbox] != name || d.Labels[LabelKind] != KindAgentbox {
		return zero, errors.New("export container ownership or native binding mismatch")
	}
	control := filepath.Join(current.DiskMount, "runtime", "openshell", "containers", launch.Incarnation)
	if d.Labels[openshell.LabelControlRoot] != control {
		return zero, errors.New("export control root belongs to a different disk")
	}
	r, err := openshell.DecodeExportRecord(raw, launch)
	if err != nil {
		return zero, err
	}
	b := r.Launch.Binding
	if b.Space != current.Space || b.Box != name || b.DiskID != current.DiskID || b.DiskUUID != current.DiskUUID || b.Incarnation != launch.Incarnation || r.Launch.UID != uint32(current.UID) || r.Launch.GID != uint32(current.GID) {
		return zero, errors.New("export record belongs to another native disk/box/identity")
	}
	workdirs, sockets := 0, 0
	for _, m := range d.Mounts {
		if m.Destination == r.Launch.Workdir {
			workdirs++
			if m.ReadOnly != r.Launch.WorkspaceReadOnly {
				return zero, errors.New("export workspace bind intent mismatch")
			}
		}
		if m.Destination == sandbox.SocketDirContainer {
			sockets++
			if m.Source != sandbox.SocketDirHost(name) || m.ReadOnly {
				return zero, errors.New("export public SSH bind mismatch")
			}
		}
	}
	if workdirs != 1 || sockets != 1 {
		return zero, errors.New("export lacks its recorded workspace/public SSH binds")
	}
	return r, nil
}
