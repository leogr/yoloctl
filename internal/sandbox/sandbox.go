package sandbox

import "github.com/leogr/yoloctl/internal/boxendpoint"

// Paths inside the env VM. These are populated by Lima provisioning
// (see assets/lima/yoloctl.yaml.tmpl) and bind-mounted into agentbox
// containers.
//
// The policy data (YAML) lives at a per-agentbox path —
// `AgentboxPolicyHostPath` in presets.go — written at create time
// from the chosen preset. Only the rego engine is shared across
// agentboxes (it's the policy interpreter; the data document is what
// varies between presets).
const (
	BinaryDirHostPath  = "/opt/openshell/bin"
	PolicyHostRegoPath = "/etc/yoloctl/sandbox-policy.rego"
)

// Paths inside the agentbox container. These are bind-mount targets and the
// arguments openshell-sandbox is launched with. `WorkdirDir` is `/workdir`,
// the in-box workdir namespace; a host workdir is bound at a path UNDER it
// (/workdir/<canonical>, see agentbox.Workdir.ContainerPath) while /workdir
// stays the granted parent subtree in the generated policy.
//
// EntrypointShimPath is baked into the agentbox base image
// (assets/containers/agentbox/yoloctl-entrypoint.sh); it runs as root,
// remaps the sandbox user's UID/GID from YOLOCTL_HOST_UID/GID, then
// exec's openshell-sandbox. No-op when those env vars are unset.
const (
	BinaryDirContainerPath  = "/opt/openshell/bin"
	PolicyContainerYAMLPath = "/etc/openshell/policy.yaml"
	PolicyContainerRegoPath = "/etc/openshell/policy.rego"
	SocketDirContainer      = "/run/openshell"
	SSHSocketPath           = SocketDirContainer + "/ssh.sock"
	WorkdirDir              = "/workdir"
	EntrypointShimPath      = "/usr/local/bin/yoloctl-entrypoint"
)

// AgentboxPolicyHostPath returns the VM-side path where the per-
// agentbox policy YAML lives. Bind-mounted into the container at
// the canonical /etc/openshell/policy.yaml path; the host-side path
// is per-agentbox so concurrent agentboxes don't share policy data.
func AgentboxPolicyHostPath(agentbox string) string {
	return AgentboxPolicyHostDir(agentbox) + "/policy.yaml"
}

// AgentboxPolicyHostDir is the parent directory of
// AgentboxPolicyHostPath. Useful for the `mkdir -p` before writing
// the policy YAML.
func AgentboxPolicyHostDir(agentbox string) string {
	return "/etc/yoloctl/agentboxes/" + agentbox
}

// InnerNetnsIP is the address openshell-sandbox assigns to the inner
// sandbox netns's veth peer (the outer side is 10.200.0.1). These are
// openshell-sandbox compile-time constants — every agentbox uses the
// same pair, scoped by the agentbox's outer container netns. Port
// forwarding DNATs incoming traffic at the outer netns's eth0 to
// InnerNetnsIP so it reaches the agent's listener inside the
// otherwise-hermetic inner netns.
const InnerNetnsIP = "10.200.0.2"

// SocketDirHost returns the per-agentbox SSH socket directory on the env VM.
// Bind-mounted into the container at SocketDirContainer so the VM host (and
// thus `yoloctl ssh-proxy`) can reach the openshell-sandbox SSH socket.
func SocketDirHost(agentbox string) string {
	return "/run/yoloctl/" + agentbox
}

// SocketHostPath is the per-agentbox SSH socket file path on the env VM.
func SocketHostPath(agentbox string) string {
	return SocketDirHost(agentbox) + "/ssh.sock"
}

// VMLocalAgentboxesDir is the parent of every per-agentbox VM-local
// workdir directory. Ephemeral-mode agentboxes (no host workdir grant)
// store /workdir here.
const VMLocalAgentboxesDir = "/var/lib/yoloctl/agentboxes"

// AgentboxDirHost returns the per-agentbox ephemeral workdir directory on
// the env VM, bind-mounted into the container at WorkdirDir. Lives on the
// VM disk so files persist across container restarts and can be inspected
// from the VM.
func AgentboxDirHost(agentbox string) string {
	return VMLocalAgentboxesDir + "/" + agentbox
}

// EntrypointArgs returns the argv for openshell-sandbox as PID 1.
//
// workdir is openshell-sandbox's --workdir: the agent's working directory
// inside the container. Pass agentbox.Workdir.ContainerPath — bare
// /workdir for ephemeral, or the host workdir mirrored under
// /workdir/<canonical> for a host workdir, so an agent that
// keys native repository-trust state by cwd sees a distinct path per repo.
//
// The optional child argv is passed after `--` and becomes the supervised
// process. For a detached agentbox this is typically `sleep infinity` —
// without it openshell-sandbox launches its default bash child, which exits
// immediately when there's no TTY and brings the whole container down.
// Interactive sessions attach later via the SSH socket and run their own
// processes, not this PID-1 child.
//
// unconfinedNetwork selects openshell-sandbox's --mode. false (the default)
// runs both supervisor components ("network,process"): the inner netns + L7
// egress proxy enforce the composed network policy (default-deny egress). true
// runs "--mode process", which drops ONLY the network component — no netns and
// no proxy are created and the policy's network_policies go unused, so the
// child inherits the container's ambient VM network with open outbound egress.
// Every OTHER layer is unchanged in either mode: Landlock, seccomp, and the
// drop to the sandbox user ride the process component, as does the embedded SSH
// daemon (gated on --ssh-socket-path, not on the network component — verified
// against openshell-sandbox v0.0.72), so attach/exec/session are unaffected.
// The --policy-rules/--policy-data pair stays REQUIRED in either mode (it still
// drives the filesystem/landlock/process blocks). This is the opt-in
// unconfined-network knob (Config.UnconfinedNetwork); box show / box inspect
// surface the egress trade-off loudly.
func EntrypointArgs(workdir string, unconfinedNetwork bool, child ...string) []string {
	if workdir == "" {
		workdir = WorkdirDir
	}
	args := []string{
		EntrypointShimPath,
		BinaryDirContainerPath + "/openshell-sandbox",
		"--policy-rules", PolicyContainerRegoPath,
		"--policy-data", PolicyContainerYAMLPath,
		"--workdir", workdir,
		"--ssh-socket-path", SSHSocketPath,
		"--log-level", "info",
	}
	if unconfinedNetwork {
		// Drop the network supervisor component: no inner netns, no L7
		// proxy — the agent uses the container's ambient network. The SSH
		// daemon, Landlock, and seccomp ride the process component and stay on.
		args = append(args, "--mode", "process")
	}
	if len(child) > 0 {
		args = append(args, "--")
		args = append(args, child...)
	}
	return args
}

// PrivilegedEntrypointArgs returns PID 1's argv for a privileged box
// (privileged: true): the same UID/GID shim, then yoloctl's own endpoint
// serving the same in-container socket. Its binary comes from the read-only
// boxendpoint.BinaryDir bind, never from the image. There is no child argv:
// the endpoint idles until a client opens a session.
func PrivilegedEntrypointArgs(workdir string) []string {
	if workdir == "" {
		workdir = WorkdirDir
	}
	return append([]string{EntrypointShimPath}, boxendpoint.Args(workdir, SSHSocketPath)...)
}
