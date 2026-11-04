// Package session implements yoloctl's agent-session persistence layer.
//
// Agents follow the K8s pod model: the agent runs in its agentbox whether
// or not a client is attached; coming back means attaching, not
// restarting. The architecture mirrors Kubernetes — a per-agentbox
// **session holder** on the space VM is the containerd-shim analog. The
// holder owns the SSH connection to the agentbox's openshell-sandbox
// socket (and thereby the agent's PTY) for the session's lifetime, ring-
// buffers output for replay, and serves attach clients on a Unix socket.
// It runs as a supervised guest service (`yoloctl-session-<box>.service`,
// same lifecycle pattern as the `yoloctl-port-*` forward units), executed
// by the yoloctl binary itself via the hidden `session-hold` subcommand
// (the binary is shipped into the VM — see EnsureVMBinary).
//
// The holder lifecycle preserves three constraints:
//
//   - F1: SSH signal requests are a no-op on openshell-sandbox, so the
//     holder never tries to signal the agent; stopping the holder
//     deliberately leaves the agent running (that survival IS the
//     persistence). Agent termination goes through container stop of
//     PID 1 (`box stop` / `destroy`).
//   - F2: holder death orphans the agent — openshell-sandbox has no
//     reattach API (a new SSH session always spawns a fresh child), so an
//     orphaned agent cannot be adopted. The marker file written by the
//     holder lets the CLI detect "container up + marker present + no
//     active unit" and steer the user to recreate. The fixed service name
//     also excludes a second holder while one is active.
//   - F3: the holder sends periodic client-to-server keepalives so idle
//     sessions retain their SSH transport; see RunHolder and
//     DefaultKeepaliveInterval.
package session

import (
	"fmt"
	"time"
)

// VM-side filesystem layout. Everything here is root-owned and — unlike
// the per-agentbox dirs under /run/yoloctl/<box>/ (the sandbox SSH socket
// dir, bind-mounted into the container) and /var/lib/yoloctl/agentboxes/
// <box>/ (the VM-local workdir, bind-mounted at /workdir) — is NEVER
// bind-mounted into any container. That keeps session control state
// (markers, tombstones) and the attach protocol endpoint out of the
// agent's reach: a compromised agent must not be able to forge exit
// records, clear orphan markers, or speak the attach protocol to its own
// holder. The agentbox name "sessions" is reserved (layout.ValidateName)
// so /run/yoloctl/sessions can never collide with a box's socket dir.
const (
	// SocketDir holds one attach socket per agentbox: <box>.sock. Under
	// /run so a VM reboot (which kills every session) clears the
	// endpoints with it.
	SocketDir = "/run/yoloctl/sessions"

	// StateDir holds one subdir per agentbox with the session marker and
	// tombstone. Under /var/lib so a tombstone written moments before a
	// VM shutdown survives the reboot.
	StateDir = "/var/lib/yoloctl/sessions"

	// VMBinaryPath is where EnsureVMBinary installs the yoloctl binary
	// inside the VM (mirrors /opt/openshell/bin for the sandbox binary).
	VMBinaryPath = "/opt/yoloctl/bin/yoloctl"

	// unitPrefix namespaces the holder service inventory.
	unitPrefix = "yoloctl-session-"
)

// DefaultRingBytes is the holder's output ring-buffer size: enough to
// replay a recent screenful-plus-history through the client's emulator
// without holding the whole session in memory.
const DefaultRingBytes = 1 << 20 // 1 MiB

// DefaultKeepaliveInterval is the holder's client→server keepalive
// period. Keep it below the sandbox SSH server's inactivity timeout when
// changing the pinned OpenShell version; transport loss can orphan the child.
const DefaultKeepaliveInterval = 4 * time.Minute

// SocketPath returns the attach socket path for an agentbox.
func SocketPath(agentbox string) string {
	return SocketDir + "/" + agentbox + ".sock"
}

// StateDirFor returns the per-agentbox session-state directory.
func StateDirFor(agentbox string) string {
	return StateDir + "/" + agentbox
}

// MarkerPath returns the session marker path for an agentbox. The marker
// exists while an agent session may be running: written at holder
// startup, removed when the agent exits cleanly through the holder. A
// marker with no active holder unit and a running container means the
// holder died and the agent is an unrecoverable orphan (spike F2).
func MarkerPath(agentbox string) string {
	return StateDirFor(agentbox) + "/active"
}

// TombstonePath returns the tombstone path for an agentbox: the agent's
// last exit status, written by the holder at agent exit so a detached
// exit is reported on the next attach.
func TombstonePath(agentbox string) string {
	return StateDirFor(agentbox) + "/last-exit"
}

// UnitName returns the holder's transient systemd unit name for an
// agentbox.
func UnitName(agentbox string) string {
	return fmt.Sprintf("%s%s.service", unitPrefix, agentbox)
}
