package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// openshell-sandbox names its per-run network namespace
// sandbox-<uuid> and the host-side end of its veth pair
// veth-h-<uuid> (same uuid). Both normally die with the supervisor:
// on SIGTERM its Drop handler deletes them, and on container removal
// the kernel collects whatever lived inside the container's netns.
// When the supervisor is SIGKILLed (nerdctl rm -f, or nerdctl stop's
// grace period escalating) the Drop handler never runs and the pair
// can leak into the VM's scope — observed during the original live-stack
// validation and preserved here as a teardown invariant.
const (
	netnsPrefix    = "sandbox-"
	vethHostPrefix = "veth-h-"
)

// ReapOrphanedNetns deletes leaked sandbox-<uuid> network namespaces
// (and their veth-h-<uuid> host ends) on the space VM. A namespace is
// an orphan when no process runs inside it: every live agentbox keeps
// at least its supervised command (the agent or `sleep infinity`)
// in its inner netns, so a pid-less sandbox-* netns is teardown
// residue, not a running sandbox.
//
// Best-effort by design — callers run it after stop/remove operations
// and treat failures as warnings. Races where a netns vanishes (or
// gains a pid) between inspection and deletion are absorbed by
// skipping on any per-namespace error. The one theoretical loss is a
// sandbox mid-setup on another agentbox (netns created, command not
// yet spawned, a sub-second window): losing that race fails the other
// box's start, which its own create path surfaces; nothing is leaked
// or corrupted.
//
// One `ip netns list` round-trip when the VM is clean; three more per
// orphan otherwise. Reaped namespaces are reported on notice.
func ReapOrphanedNetns(ctx context.Context, space string, notice io.Writer) error {
	out, err := quietOutput(vmhost.ShellCommand(ctx, space, "sudo", "ip", "netns", "list"))
	if err != nil {
		return fmt.Errorf("list netns on %s: %w", space, err)
	}
	for _, ns := range parseSandboxNetns(out) {
		pids, err := quietOutput(vmhost.ShellCommand(ctx, space, "sudo", "ip", "netns", "pids", ns))
		if err != nil || strings.TrimSpace(pids) != "" {
			// Live sandbox, or the netns vanished mid-check — skip.
			continue
		}
		if err := vmhost.ShellCommand(ctx, space, "sudo", "ip", "netns", "del", ns).Run(); err != nil {
			continue
		}
		// Deleting the netns destroys the veth pair with it; the
		// explicit link del catches the dangling-host-end variant
		// spike 0.3 observed. Expected to no-op most of the time.
		_ = vmhost.ShellCommand(ctx, space, "sudo", "ip", "link", "del",
			vethHostPrefix+strings.TrimPrefix(ns, netnsPrefix)).Run()
		_, _ = fmt.Fprintf(notice, "notice: reaped leaked sandbox netns %s\n", ns)
	}
	return nil
}

// parseSandboxNetns extracts sandbox-* namespace names from
// `ip netns list` output. Lines look like "sandbox-9f3c2a1b" or
// "sandbox-9f3c2a1b (id: 0)"; the name is always the first field.
func parseSandboxNetns(out string) []string {
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], netnsPrefix) {
			names = append(names, fields[0])
		}
	}
	return names
}

// quietOutput runs cmd capturing stdout and discarding stderr — the
// reaper's probe commands fail routinely (vanished netns, already-
// collected veth) and their stderr is noise, not signal.
func quietOutput(cmd *exec.Cmd) (string, error) {
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}
