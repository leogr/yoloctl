package agentbox

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// PrivilegedAuthority is the shared warning for every surface that selects or
// reports a privileged box (Config.Privileged).
const PrivilegedAuthority = "space-VM root authority: every box, account credential and native state on the VM, its declarations and controller/runtime state, and every writable host grant; OpenShell filesystem, syscall and network confinement is absent. Host-enforced share roots, read-only shares and read-only drives remain. Setting privileged: false and recreating does not undo VM-wide changes or loaded modules. " + PrivilegedOperatorAccess

// PrivilegedOperatorAccess states who reaches PrivilegedAuthority through a
// running privileged box, and who may launch one.
const PrivilegedOperatorAccess = "While the box runs, every " + operator.Capability + " box in this space can exec in, attach to, use, restart and recreate it and so gain this authority; only the host can start it when it is new or stopped"

// RuntimeFeatures is the final image recipe, resolved in one ordered pass:
// the declared and profile features (EffectiveFeaturesStrict), then the
// internal sudo installer for a privileged box. The installer requires no
// capabilities, so nothing feeds back into feature or capability resolution.
// Image identity, builds and resolved-image drift all use it.
func RuntimeFeatures(c *Config) ([]string, error) {
	names, err := EffectiveFeaturesStrict(c)
	if err != nil {
		return names, err
	}
	if c.Privileged {
		names = append(names, features.PrivilegedRuntime)
	}
	return names, nil
}

// RuntimeMode is a container's inspected launch mode.
type RuntimeMode string

const (
	// RuntimeModeUnknown means the runtime evidence is absent or inconsistent.
	RuntimeModeUnknown RuntimeMode = ""
	// RuntimeModeConfined is an unprivileged container running openshell-sandbox.
	RuntimeModeConfined RuntimeMode = "confined"
	// RuntimeModePrivileged is a privileged container running the endpoint.
	RuntimeModePrivileged RuntimeMode = "privileged"
)

// RuntimeEvidence is what the runtime reports about a box's launch mode. A
// label alone never proves privilege or its absence.
type RuntimeEvidence struct {
	// Mode is privileged only when the runtime's privileged record, the
	// recorded PID 1 argv, the read-only endpoint bind, the mode label and a
	// launcher label naming the argv's protocol all agree; confined only when
	// none of them indicates privilege.
	Mode RuntimeMode
	// Reason explains an unknown mode.
	Reason string
	// AnyPrivileged reports any single privileged signal, including partial,
	// malformed or stale ones. Authority checks treat it as privileged.
	AnyPrivileged bool
	// Launcher is the launcher identity recorded at creation.
	Launcher string
	// Protocol is the endpoint protocol in the recorded PID 1 argv, empty when
	// the argv is not a well-formed endpoint launch.
	Protocol string
}

// ClassifyRuntime derives RuntimeEvidence from inspected container details.
func ClassifyRuntime(d *container.Details) RuntimeEvidence {
	if d == nil {
		return RuntimeEvidence{Reason: "runtime details unavailable"}
	}
	labels := DecodeLabels(d.Labels)
	launch := classifyLaunch(d.Command)
	bind := false
	for _, m := range d.Mounts {
		if m.Destination == boxendpoint.BinaryDir {
			bind = bind || m.Source == boxendpoint.BinaryDir && m.ReadOnly
		}
	}
	ev := RuntimeEvidence{Launcher: labels.Launcher, Protocol: launch.protocol}
	recorded := d.Privileged
	ev.AnyPrivileged = labels.Privileged || labels.Launcher != "" || launch.endpoint || bind || recorded != nil && *recorded
	switch {
	case recorded == nil:
		ev.Reason = "the runtime does not report whether the container is privileged"
	case *recorded && labels.Privileged && launch.protocol != "" && bind && labels.Launcher == boxendpoint.LauncherFor(launch.protocol):
		ev.Mode = RuntimeModePrivileged
	case !*recorded && !ev.AnyPrivileged && hasOpenShellRuntimeSignals(d):
		if runtime, ok := openShellRuntimeEvidence(d); ok {
			ev.Mode = RuntimeModeConfined
			ev.Launcher = "openshell-helper/" + runtime.Protocol
			ev.Protocol = runtime.Protocol
		} else {
			ev.Reason = "OpenShell helper launch, exact-build binds and runtime labels disagree"
		}
	case !*recorded && !ev.AnyPrivileged && launch.confined:
		ev.Mode = RuntimeModeConfined
	default:
		ev.Reason = "privileged setting, launch command and protocol, endpoint bind and labels disagree"
	}
	return ev
}

type launchEvidence struct {
	endpoint bool   // the argv invokes the endpoint, well-formed or not
	protocol string // its protocol, when the argv is well-formed
	confined bool   // the argv invokes openshell-sandbox
}

func classifyLaunch(args []string) launchEvidence {
	if len(args) > 0 && args[0] == sandbox.EntrypointShimPath {
		args = args[1:]
	}
	switch {
	case len(args) > 0 && (args[0] == boxendpoint.Binary || len(args) > 1 && args[1] == boxendpoint.Command):
		ev := launchEvidence{endpoint: true}
		if len(args) > 1 && args[0] == boxendpoint.Binary && args[1] == boxendpoint.Command {
			if launch, err := boxendpoint.ParseLaunch(args[2:]); err == nil {
				ev.protocol = launch.Protocol
			}
		}
		return ev
	case len(args) > 0 && args[0] == sandbox.BinaryDirContainerPath+"/openshell-sandbox":
		return launchEvidence{confined: true}
	}
	return launchEvidence{}
}

// EndpointBinary is the verified endpoint executable bound into a box.
type EndpointBinary struct {
	SHA256 string
}

// InspectEndpointBinaryFn verifies the VM's endpoint binary before a
// privileged container is created or started. Tests replace it.
var InspectEndpointBinaryFn = InspectEndpointBinary

// InspectEndpointBinary requires a root-owned, non-writable directory chain
// and a static Linux executable for this VM's architecture. The endpoint runs
// with the box image's filesystem, so a dynamically linked development build
// cannot be assumed to load there; it is an explicit failure, not a fallback.
func InspectEndpointBinary(context.Context, *container.Client) (EndpointBinary, error) {
	if !spaceenv.Native() {
		return EndpointBinary{}, errors.New("privileged boxes are launched by the space controller")
	}
	for dir := boxendpoint.BinaryDir; dir != "/"; dir = filepath.Dir(dir) {
		if err := rootOwnedPath(dir, true); err != nil {
			return EndpointBinary{}, err
		}
	}
	if err := rootOwnedPath(boxendpoint.Binary, false); err != nil {
		return EndpointBinary{}, err
	}
	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
	}
	if _, err := guestbinary.LinuxExecutable(boxendpoint.Binary, machine, false); err != nil {
		return EndpointBinary{}, fmt.Errorf("endpoint %s: %w; install a static make build bundle", boxendpoint.Binary, err)
	}
	sum, err := guestbinary.SHA256File(boxendpoint.Binary)
	if err != nil {
		return EndpointBinary{}, err
	}
	return EndpointBinary{SHA256: sum}, nil
}

func rootOwnedPath(path string, dir bool) error {
	kind := map[bool]string{true: "directory", false: "executable"}[dir]
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("endpoint path %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	mode := info.Mode()
	if !ok || st.Uid != 0 || mode.Perm()&0o022 != 0 || dir != mode.IsDir() || !dir && (!mode.IsRegular() || mode.Perm()&0o111 == 0) {
		return fmt.Errorf("endpoint path %s must be a root-owned %s writable only by root", path, kind)
	}
	return nil
}

type runtimeModeChangeKey struct{}

// WithRuntimeModeChange marks an explicit box recreate: the only request that
// may switch a box between confined and privileged launches. Reconcile, set,
// edit, rebuild, start, attach and chat recreation never do so on their own.
func WithRuntimeModeChange(ctx context.Context) context.Context {
	return context.WithValue(ctx, runtimeModeChangeKey{}, true)
}

// RuntimeModeChangeAllowed reports whether ctx carries WithRuntimeModeChange.
func RuntimeModeChangeAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(runtimeModeChangeKey{}).(bool)
	return allowed
}

// CheckRuntimeModeChange refuses replacing details with c's launch when that
// enables or removes privilege, unless the request is an explicit recreate.
// Repairing or updating an already privileged launch is not a mode change.
func CheckRuntimeModeChange(ctx context.Context, c *Config, details *container.Details) error {
	if RuntimeModeChangeAllowed(ctx) {
		return nil
	}
	if want, have := c.Privileged, ClassifyRuntime(details).AnyPrivileged; want != have {
		from, to := "confined", "privileged"
		if have {
			from, to = to, from
		}
		return fmt.Errorf("box %s would change from a %s to a %s launch; only an explicit `yoloctl box recreate %s --space %s` changes the launch mode", c.Name, from, to, c.Name, c.Space)
	}
	return nil
}
