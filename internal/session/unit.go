package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmservice"
	"gopkg.in/yaml.v3"
)

// Management of the holder's supervised service inside the
// space VM, following the yoloctl-port-* pattern (internal/port).

// UnitSpec describes one holder unit to start.
type UnitSpec struct {
	Agentbox string
	Argv     []string // agent command and args

	Term       string // optional; holder defaults apply
	Cols, Rows int    // optional initial PTY size
	RingBytes  int    // optional ring size override
	// TitleTemplate is the EXPANDED box title template for the holder's
	// OSC 0/1/2 rewrite (ExpandBoxTitleTemplate's output). Optional:
	// empty keeps the holder's default "<agentbox>: <title>". Passed as
	// one argv element through the guest service adapter.
	TitleTemplate string
	// TitleStrip is the agent icon the holder strips from the front of
	// agent titles (ExpandBoxTitleTemplate's strip). Optional.
	TitleStrip      string
	HostIntegration bool
	HostWorkdir     string // canonical host path; empty for ephemeral boxes
	// LaunchToken, when set, is written into the holder's marker and
	// tombstone so the controller can tell which launch they belong to.
	LaunchToken string
	// Daemons are the agent profile's declared daemons, which the holder
	// stops before the agent starts and after it exits.
	Daemons []string
}

// StartHolderUnit starts the session holder for spec.Agentbox as a
// supervised service on the VM. The fixed unit name is the
// double-spawn guard: both supervisors refuse to start a second
// service with the same name while one is active, so two concurrent
// run/attach calls cannot race two holders (and so two agents) into the
// same box.
//
// Holders never restart automatically. An automatic restart would spawn a
// fresh agent next to the orphaned one — openshell-sandbox has no
// reattach API (spike F2). Holder death must surface as "session lost",
// never self-heal into a duplicate agent.
func StartHolderUnit(ctx context.Context, c *container.Client, spec UnitSpec) error {
	args, err := holderUnitArgs(spec)
	if err != nil {
		return err
	}
	env := []string{"HOME=/root"}
	if spaceenv.Native() {
		env = append(env, spaceenv.ContextEnv+"="+spaceenv.ContextPath)
	}
	return vmservice.Start(ctx, c, vmservice.Unit{Name: UnitName(spec.Agentbox), Description: fmt.Sprintf("yoloctl agent session %s", spec.Agentbox), Args: args, Env: env})
}

// holderUnitArgs is the holder unit's command line for spec.
func holderUnitArgs(spec UnitSpec) ([]string, error) {
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("session: holder unit for %s needs an agent argv", spec.Agentbox)
	}
	args := []string{VMBinaryPath, "session-hold", spec.Agentbox}
	if spec.Term != "" {
		args = append(args, "--term", spec.Term)
	}
	if spec.Cols > 0 && spec.Rows > 0 {
		args = append(args, "--cols", fmt.Sprint(spec.Cols), "--rows", fmt.Sprint(spec.Rows))
	}
	if spec.RingBytes > 0 {
		args = append(args, "--ring-bytes", fmt.Sprint(spec.RingBytes))
	}
	if spec.TitleTemplate != "" {
		args = append(args, "--title-template", spec.TitleTemplate)
	}
	if spec.TitleStrip != "" {
		args = append(args, "--title-strip", spec.TitleStrip)
	}
	if spec.LaunchToken != "" {
		if !ValidLaunchToken(spec.LaunchToken) {
			return nil, fmt.Errorf("session: holder unit for %s has an invalid launch token", spec.Agentbox)
		}
		args = append(args, "--launch-token", spec.LaunchToken)
	}
	for _, d := range spec.Daemons {
		if _, err := agentprofile.DaemonArgv(d); err != nil {
			return nil, fmt.Errorf("session: holder unit for %s: %w", spec.Agentbox, err)
		}
		args = append(args, "--reap-daemon", d)
	}
	if spec.HostIntegration {
		args = append(args, "--host-integration")
		if spec.HostWorkdir != "" {
			args = append(args, "--host-workdir", spec.HostWorkdir)
		}
	}
	args = append(args, "--")
	return append(args, spec.Argv...), nil
}

// StopHolderUnit stops the holder unit, idempotently: a unit that is
// already gone is success. Per spike F1 this leaves the agent running —
// agent termination is the container stop that lifecycle commands
// perform alongside.
func StopHolderUnit(ctx context.Context, c *container.Client, agentbox string) error {
	return vmservice.Stop(ctx, c, UnitName(agentbox))
}

// HolderUnitActive reports whether the holder unit is active (the
// session is live). An unavailable probe reads as "not active". A subsequent
// start still goes through the supervisor's authoritative fixed-name exclusion.
func HolderUnitActive(ctx context.Context, c *container.Client, agentbox string) bool {
	out, err := vmservice.State(ctx, c, UnitName(agentbox))
	if err != nil {
		return false
	}
	state := strings.TrimSpace(out)
	return state == "active" || state == "activating"
}

// HolderUnitState reports whether the holder unit is active, and an error
// when the probe cannot tell: a transport or service-manager failure, or a
// state it does not know. Conversation-identity decisions use it instead of
// HolderUnitActive, whose unavailable probe reads as "not active".
func HolderUnitState(ctx context.Context, c *container.Client, agentbox string) (bool, error) {
	out, err := vmservice.State(ctx, c, UnitName(agentbox))
	if err != nil {
		return false, fmt.Errorf("probe session holder of %s: %w", agentbox, err)
	}
	switch state := strings.TrimSpace(out); state {
	case "active", "activating", "reloading", "deactivating":
		return true, nil
	case "inactive", "failed", "unknown":
		return false, nil
	default:
		return false, fmt.Errorf("probe session holder of %s: unknown unit state %q", agentbox, state)
	}
}

// ProbeVMMarker reads the agentbox's session marker off the VM: present
// reports whether it exists, and err is set when existence or contents could
// not be established (a failed probe, or an unreadable or malformed marker),
// never folded into "absent".
func ProbeVMMarker(ctx context.Context, c *container.Client, agentbox string) (m Marker, present bool, err error) {
	out, err := c.ShellOutputQuiet(ctx, "sudo", "sh", "-c", `if [ -e "$1" ]; then echo present; cat -- "$1"; else echo absent; fi`, "sh", MarkerPath(agentbox))
	if err != nil {
		return m, false, fmt.Errorf("probe session marker of %s: %w", agentbox, err)
	}
	head, body, _ := strings.Cut(out, "\n")
	switch head {
	case "absent":
		return m, false, nil
	case "present":
	default:
		return m, false, fmt.Errorf("probe session marker of %s: unexpected answer", agentbox)
	}
	if err := yaml.Unmarshal([]byte(body), &m); err != nil || m.Identity() == "" {
		return m, true, fmt.Errorf("session marker of %s is unreadable or malformed", agentbox)
	}
	return m, true, nil
}

// ReadVMMarker reads and parses the agentbox's session marker off the VM.
// ok is false when there is none or it cannot be read or parsed.
func ReadVMMarker(ctx context.Context, c *container.Client, agentbox string) (Marker, bool) {
	var m Marker
	out, err := c.ShellOutputQuiet(ctx, "sudo", "cat", MarkerPath(agentbox))
	if err != nil {
		return m, false
	}
	if err := yaml.Unmarshal([]byte(out), &m); err != nil {
		return m, false
	}
	return m, true
}

// ReadVMTombstone reads and parses the agentbox's tombstone off the VM.
// ok is false when there is none (or it can't be read/parsed). Quiet probe:
// no tombstone is the common case and cat's complaint about it is not for
// the user.
func ReadVMTombstone(ctx context.Context, c *container.Client, agentbox string) (Tombstone, bool) {
	var ts Tombstone
	out, err := c.ShellOutputQuiet(ctx, "sudo", "cat", TombstonePath(agentbox))
	if err != nil {
		return ts, false
	}
	if err := yaml.Unmarshal([]byte(out), &ts); err != nil {
		return ts, false
	}
	return ts, true
}
