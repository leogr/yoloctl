package agentbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// nativeContainerPlan describes a new, not-yet-started container. It grants no
// launch/activation/cleanup authority. The lifecycle caller must own native
// admission, verify storage/artifacts and acknowledge its own fresh create
// before preparing private material or starting this container.
type nativeContainerPlan struct {
	launch        openshell.HelperLaunch
	binding       openshell.Binding
	uid, gid      uint32
	controlRoot   string
	workdirSource string
	readOnly      bool
	unconfined    bool
	privateEnv    string
	literalEnv    map[string]string
}

func (nativeContainerPlan) String() string     { return "OpenShell container plan [redacted]" }
func (p nativeContainerPlan) GoString() string { return p.String() }

// planNativeContainer leaves the container unstarted. After an acknowledged
// create, its inspected effective ENV supplies any image-provided private
// channel only when no account override exists. This preserves precedence
// without putting account secrets on container launch argv or on the supervisor.
// No caller/default is enabled by this metadata assembler alone.
func planNativeContainer(c *Config, image string, current spaceenv.Context, release openshell.QualifiedRelease, incarnation string, policy []byte) (container.RunOptions, nativeContainerPlan, error) {
	if c == nil || c.Privileged || c.Space != current.Space || current.UID <= 0 || current.GID <= 0 || !filepath.IsAbs(current.DiskMount) || filepath.Clean(current.DiskMount) != current.DiskMount {
		return container.RunOptions{}, nativeContainerPlan{}, errors.New("native container plan requires an ordinary box and selected native identity")
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return container.RunOptions{}, nativeContainerPlan{}, err
	}
	sum := sha256.Sum256(policy)
	plan := nativeContainerPlan{
		launch:  openshell.HelperLaunch{Protocol: openshell.HelperProtocol, Workdir: c.Workdir.ContainerPath(), Qualification: release.Identity(), HelperSHA256: helper.BinarySHA256, Incarnation: incarnation},
		binding: openshell.Binding{Space: c.Space, DiskID: current.DiskID, DiskUUID: current.DiskUUID, Box: c.Name, Incarnation: incarnation, ControllerGeneration: current.Generation, Qualification: release.Identity(), PolicySHA256: hex.EncodeToString(sum[:])},
		uid:     uint32(current.UID), gid: uint32(current.GID), workdirSource: c.Workdir.MountSource(), readOnly: !c.WorkdirWritable(), unconfined: c.UnconfinedNetwork,
		controlRoot: filepath.Join(current.DiskMount, "runtime", "openshell", "containers", incarnation),
	}
	if err := plan.binding.Validate(); err != nil {
		return container.RunOptions{}, nativeContainerPlan{}, err
	}
	args, err := plan.launch.Args()
	if err != nil || plan.workdirSource == "" {
		return container.RunOptions{}, nativeContainerPlan{}, errors.New("native container plan requires its exact helper and actual workdir")
	}
	// Validate the explicit identity/cwd/read-only contract before resolving
	// account secrets or returning any create options.
	launchConfig, err := openshell.NewLaunchConfig(plan.bootstrap(nil), release, helper.BinarySHA256, plan.unconfined)
	if err != nil {
		return container.RunOptions{}, nativeContainerPlan{}, err
	}
	if err := launchConfig.ValidateWorkspacePolicy(policy); err != nil {
		return container.RunOptions{}, nativeContainerPlan{}, err
	}
	environment, private, err := resolveLaunchEnvironment(c)
	if err != nil {
		return container.RunOptions{}, nativeContainerPlan{}, err
	}
	plan.privateEnv = private
	// The separate supervisor does not inherit the boundary's container ENV.
	// Snapshot declared workload literals for its child bootstrap as well;
	// runtime-control keys retain the existing reserved namespace filtering.
	plan.literalEnv = make(map[string]string, len(c.Env))
	for key, value := range c.Env {
		if !strings.HasPrefix(key, "OPENSHELL_") {
			plan.literalEnv[key] = value.Literal
		}
	}
	options, err := buildRunOptionsWithEnv(c, image, nil, environment)
	if err != nil {
		return container.RunOptions{}, nativeContainerPlan{}, err
	}
	options.Command = append([]string{sandbox.EntrypointShimPath}, args...)
	options.Sysctl = nil // new generations use SSH forwarding, never legacy DNAT
	for key, value := range map[string]string{
		openshell.LabelProtocol: plan.launch.Protocol, openshell.LabelQualification: plan.launch.Qualification,
		openshell.LabelHelperSHA256: plan.launch.HelperSHA256, openshell.LabelIncarnation: incarnation, openshell.LabelControlRoot: plan.controlRoot,
	} {
		options.Labels[key] = value
	}
	helperRoot, _ := openshell.HelperSourceDirectory(helper.BinarySHA256)
	artifacts, _ := openshell.RuntimeDirectory(release.Identity())
	mounts := make([]container.Mount, 0, len(options.Mounts)+1)
	removedBinary, removedRego := false, false
	for _, mount := range options.Mounts {
		if !removedBinary && mount.Destination == sandbox.BinaryDirContainerPath && mount.Source == sandbox.BinaryDirHostPath && mount.ReadOnly {
			removedBinary = true
			continue
		}
		if !removedRego && mount.Destination == sandbox.PolicyContainerRegoPath && mount.Source == sandbox.PolicyHostRegoPath && mount.ReadOnly {
			removedRego = true
			continue
		}
		target := filepath.Clean(mount.Destination)
		for _, protected := range []string{"/.openshell", openshell.HelperDirectory} {
			if target == protected || strings.HasPrefix(target, protected+"/") || strings.HasPrefix(protected, strings.TrimSuffix(target, "/")+"/") {
				return container.RunOptions{}, nativeContainerPlan{}, errors.New("mount conflicts with exact native runtime infrastructure")
			}
		}
		mounts = append(mounts, mount)
	}
	options.Mounts = append(mounts,
		container.Mount{Source: helperRoot, Destination: openshell.HelperDirectory, ReadOnly: true},
		container.Mount{Source: artifacts, Destination: openshell.ArtifactProjection, ReadOnly: true},
		container.Mount{Source: plan.controlRoot, Destination: openshell.NativeProjection})
	return options, plan, nil
}

func (p nativeContainerPlan) bootstrap(environment map[string]string) openshell.BootstrapInput {
	return openshell.BootstrapInput{Binding: p.binding, UID: p.uid, GID: p.gid, Workdir: p.launch.Workdir, ReadOnlyWorkdir: p.readOnly, Environment: environment}
}

// bootstrapForCreated is read-only metadata validation for an acknowledged
// fresh create, never proof of that historical operation itself. Starting an
// existing container or adopting a running/stopped incarnation is not allowed.
func (p nativeContainerPlan) bootstrapForCreated(d *container.Details) (openshell.BootstrapInput, error) {
	if !nativeUnstartedCleanupObservation(d) {
		return openshell.BootstrapInput{}, errors.New("private bootstrap requires this unstarted created container")
	}
	if d.StartedAt != "" {
		started, err := time.Parse(time.RFC3339Nano, d.StartedAt)
		if err != nil || !started.IsZero() {
			return openshell.BootstrapInput{}, errors.New("private bootstrap cannot adopt a previously started container")
		}
	}
	if err := p.validateContainer(d); err != nil {
		return openshell.BootstrapInput{}, err
	}
	raw := p.privateEnv
	if raw == "" {
		for _, entry := range d.Env {
			if key, value, ok := strings.Cut(entry, "="); ok && key == openshellUserEnvironment {
				raw = value
			}
		}
	}
	environment := map[string]string{}
	if raw != "" {
		if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &environment) != nil || environment == nil {
			return openshell.BootstrapInput{}, errors.New("private workload environment is not a bounded string map")
		}
	}
	// The private account/envelope map overrides ordinary literals, as on the
	// legacy launch path. Only declared literals join it, never unrelated image
	// ENV or the helper's own ambient environment.
	for key, value := range p.literalEnv {
		if _, overridden := environment[key]; !overridden {
			environment[key] = value
		}
	}
	for key, value := range environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return openshell.BootstrapInput{}, errors.New("private workload environment contains an invalid entry")
		}
	}
	return p.bootstrap(environment), nil
}

// validateContainer checks the created container's identity (name and
// labels), as the old path did. Its launch/bind comparison is a named warning,
// never a refusal (parity with the old path): engines may report bind paths
// differently, for example on Lima.
func (p nativeContainerPlan) validateContainer(d *container.Details) error {
	if d == nil || !nativeLowerHex(d.ID, 32) || d.Name != p.binding.Box || d.Labels[LabelSpace] != p.binding.Space || d.Labels[LabelAgentbox] != p.binding.Box || d.Labels[openshell.LabelControlRoot] != p.controlRoot || DecodeLabels(d.Labels).UnconfinedNetwork != p.unconfined {
		return errors.New("container differs from the native plan identity")
	}
	if d.Privileged != nil && *d.Privileged {
		return errors.New("created container is privileged, not the planned confined box")
	}
	if err := p.compareLaunchAndBinds(d); err != nil {
		nativeCreateNotice(d.ID, fmt.Errorf("box %s: %w", p.binding.Box, err))
	}
	return nil
}

func (p nativeContainerPlan) compareLaunchAndBinds(d *container.Details) error {
	launch, ok := openShellRuntimeEvidence(d)
	if !ok || launch != p.launch || ClassifyRuntime(d).Mode != RuntimeModeConfined {
		return errors.New("created container differs from the selected native launch/binds")
	}
	workdirs, sockets := 0, 0
	for _, mount := range d.Mounts {
		switch mount.Destination {
		case p.launch.Workdir:
			workdirs++
			if !sameNativePath(mount.Source, p.workdirSource) || mount.ReadOnly != p.readOnly {
				return errors.New("created workdir differs from explicit mount/read-only intent")
			}
		case sandbox.SocketDirContainer:
			sockets++
			if !sameNativePath(mount.Source, sandbox.SocketDirHost(p.binding.Box)) || mount.ReadOnly {
				return errors.New("created public SSH bind differs from the owned box directory")
			}
		}
	}
	if workdirs != 1 || sockets != 1 {
		return errors.New("created container lacks its exact workdir/public SSH binds")
	}
	return nil
}

// nativeCreateNotice prints a create/recreate warning once per container and
// message; tests replace it.
var nativeCreateNotice = func() func(string, error) {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(id string, err error) {
		key := id + "\x00" + err.Error()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) > 256 {
			clear(seen)
		}
		if !seen[key] {
			seen[key] = true
			_, _ = fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
}()
