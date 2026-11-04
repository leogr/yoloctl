// Package boxendpoint is the managed SSH process endpoint that replaces
// openshell-sandbox as PID 1 in a privileged box (the box's privileged setting).
//
// It serves the subset of SSH that yoloctl's own clients use (session holder,
// box exec, readiness, stop and host helper execs) on the box's existing
// root-owned Unix socket, with the same no-authentication rule: the socket
// directory is the endpoint's identity. Children run as the shim-mapped
// sandbox user with the same HOME/USER/SHELL/PATH/TERM and child-environment
// channel as an OpenShell launch, but without Landlock, seccomp filters,
// no_new_privs, an inner network namespace or a proxy. The container is
// privileged, so a child that uses the image's sudo is root over the space VM.
// Nothing here claims or provides confinement.
//
// The endpoint offers no TCP listener, forwarding, subsystems, environment
// requests or host authority. Every request is bounded; unknown requests are
// refused rather than left unanswered.
package boxendpoint

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

const (
	// Command is the hidden first argument that selects the endpoint before
	// any CLI, controller, operator or VM-home initialization.
	Command = "box-endpoint-internal"
	// ProtocolVersion is the launch contract this build serves. It changes
	// only when an older running endpoint can no longer satisfy current
	// clients; a different recorded version is create-only drift.
	ProtocolVersion = "1"
	// Launcher is the recorded launcher identity of this contract.
	Launcher       = launcherPrefix + ProtocolVersion
	launcherPrefix = "yoloctl-box-endpoint/"
	// BinaryDir is the VM's root-owned yoloctl directory. Privileged boxes
	// bind the directory read-only at the same path, so a later atomic
	// replacement is visible to the next start without rebinding an inode.
	BinaryDir = "/opt/yoloctl/bin"
	// Binary is the endpoint executable inside BinaryDir.
	Binary = BinaryDir + "/yoloctl"
	// SandboxUser is the only SSH user name and the child identity.
	SandboxUser = "sandbox"
	// UserEnvironmentVariable carries the account-injected child environment
	// (a JSON string map), exactly as for openshell-sandbox.
	UserEnvironmentVariable = "OPENSHELL_USER_ENVIRONMENT"
	// WorkdirRoot bounds the supervised working directory.
	WorkdirRoot = "/workdir"
)

// Limits on peer-controlled input. The SSH transport separately bounds
// packets and per-channel windows.
const (
	maxConnections        = 64
	maxSessionsPerConn    = 32
	maxCommandBytes       = 128 << 10
	maxTermBytes          = 256
	maxModeEntries        = 256
	maxUserEnvironment    = 1 << 20
	maxEnvironmentEntries = 1024
)

// LauncherFor is the launcher identity of an endpoint protocol.
func LauncherFor(protocol string) string { return launcherPrefix + protocol }

// Args returns the endpoint argv (after the entrypoint shim) for a supervised
// working directory and the in-container socket path.
func Args(workdir, socket string) []string {
	return []string{Binary, Command, "--protocol", ProtocolVersion, "--workdir", workdir, "--ssh-socket-path", socket}
}

// Options is the parsed launch argv.
type Options struct {
	Protocol   string
	Workdir    string
	SocketPath string
}

// ParseArgs strictly accepts the argv produced by Args, without Command.
// An unknown protocol is refused so a replaced binary never silently serves a
// contract that the recorded runtime did not request.
func ParseArgs(args []string) (Options, error) {
	o, err := ParseLaunch(args)
	if err != nil {
		return Options{}, err
	}
	if o.Protocol != ProtocolVersion {
		return Options{}, fmt.Errorf("endpoint protocol %q is not served by this build (serves %s); recreate the box", o.Protocol, ProtocolVersion)
	}
	return o, nil
}

// ParseLaunch validates a recorded launch of any protocol version, so runtime
// inspection can identify its workdir and version without serving it.
func ParseLaunch(args []string) (Options, error) {
	var o Options
	seen := map[string]bool{}
	for len(args) > 0 {
		if len(args) < 2 {
			return Options{}, fmt.Errorf("incomplete endpoint argument %q", args[0])
		}
		flag, value := args[0], args[1]
		if seen[flag] {
			return Options{}, fmt.Errorf("duplicate endpoint argument %s", flag)
		}
		seen[flag] = true
		switch flag {
		case "--protocol":
			o.Protocol = value
		case "--workdir":
			o.Workdir = value
		case "--ssh-socket-path":
			o.SocketPath = value
		default:
			return Options{}, fmt.Errorf("unknown endpoint argument %q", flag)
		}
		args = args[2:]
	}
	if o.Protocol == "" {
		return Options{}, errors.New("endpoint protocol is required")
	}
	if err := ValidateWorkdir(o.Workdir); err != nil {
		return Options{}, err
	}
	if !path.IsAbs(o.SocketPath) || path.Clean(o.SocketPath) != o.SocketPath || o.SocketPath == "/" {
		return Options{}, errors.New("endpoint socket path must be a clean absolute path")
	}
	return o, nil
}

// ValidateWorkdir accepts /workdir or a clean path below it, matching the
// confined launch's --workdir contract.
func ValidateWorkdir(dir string) error {
	if (dir != WorkdirRoot && !strings.HasPrefix(dir, WorkdirRoot+"/")) || path.Clean(dir) != dir {
		return fmt.Errorf("invalid endpoint workdir %q", dir)
	}
	return nil
}

// ChildEnvironment builds the sorted base environment for every child:
// HOME/USER/SHELL/PATH, then the account-injected map, which may override
// them (as OpenShell applies it after its base). OPENSHELL_* keys are never
// forwarded, nor is the raw JSON or any other supervisor variable. TERM is
// added per channel by WithTerm.
func ChildEnvironment(home, user, pathValue, userEnvironment string) ([]string, error) {
	if pathValue == "" {
		pathValue = "/usr/local/bin:/usr/bin:/bin"
	}
	env := map[string]string{"HOME": home, "USER": user, "SHELL": "/bin/bash", "PATH": pathValue}
	if userEnvironment != "" {
		if len(userEnvironment) > maxUserEnvironment {
			return nil, errors.New("child environment exceeds its size limit")
		}
		var injected map[string]string
		if err := json.Unmarshal([]byte(userEnvironment), &injected); err != nil || injected == nil {
			return nil, errors.New("child environment must be a JSON object of strings")
		}
		if len(injected) > maxEnvironmentEntries {
			return nil, errors.New("child environment has too many entries")
		}
		for key, value := range injected {
			if !validEnvKey(key) || strings.ContainsRune(value, 0) {
				return nil, errors.New("child environment has an invalid entry")
			}
			if strings.HasPrefix(key, "OPENSHELL_") {
				continue
			}
			env[key] = value
		}
	}
	for _, value := range env {
		if strings.ContainsRune(value, 0) {
			return nil, errors.New("child environment has an invalid entry")
		}
	}
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out, nil
}

// WithTerm returns env plus TERM unless the injected map already chose one.
func WithTerm(env []string, term string) []string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			return env
		}
	}
	out := append(make([]string, 0, len(env)+1), env...)
	return append(out, "TERM="+term)
}

func validEnvKey(key string) bool {
	return key != "" && !strings.ContainsAny(key, "=\x00")
}

// Shell argv mirrors the confined launch: an interactive shell for a PTY
// shell request, a login shell command for exec, and script mode for a
// shell without a terminal.
func shellArgv(command string, hasCommand, pty bool) []string {
	switch {
	case hasCommand:
		return []string{"/bin/bash", "-lc", command}
	case pty:
		return []string{"/bin/bash", "-i"}
	default:
		return []string{"/bin/bash"}
	}
}

// ptyRequest is RFC 4254 section 6.2.
type ptyRequest struct {
	Term                      string
	Cols, Rows, Width, Height uint32
	Modes                     string
}

// windowChange is RFC 4254 section 6.7.
type windowChange struct {
	Cols, Rows, Width, Height uint32
}

// terminalMode is one decoded RFC 4254 section 8 entry.
type terminalMode struct {
	Opcode byte
	Value  uint32
}

// parseModes decodes an encoded terminal-mode string. Opcodes 1-159 carry a
// uint32; TTY_OP_END or an opcode of 160 or above ends parsing, as the RFC
// specifies. Truncated input is malformed.
func parseModes(raw string) ([]terminalMode, error) {
	var out []terminalMode
	b := []byte(raw)
	for len(b) > 0 {
		op := b[0]
		if op == 0 || op >= 160 {
			return out, nil
		}
		if len(b) < 5 {
			return nil, errors.New("truncated terminal mode")
		}
		if len(out) == maxModeEntries {
			return nil, errors.New("too many terminal modes")
		}
		out = append(out, terminalMode{Opcode: op, Value: binary.BigEndian.Uint32(b[1:5])})
		b = b[5:]
	}
	return out, nil
}

func validTerm(term string) bool {
	if len(term) > maxTermBytes {
		return false
	}
	for _, r := range term {
		if r < 0x21 || r > 0x7e || r == '=' {
			return false
		}
	}
	return true
}

// clampWinsize keeps peer dimensions inside the kernel's 16-bit fields.
// Zero rows/columns become one, as for the confined endpoint; zero pixels
// remain unknown.
func clampWinsize(cols, rows, width, height uint32) (uint16, uint16, uint16, uint16) {
	clamp := func(v uint32, min uint16) uint16 {
		switch {
		case v > 0xffff:
			return 0xffff
		case uint16(v) < min:
			return min
		default:
			return uint16(v)
		}
	}
	return clamp(cols, 1), clamp(rows, 1), clamp(width, 0), clamp(height, 0)
}
