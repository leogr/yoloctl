// Package observe is the agent observation substrate. The controller mounts
// a size- and inode-capped tmpfs per box (EnsureRoot), binds it read-write at
// ContainerDir, and creates one sandbox-owned directory per launch generation
// before each launch (Prepare). The agent's hook writer appends events there;
// Read parses them without following links and with bounded sizes.
//
// Every value is agent-reported. Any sandbox process can write into any
// generation directory it can name, so a report under gen-N means "written
// into launch N's directory", never "written by launch N's agent". Nothing
// read here is authority, and an unreadable or tampered generation never
// implies that an agent failed.
package observe

import (
	"errors"
	"fmt"
	"strconv"
)

const (
	// ContainerDir is where the box's tmpfs is bound inside the container.
	ContainerDir = "/run/yoloctl/agent"
	// EnvGeneration names the launch generation in the agent's environment.
	// It is non-secret and copyable; it only selects a directory.
	EnvGeneration = "YOLOCTL_AGENT_GEN"

	// FileBytes caps one events file; the writer starts the next file
	// instead of exceeding it, and keeps at most two.
	FileBytes = 256 << 10
	// LineBytes caps one event line, including its newline.
	LineBytes = 4 << 10
	// RootBytes holds the current and the previous generation at their
	// writer bounds (2 × 2 × FileBytes) plus lock files and directories.
	RootBytes = 2 << 20
	// RootInodes bounds files and directories a sandbox process can create.
	RootInodes = 32
	// KeepGenerations is how many generation directories Prepare retains.
	KeepGenerations = 2
)

// ErrUnavailable marks a box whose observation cannot work now: no bind, no
// verified root, a used generation directory or a full tmpfs. Callers report
// it as observation health and never fail or block a launch on it.
var ErrUnavailable = errors.New("agent observation unavailable")

func unavailable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, fmt.Sprintf(format, args...))
}

// ErrRootUnverified marks a root that is missing, foreign or not the capped
// tmpfs. It is also ErrUnavailable. A caller whose container binds the root
// fails closed on it: an optional report may be missing, but nothing may run
// over an unverified directory.
var ErrRootUnverified = errors.New("agent observation root cannot be verified")

func unverified(format string, args ...any) error {
	return fmt.Errorf("%w: %w: %s", ErrUnavailable, ErrRootUnverified, fmt.Sprintf(format, args...))
}

// genName is a generation's directory name. Generations start at 1.
func genName(gen uint64) string { return "gen-" + strconv.FormatUint(gen, 10) }
