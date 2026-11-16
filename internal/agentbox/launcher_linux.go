package agentbox

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

// PrivilegedMainSession asks a running privileged box's endpoint whether it
// takes the main-session request (contract M-b section 7): one request, no
// session runs. The current endpoint serves it; use the read-only probe by
// default. A nil hook keeps the transitional exec-only path.
var PrivilegedMainSession func(ctx context.Context, name string) (bool, error) = ProbeMainSession

// BoxLauncher reads how the inspected container of box name takes a managed
// session. It never treats an unreadable record as "old": that is an error.
func BoxLauncher(ctx context.Context, client *container.Client, name string, details *container.Details) (LauncherSupport, error) {
	if details == nil {
		return LauncherSupport{}, errors.New("launcher: no container details")
	}
	ev := ClassifyRuntime(details)
	switch {
	case ev.Mode == RuntimeModePrivileged:
		if !details.Running {
			return LauncherSupport{}, nil // its next start runs the current endpoint
		}
		if PrivilegedMainSession == nil {
			return LauncherSupport{Exec: true}, nil
		}
		ok, err := PrivilegedMainSession(ctx, name)
		if err != nil {
			return LauncherSupport{}, err
		}
		return LauncherSupport{NeedsRestart: !ok, Privileged: true}, nil
	case hasOpenShellRuntimeSignals(details):
		runtime, err := inspectOpenShellRuntime(ctx, client, name, details.ID)
		if err != nil {
			return LauncherSupport{}, err
		}
		capable, err := openshell.ContainerLauncherCapable(runtime.ControlRoot)
		if err != nil {
			return LauncherSupport{}, err
		}
		return LauncherSupport{NeedsRecreate: !capable}, nil
	case classifyLaunch(details.Command).confined:
		// The old upstream runtime (legacy branch): never the launcher, and
		// never started or restarted again.
		return LauncherSupport{NeedsRecreate: true, LegacyRuntime: true}, nil
	}
	return LauncherSupport{}, errors.New("launcher: the box's runtime cannot be classified (" + ev.Reason + ")")
}

// MainRecord names the generation a confined box's holder starts its main
// session on, for the holder's exit-status fallback: that generation's
// record.json and the hash of the record this inspection verified for the
// exact container details.ID (native control root and current selection
// checked; never chosen by name alone).
func MainRecord(ctx context.Context, client *container.Client, name string, details *container.Details) (string, string, error) {
	if details == nil || details.ID == "" {
		return "", "", errors.New("main record: no container details")
	}
	runtime, err := inspectOpenShellRuntime(ctx, client, name, details.ID)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(runtime.ControlRoot, "generations", runtime.Record.Generation, "record.json"), runtime.RecordSHA256, nil
}
