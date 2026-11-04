package openshell

import (
	"errors"
	"strconv"
)

const (
	boundaryBootstrapPath           = "/.openshell/channel/sandbox/bootstrap.json"
	supervisorDescriptorPath        = "/.openshell/supervisor/config/runtime-descriptor.json"
	supervisorAuthPath              = "/.openshell/supervisor/config/auth.json"
	supervisorPolicyPath            = "/.openshell/supervisor/config/policy.yaml"
	supervisorRegoPath              = "/.openshell/supervisor/config/sandbox-policy.rego"
	supervisorSSHSocket             = "/.openshell/supervisor/run/ssh/ssh.sock"
	supervisorHealthSocket          = "/.openshell/supervisor/run/ready.sock"
	SupervisorUID            uint32 = 65534
	SupervisorGID            uint32 = 65534
)

// LaunchConfig is a nonsecret, root-owned record. Possessing its contents or
// constructing a value grants no authority: the runtime helper must verify the
// read-only record bind, exact helper/artifacts, generation and live mounts
// before invoking either binary. The native controller supplies effective
// WorkdirWritable intent; neither workload argv nor environment supplies it.
type LaunchConfig struct {
	Schema            int     `json:"schema"`
	Binding           Binding `json:"binding"`
	UID               uint32  `json:"uid"`
	GID               uint32  `json:"gid"`
	Workdir           string  `json:"workdir"`
	WorkspaceReadOnly bool    `json:"workspace_read_only"`
	UnconfinedNetwork bool    `json:"unconfined_network"`
	HelperSHA256      string  `json:"helper_sha256"`
}

func NewLaunchConfig(in BootstrapInput, release QualifiedRelease, helperSHA256 string, unconfinedNetwork bool) (LaunchConfig, error) {
	if err := in.validate(); err != nil {
		return LaunchConfig{}, err
	}
	if release.Identity() == "" || release.Identity() != in.Binding.Qualification {
		return LaunchConfig{}, errors.New("runtime bootstrap is not bound to the qualified patch-2 pair")
	}
	c := LaunchConfig{Schema: BootstrapSchema, Binding: in.Binding, UID: in.UID, GID: in.GID,
		Workdir: in.Workdir, WorkspaceReadOnly: in.ReadOnlyWorkdir, UnconfinedNetwork: unconfinedNetwork, HelperSHA256: helperSHA256}
	if err := c.Validate(); err != nil {
		return LaunchConfig{}, err
	}
	return c, nil
}

func (c LaunchConfig) Validate() error {
	if c.Schema != BootstrapSchema || !lowerHex(c.HelperSHA256, 32) || !lowerHex(c.Binding.Qualification, 32) {
		return errors.New("unsupported runtime bootstrap/helper qualification")
	}
	return (BootstrapInput{Binding: c.Binding, UID: c.UID, GID: c.GID, Workdir: c.Workdir, ReadOnlyWorkdir: c.WorkspaceReadOnly}).validate()
}

// SupervisorArgs contains only infrastructure paths, identity and the explicit
// idle child. Real managed workloads still start through the supervisor SSH
// endpoint and session holder. There is no arbitrary extra-argv parameter.
func (c LaunchConfig) SupervisorArgs() ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	args := []string{
		"--backend-descriptor-file", supervisorDescriptorPath,
		"--auth-bundle-file", supervisorAuthPath,
		"--sandbox-id", c.Binding.Box,
		"--policy-rules", supervisorRegoPath,
		"--policy-data", supervisorPolicyPath,
		"--workdir", c.Workdir,
		"--ssh-socket-path", supervisorSSHSocket,
		"--health-socket-path", supervisorHealthSocket,
		"--log-level", "info",
	}
	if c.WorkspaceReadOnly {
		args = append(args, "--workspace-read-only")
	}
	return append(args, "--", "sleep", "infinity"), nil
}

// BoundaryArgs never supplies launch-capability-free's optional workspace
// chown argument. Capability/namespace/firewall preparation precedes its use.
func (c LaunchConfig) BoundaryArgs() ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	args := []string{"launch-capability-free"}
	if c.UnconfinedNetwork {
		args = append(args, "--unmediated-network")
	}
	return append(args, strconv.FormatUint(uint64(c.UID), 10), strconv.FormatUint(uint64(c.GID), 10), boundaryBootstrapPath), nil
}
