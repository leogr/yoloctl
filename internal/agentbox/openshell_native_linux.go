package agentbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// OpenShellRuntime is observed, nonsecret metadata, never a launch grant or a
// historical operation receipt. In particular it contains no environment or
// bootstrap/TLS/JWT secrets. Every caller still owns current storage/lifecycle
// admission and, where applicable, independent native activation.
type OpenShellRuntime struct {
	ContainerID  string
	PID          int
	StartedAt    time.Time
	Running      bool
	ControlRoot  string
	RecordSHA256 string
	Launch       openshell.HelperLaunch
	Record       openshell.RuntimeRecord
	Release      openshell.QualifiedRelease
}

// InspectOpenShellRuntime reads only the current inspected container and fixed
// versioned native metadata. The infrastructure caller must already have loaded
// and verified the native controller storage. This read neither boots a VM nor
// installs/repairs/adopts any artifact, generation, socket or process.
func InspectOpenShellRuntime(ctx context.Context, client *container.Client, name string) (OpenShellRuntime, error) {
	return inspectOpenShellRuntime(ctx, client, name, name)
}

// selector is the create acknowledgement CID in the startup transaction.
// Read-only public views retain name selection and the same metadata checks.
func inspectOpenShellRuntime(ctx context.Context, client *container.Client, name, selector string) (OpenShellRuntime, error) {
	current := spaceenv.Current()
	if current == nil || os.Geteuid() != 0 || os.Getegid() != 0 || client == nil {
		return OpenShellRuntime{}, errors.New("OpenShell runtime metadata requires native root infrastructure")
	}
	if err := spaceenv.RequireSpace(client.Env()); err != nil {
		return OpenShellRuntime{}, err
	}
	if err := layout.ValidateName("agentbox", name); err != nil {
		return OpenShellRuntime{}, err
	}
	if err := ctx.Err(); err != nil {
		return OpenShellRuntime{}, err
	}
	details, err := client.Inspect(ctx, selector)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	launch, ok := openShellRuntimeEvidence(details)
	if !ok {
		return OpenShellRuntime{}, errors.New("container has no complete OpenShell runtime launch/bind evidence")
	}
	root, err := layout.OpenShellControlDir()
	if err != nil {
		return OpenShellRuntime{}, err
	}
	control := filepath.Join(root, "containers", launch.Incarnation)
	if details.Labels[openshell.LabelControlRoot] != control {
		return OpenShellRuntime{}, errors.New("runtime native control bind differs from the selected disk")
	}
	raw, err := readRuntimeMetadata(filepath.Join(control, "current.json"), 0o600)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	artifacts, err := openshell.RuntimeDirectory(launch.Qualification)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	manifest, err := readRuntimeMetadata(filepath.Join(artifacts, "qualification.json"), 0o444)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	observed, err := validateOpenShellRuntime(*current, name, details, raw, manifest)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	selected, err := readRuntimeMetadata(filepath.Join(control, "generations", observed.Record.Generation, "record.json"), 0o600)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	if !bytes.Equal(raw, selected) {
		return OpenShellRuntime{}, errors.New("runtime generation does not match its native current selection")
	}
	if err := ctx.Err(); err != nil {
		return OpenShellRuntime{}, err
	}
	return observed, nil
}

func readRuntimeMetadata(name string, mode os.FileMode) ([]byte, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errors.New("invalid native runtime metadata path")
	}
	// Walk every parent with no-follow I/O, starting at the trusted root, not
	// just the final path component. The selected native mount is independently
	// verified by the infrastructure caller; only root-owned metadata is read.
	raw, info, err := safepath.ReadFileInfo("/", strings.TrimPrefix(name, "/"), 1<<20)
	if err != nil {
		return nil, err
	}
	if err := runtimeMetadataOwner(info, mode, 0, 0); err != nil {
		return nil, err
	}
	return raw, nil
}

func runtimeMetadataOwner(info os.FileInfo, mode os.FileMode, uid, gid int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Nlink != 1 {
		return errors.New("native runtime metadata has unexpected ownership, links or mode")
	}
	return nil
}

func validateOpenShellRuntime(current spaceenv.Context, name string, details *container.Details, raw, manifest []byte) (OpenShellRuntime, error) {
	if details == nil || details.Privileged == nil || *details.Privileged {
		return OpenShellRuntime{}, errors.New("ordinary runtime metadata cannot substitute for a privileged/unknown route")
	}
	launch, ok := openShellRuntimeEvidence(details)
	if !ok || launch.Protocol != openshell.HelperProtocol {
		return OpenShellRuntime{}, errors.New("runtime launch protocol/binds are incomplete or unsupported")
	}
	control := filepath.Join(current.DiskMount, "runtime", "openshell", "containers", launch.Incarnation)
	if details.Name != name || details.Labels[LabelSpace] != current.Space || details.Labels[LabelAgentbox] != name || details.Labels[openshell.LabelControlRoot] != control {
		return OpenShellRuntime{}, errors.New("runtime container/control identity differs from native space/box")
	}
	release, err := openshell.LoadQualifiedRelease(manifest, launch.Qualification)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	if helper.BinarySHA256 != launch.HelperSHA256 {
		return OpenShellRuntime{}, errors.New("runtime does not select its qualified separate helper")
	}
	record, err := openshell.DecodeRuntimeRecord(raw, release, launch)
	if err != nil {
		return OpenShellRuntime{}, err
	}
	binding := record.Launch.Binding
	if binding.Space != current.Space || binding.Box != name || binding.DiskID != current.DiskID || binding.DiskUUID != current.DiskUUID ||
		record.Launch.UID != uint32(current.UID) || record.Launch.GID != uint32(current.GID) {
		return OpenShellRuntime{}, errors.New("runtime record is bound to another native disk/box/identity")
	}
	workdirBinds, socketBinds := 0, 0
	for _, mount := range details.Mounts {
		if mount.Destination == record.Launch.Workdir {
			workdirBinds++
			if mount.ReadOnly != record.Launch.WorkspaceReadOnly {
				return OpenShellRuntime{}, errors.New("runtime workdir bind differs from explicit read-only intent")
			}
		}
		if mount.Destination == sandbox.SocketDirContainer {
			socketBinds++
			if mount.Source != sandbox.SocketDirHost(name) || mount.ReadOnly {
				return OpenShellRuntime{}, errors.New("runtime public SSH bind differs from its protected native box directory")
			}
		}
	}
	if workdirBinds != 1 {
		return OpenShellRuntime{}, errors.New("runtime lacks its single actual workdir bind")
	}
	if socketBinds != 1 {
		return OpenShellRuntime{}, errors.New("runtime lacks its single protected public SSH directory bind")
	}
	// Existing containers survive a controller update. Their old generation is
	// verified as recorded; a read does not turn it into a fresh launch grant.
	// PrepareNative separately requires the current controller generation/source.
	if !nativeLowerHex(details.ID, 32) || details.PID < 0 {
		return OpenShellRuntime{}, errors.New("runtime lacks an observed container/process identity")
	}
	var started time.Time
	if details.StartedAt != "" {
		started, err = time.Parse(time.RFC3339Nano, details.StartedAt)
		if err != nil {
			return OpenShellRuntime{}, errors.New("runtime start timestamp is malformed")
		}
	}
	if details.Running && (details.PID <= 1 || started.IsZero()) {
		return OpenShellRuntime{}, errors.New("running runtime lacks its real PID/start instance")
	}
	hash := sha256.Sum256(raw)
	return OpenShellRuntime{ContainerID: details.ID, PID: details.PID, StartedAt: started, Running: details.Running, ControlRoot: control,
		RecordSHA256: hex.EncodeToString(hash[:]), Launch: launch, Record: record, Release: release}, nil
}

func nativeLowerHex(value string, size int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == size && hex.EncodeToString(raw) == value
}

func (r OpenShellRuntime) String() string {
	return fmt.Sprintf("OpenShell runtime metadata %s/%s (not launch authority)", r.Record.Launch.Binding.Box, r.Record.Generation)
}
func (r OpenShellRuntime) GoString() string { return r.String() }
