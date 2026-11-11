package agentbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Label keys used by yoloctl-managed containers. Constants live here
// because internal/agentbox is the canonical owner of agentbox
// identity in the new reconcile model; cmd/yoloctl has its own copies
// today that the CLI rewiring commit will collapse onto these.
const (
	LabelAgentbox = "yoloctl.agentbox"
	LabelSpace    = "yoloctl.space"
	// LabelAgent records the agent profile (e.g. "claude") this
	// agentbox hosts. Set at create time from cfg.Agent. The lookup
	// key for `yoloctl agent update <profile>` bulk filtering — that
	// command lists containers carrying this label rather than
	// trying to infer the profile from the image string. Empty/
	// missing means "generic agentbox without a registered agent
	// profile".
	LabelAgent = "yoloctl.agent"
	// LabelEnvKeys records the sorted, comma-joined list of yoloctl-MANAGED
	// env var KEYS (the keys only — non-secret — never the values) set at
	// create time. The reconciler reads it back so a key REMOVED from the
	// agentbox YAML triggers a recreate; without it a removed var would
	// linger in the running container until a manual recreate. Absent when
	// the agentbox declares no env.
	LabelEnvKeys = "yoloctl.env_keys"
)

// SandboxSocketTimeout is how long Create / Recreate / Restart (and
// the cmd-side post-start guard for existing containers) wait for
// openshell-sandbox's SSH endpoint to answer before giving up.
const SandboxSocketTimeout = 30 * time.Second

// LiveInspector implements StateInspector against a real Lima VM via
// internal/container + internal/port. Each Inspect call issues:
//   - one nerdctl ps --filter to detect existence
//   - one nerdctl inspect for container metadata
//   - a legacy ReadFile or verified native versioned policy read
//   - one systemctl list-units to enumerate port forwards
//
// Reads remain serial and bounded; native metadata is read by the already
// running controller on its independently verified native storage.
type LiveInspector struct {
	Client *container.Client
}

// Inspect reads the agentbox's actual state from the VM. Returns
// ActualState{Exists: false} (no error) when no container with the
// matching label is found.
func (i LiveInspector) Inspect(ctx context.Context, c *Config) (ActualState, error) {
	labelExpr := fmt.Sprintf("%s=%s", LabelAgentbox, c.Name)
	list, err := i.Client.List(ctx, labelExpr)
	if err != nil {
		return ActualState{}, fmt.Errorf("list containers: %w", err)
	}
	if len(list) == 0 {
		return ActualState{Exists: false}, nil
	}
	details, err := i.Client.Inspect(ctx, c.Name)
	if err != nil {
		return ActualState{}, fmt.Errorf("inspect container %s: %w", c.Name, err)
	}
	state := ActualState{
		Exists:            true,
		Image:             details.Image,
		Env:               details.Env,
		Labels:            details.Labels,
		CPUs:              details.CPUs,
		Memory:            details.Memory,
		WorkloadPoolKnown: true,
		WorkloadPool:      details.Labels[resources.WorkloadLabel] == resources.WorkloadVersion,
	}
	// Record which env keys yoloctl managed last time (label present) so
	// Diff can detect a removed managed key. Label absent → nil → Diff
	// keeps the conservative legacy behavior (no removal detection).
	if v, ok := details.Labels[LabelEnvKeys]; ok {
		state.ManagedEnvKeys = splitEnvKeys(v)
	}
	// Decode the account NAME the container was created against (non-secret
	// label). Diff compares it against the desired Config.Account to detect
	// an account RE-POINT. A generic-shell container carries no account
	// label → Account "", which Diff treats as "no account to compare".
	ls := DecodeLabels(details.Labels)
	if desired := details.Labels["yoloctl.runtime_image"]; desired != "" {
		state.Image = desired
	}
	state.Account = ls.Account
	state.UnconfinedNetwork = ls.UnconfinedNetwork
	state.Runtime = ClassifyRuntime(details)
	_, newRuntime := openShellRuntimeEvidence(details)
	newRuntime = newRuntime && state.Runtime.Mode == RuntimeModeConfined
	workdirMount, err := liveWorkdirMount(details)
	if err != nil {
		return ActualState{}, fmt.Errorf("inspect container %s workdir: %w", c.Name, err)
	}
	for index, m := range details.Mounts {
		switch {
		case index == workdirMount:
			// The persisted launch argv identifies the original workdir,
			// independently of the current declaration. Other /workdir
			// mounts remain extras, including another mirrored project.
			state.ReadOnlyWD = m.ReadOnly
			state.WorkdirSource = m.Source
			state.WorkdirDest = m.Destination
		case m.Destination == sandbox.BinaryDirContainerPath,
			m.Destination == sandbox.PolicyContainerYAMLPath,
			m.Destination == sandbox.PolicyContainerRegoPath,
			m.Destination == sandbox.SocketDirContainer:
			// canonical fixed infra mounts; not user-declared.
		case m.Destination == boxendpoint.BinaryDir && m.Source == boxendpoint.BinaryDir && m.ReadOnly:
			// The privileged endpoint bind; ClassifyRuntime reports it.
		case newRuntime && (m.Destination == openshell.HelperDirectory || m.Destination == openshell.ArtifactProjection || m.Destination == openshell.NativeProjection):
			// Exact-build runtime infrastructure, checked together with argv
			// and labels above. An inconsistent bind remains visible drift.
		case m.Destination == operator.Mount:
			state.OperatorEnabled = true
		case m.Destination == observe.ContainerDir:
			// Controller infrastructure, not a declared mount: a container
			// without it never gains a recreate reason.
			state.ObservationEnabled = true
		case m.Destination == operator.HelperMount, m.Destination == operator.CodexSkill,
			m.Destination == operator.ClaudeSkill, m.Destination == operator.GeminiSkill:
		case managedchat.IsProjectionTarget(m.Destination):
			state.ChatMounts = append(state.ChatMounts, Mount{Source: m.Source, Target: m.Destination, ReadOnly: m.ReadOnly})
		default:
			state.Mounts = append(state.Mounts, Mount{
				Source:   m.Source,
				Target:   m.Destination,
				ReadOnly: m.ReadOnly,
			})
		}
	}
	policy, err := readInspectedRuntimePolicy(ctx, details,
		func(read context.Context) ([]byte, error) {
			return readNativeRuntimePolicy(read, i.Client, c.Name, details)
		},
		func(read context.Context) ([]byte, error) {
			return i.Client.ReadFile(read, sandbox.AgentboxPolicyHostPath(c.Name))
		})
	if err != nil {
		return state, fmt.Errorf("read policy file: %w", err)
	}
	state.PolicyContent = policy
	specs, err := port.List(ctx, i.Client, c.Name)
	if err != nil {
		return state, fmt.Errorf("list port forwards: %w", err)
	}
	state.PortForwards = make([]Port, 0, len(specs))
	for _, s := range specs {
		// The socat units encode only published+target; host_ip/protocol are
		// the resolved defaults (127.0.0.1/tcp) — left omitted on the Port so
		// the diff folds defaults in.
		state.PortForwards = append(state.PortForwards, Port{
			Published: s.HostPort,
			Target:    s.ContainerPort,
		})
	}
	sort.Slice(state.PortForwards, func(i, j int) bool {
		return state.PortForwards[i].Published < state.PortForwards[j].Published
	})
	return state, nil
}

// liveWorkdirMount identifies the primary bind using the container's recorded
// launch command (inspect Path + Args, not image defaults such as
// Config.WorkingDir): OpenShell's or the privileged endpoint's --workdir.
// Neither mount order nor the desired declaration identifies an old workdir.
// Uncertain metadata must stop reconciliation, never authorize a recreation.
func liveWorkdirMount(details *container.Details) (int, error) {
	workdir, err := launchWorkdir(details.Command)
	if err != nil {
		return -1, err
	}
	index := -1
	for n, mount := range details.Mounts {
		if mount.Destination != workdir {
			continue
		}
		if index != -1 {
			return -1, errors.New("duplicate binds for sandbox workdir")
		}
		if mount.Source == "" {
			return -1, errors.New("sandbox workdir bind has no source")
		}
		index = n
	}
	if index == -1 {
		return -1, errors.New("sandbox workdir bind is missing")
	}
	return index, nil
}

func launchWorkdir(command []string) (string, error) {
	args := command
	if len(args) > 0 && args[0] == sandbox.EntrypointShimPath {
		args = args[1:]
	}
	if len(args) > 1 && args[0] == boxendpoint.Binary && args[1] == boxendpoint.Command {
		// Any recorded protocol: a superseded one is drift, not uncertainty.
		launch, err := boxendpoint.ParseLaunch(args[2:])
		if err != nil {
			return "", fmt.Errorf("unrecognized privileged launch arguments: %w", err)
		}
		return launch.Workdir, nil
	}
	if len(args) > 1 && args[0] == openshell.HelperBinary && args[1] == openshell.HelperCommand {
		launch, err := openshell.ParseHelperLaunch(args[2:])
		if err != nil {
			return "", fmt.Errorf("unrecognized OpenShell helper arguments: %w", err)
		}
		return launch.Workdir, nil
	}
	if len(args) == 0 || args[0] != sandbox.BinaryDirContainerPath+"/openshell-sandbox" {
		return "", errors.New("missing recognized sandbox launch command")
	}
	var workdir string
	for args = args[1:]; len(args) > 0 && args[0] != "--"; args = args[2:] {
		// These are the value-taking flags emitted by EntrypointArgs.
		switch args[0] {
		case "--policy-rules", "--policy-data", "--ssh-socket-path", "--log-level", "--mode", "--workdir":
		default:
			return "", errors.New("unrecognized sandbox launch arguments")
		}
		if len(args) < 2 || args[1] == "--" {
			return "", errors.New("incomplete sandbox launch arguments")
		}
		if args[0] == "--workdir" {
			if workdir != "" {
				return "", errors.New("duplicate sandbox workdir argument")
			}
			workdir = args[1]
			if (workdir != sandbox.WorkdirDir && !strings.HasPrefix(workdir, sandbox.WorkdirDir+"/")) || path.Clean(workdir) != workdir {
				return "", errors.New("invalid sandbox workdir argument")
			}
		}
	}
	if workdir == "" {
		return "", errors.New("missing sandbox workdir argument")
	}
	return workdir, nil
}

// LiveApplier implements Applier against a real Lima VM. Mirrors the
// strategies described in DiffPlan:
//   - Create: write policy file, run container, wait for sandbox
//     socket, apply every declared port forward.
//   - Recreate: tear down forwards, stop+remove the container, then
//     do Create.
//   - Restart: snapshot policy for rollback, tear down forwards,
//     stop, write new policy, start, wait for socket (rollback on
//     timeout), re-apply forwards.
//   - AddPortForward / RemovePortForward: thin wrappers over
//     internal/port for the live-update path.
type LiveApplier struct {
	nativeForwardRestoration *nativeForwardRestoration
	nativeCreation           *nativeCreationRelease
	Client                   *container.Client
	// VerifyStorage is the admitted controller's current native disk proof.
	// It is required by new OpenShell effects, never inferred from labels.
	VerifyStorage func(context.Context) error
}

// Create brings up a new agentbox container with the supplied policy.
func (a LiveApplier) Create(ctx context.Context, c *Config, policy []byte) error {
	if c.Workdir.MountSource() == "" {
		return errors.New("workdir is required to create an agentbox; the CLI fills this in at create time")
	}
	var endpointBinary *EndpointBinary
	if c.Privileged {
		// Verify before building or staging anything for this box.
		binary, err := InspectEndpointBinaryFn(ctx, a.Client)
		if err != nil {
			return fmt.Errorf("privileged box %s: %w", c.Name, err)
		}
		endpointBinary = &binary
	}
	// Build-or-select the feature-composed runtime image BEFORE staging policy,
	// so a build failure leaves no partial VM state. EnsureComposedImage builds
	// the base from its embedded context if missing (build-on-demand), then
	// composes the box's effective features on top — content-addressed and
	// cached, so a cached composition costs one `image inspect`. A no-install
	// set returns the base as-is; a non-catalog base falls through to nerdctl's
	// pull semantics. The returned ref is what nerdctl run actually runs.
	candidates, err := RuntimeFeatures(c)
	if err != nil {
		return err
	}
	plan, err := c.FeaturePlan()
	if err != nil {
		return err
	}
	if !c.Privileged && a.nativeCreation != nil {
		if err := a.nativeCreationPrerequisite(ctx); err != nil {
			return err
		}
		runtimeImage, err := agentboximage.RequirePreparedImage(ctx, a.Client, c.Image, candidates, agentboximage.Options{FeaturePlan: plan})
		if err != nil {
			return err
		}
		return a.createQualifiedNativeContainer(ctx, c, runtimeImage, policy)
	}
	runtimeImage, err := agentboximage.EnsureComposedImage(ctx, a.Client, c.Image, candidates, agentboximage.Options{Notice: agentboximage.LogNotices, FeaturePlan: plan})
	if err != nil {
		return fmt.Errorf("ensure runtime image for %s: %w", c.Name, err)
	}
	if err := stagePolicyOnVM(ctx, a.Client, c, policy); err != nil {
		return err
	}
	runOpts, err := buildRunOptions(c, runtimeImage, endpointBinary)
	if err != nil {
		return err
	}
	if err := addChatRunOptions(c, &runOpts); err != nil {
		return err
	}
	if err := addObservationRunOptions(c, &runOpts); err != nil {
		return err
	}
	enabled, err := OperatorEnabled(c)
	if err != nil {
		return err
	}
	var endpoint operator.Endpoint
	if enabled {
		endpoint, err = operator.Provision(ctx, c.Name)
		if err != nil {
			return err
		}
		runOpts.Labels[operator.IncarnationLabel] = endpoint.Incarnation
		runOpts.Mounts = append(runOpts.Mounts,
			container.Mount{Source: endpoint.Directory(), Destination: operator.Mount, ReadOnly: true},
			container.Mount{Source: operator.HelperMount, Destination: operator.HelperMount, ReadOnly: true},
			container.Mount{Source: operator.HelperMount + "/skill", Destination: operator.CodexSkill, ReadOnly: true},
			container.Mount{Source: operator.HelperMount + "/skill", Destination: operator.ClaudeSkill, ReadOnly: true},
			container.Mount{Source: operator.HelperMount + "/skill", Destination: operator.GeminiSkill, ReadOnly: true},
		)
	}
	if err := a.Client.Run(ctx, runOpts); err != nil {
		if enabled {
			_ = os.RemoveAll(endpoint.Directory())
		}
		return fmt.Errorf("nerdctl run: %w", err)
	}
	if err := operator.RemoveBox(c.Name, endpoint.Incarnation); err != nil {
		return fmt.Errorf("retire old operator endpoints: %w", err)
	}
	if err := WaitForSandboxSocket(ctx, c.Space, c.Name, SandboxSocketTimeout); err != nil {
		return err
	}
	return a.applyPortForwards(ctx, c, c.Ports)
}

// Recreate replaces an existing agentbox container. Used when a
// create-only nerdctl property (image, resources, mounts, ...)
// changed.
func (a LiveApplier) Recreate(ctx context.Context, c *Config, policy []byte) error {
	if err := a.Client.RequireWorkloadPool(ctx); err != nil {
		return err
	}
	if err := port.UnforwardAgentbox(ctx, a.Client, c.Name); err != nil {
		return fmt.Errorf("teardown port forwards: %w", err)
	}
	if err := a.Stop(ctx, c.Name); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	if err := a.Client.Remove(ctx, c.Name, false); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	// Best-effort: if the stop escalated to SIGKILL, the supervisor's
	// netns/veth leaked — collect them before bringing up the
	// replacement.
	_ = sandbox.ReapOrphanedNetns(ctx, c.Space, os.Stderr)
	return a.Create(ctx, c, policy)
}

// Restart swaps policy without recreating the container. Native runtimes use
// a fresh generation and original private checkpoint for bounded rollback. The
// labelled legacy route retains its existing policy-file/socket behavior.
func (a LiveApplier) Restart(ctx context.Context, c *Config, policy []byte) error {
	policy = bytes.Clone(policy)
	native, err := a.preflightPolicyRestart(ctx, c, policy)
	if err != nil {
		return err
	}
	if native {
		return a.restartNativeGeneration(ctx, c, policy)
	}
	policyPath := sandbox.AgentboxPolicyHostPath(c.Name)
	snapshot, err := a.Client.ReadFile(ctx, policyPath)
	if err != nil {
		return fmt.Errorf("snapshot current policy: %w", err)
	}
	if err := port.UnforwardAgentbox(ctx, a.Client, c.Name); err != nil {
		return fmt.Errorf("teardown port forwards: %w", err)
	}
	if err := a.Stop(ctx, c.Name); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	// Best-effort: collect any netns/veth a SIGKILL-escalated stop
	// orphaned. The restarted supervisor creates a fresh netns, so
	// the old one is residue either way.
	_ = sandbox.ReapOrphanedNetns(ctx, c.Space, os.Stderr)
	if err := a.Client.WriteFile(ctx, policyPath, policy, "0644"); err != nil {
		// Never started the swap; restart with old policy still in
		// place so the agentbox isn't left stopped.
		_ = a.Client.Start(ctx, c.Name)
		return fmt.Errorf("write new policy: %w", err)
	}
	if err := a.Client.Start(ctx, c.Name); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	if err := WaitForSandboxSocket(ctx, c.Space, c.Name, SandboxSocketTimeout); err != nil {
		// New policy refused to load — restore snapshot. The forwards
		// were torn down above, so once the rolled-back container is up
		// again they must be re-applied too; otherwise a failed policy
		// swap silently leaves the agentbox without its declared
		// forwards while the YAML still claims them. Best-effort, like
		// the rest of the rollback: the policy error is what the caller
		// needs to see.
		if stopErr := a.Stop(ctx, c.Name); stopErr != nil {
			var uncertain *NativeEffectsUncertain
			if errors.As(stopErr, &uncertain) {
				return fmt.Errorf("new policy failed; rollback stop is uncertain: %w", errors.Join(err, stopErr))
			}
		}
		_ = a.Client.WriteFile(ctx, policyPath, snapshot, "0644")
		if startErr := a.Client.Start(ctx, c.Name); startErr == nil {
			if waitErr := WaitForSandboxSocket(ctx, c.Space, c.Name, SandboxSocketTimeout); waitErr == nil {
				_ = a.applyPortForwards(ctx, c, c.Ports)
			}
		}
		return fmt.Errorf("new policy failed to load; previous policy restored: %w", err)
	}
	return a.applyPortForwards(ctx, c, c.Ports)
}

// AddPortForward installs one forward against an already-running
// container, selecting the transport from inspected launch evidence.
func (a LiveApplier) AddPortForward(ctx context.Context, c *Config, pf Port) error {
	details, err := a.Client.Inspect(ctx, c.Name)
	if err != nil {
		return err
	}
	return dispatchRuntimePortForward(ctx, details, c, pf,
		func(ctx context.Context, spec port.Spec) error {
			return a.startNativePortForward(ctx, spec)
		},
		func(ctx context.Context, spec port.Spec) error {
			return port.Forward(ctx, a.Client, spec)
		})
}

// RemovePortForward tears down one forward.
func (a LiveApplier) RemovePortForward(ctx context.Context, c *Config, pf Port) error {
	return port.Unforward(ctx, a.Client, portSpec(c, pf))
}

// portSpec routes a privileged box's forwards directly to its container
// network namespace: it has no inner sandbox namespace. The declared mode
// matches the running one here, because a mode change is create-only drift.
func portSpec(c *Config, pf Port) port.Spec {
	return port.Spec{Agentbox: c.Name, HostPort: pf.Published, ContainerPort: pf.Target, Direct: c.Privileged}
}

func (a LiveApplier) applyPortForwards(ctx context.Context, c *Config, pfs []Port) error {
	for _, pf := range pfs {
		if err := a.AddPortForward(ctx, c, pf); err != nil {
			return fmt.Errorf("forward %d:%d: %w", pf.Published, pf.Target, err)
		}
	}
	return nil
}

// buildRunOptions converts a Config into the nerdctl run argument
// shape consumed by container.Client.Run. Mirrors the inlined
// construction in cmd/yoloctl/agentbox.go:createAgentbox — the CLI
// rewiring commit will dedupe. runtimeImage is the resolved image to
// run (the feature-composed ref, or the base when nothing composes);
// it is what the live inspector reads back as a.Image, so the drift
// comparison against ResolvedImage(c) agrees by construction.
//
// endpoint is non-nil exactly for a privileged box: the container then runs
// --privileged with yoloctl's endpoint as PID 1, bound read-only from the VM's
// binary directory, and records its mode and launcher. Every other option,
// including namespaces, resources and the child environment channel, is the
// same as for a confined box. Unused legacy program/Rego binds are omitted.
func buildRunOptions(c *Config, runtimeImage string, endpoint *EndpointBinary) (container.RunOptions, error) {
	envVars, err := resolveEnv(c)
	if err != nil {
		return container.RunOptions{}, err
	}
	return buildRunOptionsWithEnv(c, runtimeImage, endpoint, envVars)
}

// New native preparation separates private account overrides before building
// container argv. Legacy callers still use resolveEnv's historical projection.
func buildRunOptionsWithEnv(c *Config, runtimeImage string, endpoint *EndpointBinary, envVars []string) (container.RunOptions, error) {
	// Stamp the compact identity/reconciliation label set so the live box is
	// reconstructable from container labels alone (no private DB): kind,
	// space/box/agent, account name, managed env keys, and network posture.
	set := c.Labels()
	if endpoint != nil {
		set.Privileged, set.Launcher, set.LauncherSHA256 = true, boxendpoint.Launcher, endpoint.SHA256
	}
	labels, err := EncodeLabels(set)
	if err != nil {
		return container.RunOptions{}, err
	}
	labels[resources.WorkloadLabel] = resources.WorkloadVersion
	if desired, _, err := ResolvedImage(c); err != nil {
		return container.RunOptions{}, err
	} else {
		labels["yoloctl.runtime_image"] = desired
	}
	opts := container.RunOptions{
		Image:           runtimeImage,
		Name:            c.Name,
		Labels:          labels,
		AddCaps:         []string{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE", "SYSLOG"},
		SeccompProfile:  "unconfined",
		AppArmorProfile: "unconfined",
		User:            "0",
		CPUs:            c.Resources.CPUs,
		Memory:          c.Resources.Memory,
		WorkloadPool:    true,
		Mounts:          buildMounts(c),
		Env:             envVars,
		Sysctl:          []string{"net.ipv4.ip_forward=1"},
		Detach:          true,
		Command:         sandbox.EntrypointArgs(c.Workdir.ContainerPath(), c.UnconfinedNetwork, "sleep", "infinity"),
	}
	if endpoint != nil {
		opts.Privileged = true
		opts.Mounts = withoutLegacyOpenShellBinds(opts.Mounts)
		opts.Command = sandbox.PrivilegedEntrypointArgs(c.Workdir.ContainerPath())
		opts.Mounts = append(opts.Mounts, container.Mount{Source: boxendpoint.BinaryDir, Destination: boxendpoint.BinaryDir, ReadOnly: true})
	}
	return opts, nil
}

// managedEnvKeys returns the sorted keys of the yoloctl-managed env map.
// Stored in the LabelEnvKeys container label so a key removed from the
// agentbox YAML can be detected on the next diff (it would otherwise
// linger in the running container). Keys only — values stay out of labels.
func managedEnvKeys(env map[string]EnvVar) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// splitEnvKeys parses a LabelEnvKeys value (comma-joined keys) into a
// slice. An empty label yields a NON-nil empty slice so Diff can tell
// "yoloctl managed zero env keys" (label present) from "unknown" (label
// absent → nil → no removal detection).
func splitEnvKeys(v string) []string {
	if v == "" {
		return []string{}
	}
	return strings.Split(v, ",")
}

// buildMounts is the canonical mount list every agentbox gets, in
// order: openshell binary, per-agentbox policy YAML, shared rego
// engine, per-agentbox SSH socket dir, the workdir bind, then any
// extra mounts from the Config. The workdir destination mirrors the
// canonical host path under /workdir for host workdirs, bare
// /workdir for ephemeral — see Workdir.ContainerPath.
func buildMounts(c *Config) []container.Mount {
	mounts := []container.Mount{
		{Source: sandbox.BinaryDirHostPath, Destination: sandbox.BinaryDirContainerPath, ReadOnly: true},
		{Source: sandbox.AgentboxPolicyHostPath(c.Name), Destination: sandbox.PolicyContainerYAMLPath, ReadOnly: true},
		{Source: sandbox.PolicyHostRegoPath, Destination: sandbox.PolicyContainerRegoPath, ReadOnly: true},
		{Source: sandbox.SocketDirHost(c.Name), Destination: sandbox.SocketDirContainer},
		{Source: c.Workdir.MountSource(), Destination: c.Workdir.ContainerPath(), ReadOnly: !c.WorkdirWritable()},
	}
	for _, m := range c.Mounts {
		mounts = append(mounts, container.Mount{
			Source:      m.Source,
			Destination: m.Target,
			ReadOnly:    m.ReadOnly,
		})
	}
	return mounts
}

// resolveEnv renders Config.Env (literal, non-secret values) into "KEY=VALUE"
// strings the container runtime accepts, sorted for deterministic argv. Always
// appends YOLOCTL_HOST_UID / GID so the entrypoint shim can align the
// in-container `sandbox` user with the host developer's UID/GID at startup.
//
// Env is literal-only: a box does not own secret values, so
// there is no credential-store lookup here. Supported account secrets reach
// the agent through secret_injection; native credentials remain agent-owned in
// the account-state leaf. A secret-injection computation that cannot resolve
// its profile or load its account is a hard error: the create fails
// cleanly rather than launching the agent silently unauthenticated.
func resolveEnv(c *Config) ([]string, error) {
	out, userEnv, err := resolveLaunchEnvironment(c)
	if err != nil {
		return nil, err
	}
	if userEnv != "" {
		out = append(out, fmt.Sprintf("%s=%s", openshellUserEnvironment, userEnv))
	}
	return out, nil
}

// resolveLaunchEnvironment returns literal container ENV separately from the
// private account JSON channel, so new native launch argv never contains it.
// It resolves the account exactly once using the existing narrowing router.
func resolveLaunchEnvironment(c *Config) ([]string, string, error) {
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(c.Env)+2)
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, c.Env[k].Literal))
	}
	uid, gid := spaceenv.Identity()
	out = append(out,
		fmt.Sprintf("YOLOCTL_HOST_UID=%d", uid),
		fmt.Sprintf("YOLOCTL_HOST_GID=%d", gid),
	)
	// Account secrets the agent declares for env injection (secret_injection) are
	// delivered through the openshell-sandbox supervisor, which injects them into
	// the agent's ENVIRONMENT (not its command line). The value rides this
	// container env var as a JSON map; the supervisor reads it at startup.
	userEnv, err := userEnvironmentJSON(c)
	if err != nil {
		return nil, "", err
	}
	return out, userEnv, nil
}

// stagePolicyOnVM materializes the per-agentbox policy.yaml on the
// VM at sandbox.AgentboxPolicyHostPath. Creates the parent dir if
// missing. Used by Create (where the file may not exist yet);
// Restart writes directly via container.Client.WriteFile because the
// path is already in place.
func stagePolicyOnVM(ctx context.Context, c *container.Client, ab *Config, content []byte) error {
	dir := sandbox.AgentboxPolicyHostDir(ab.Name)
	if err := c.Shell(ctx, "sudo", "mkdir", "-p", dir); err != nil {
		return fmt.Errorf("mkdir policy dir: %w", err)
	}
	if err := c.WriteFile(ctx, sandbox.AgentboxPolicyHostPath(ab.Name), content, "0644"); err != nil {
		return fmt.Errorf("write policy: %w", err)
	}
	return nil
}
