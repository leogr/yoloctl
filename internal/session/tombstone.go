package session

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"gopkg.in/yaml.v3"
)

// Tombstone records the agent's exit when it happens with no client
// attached (and, for uniformity, on every exit): written atomically by
// the holder, reported once by the next attach, then cleared.
type Tombstone struct {
	ExitCode int       `yaml:"exit_code"`
	ExitedAt time.Time `yaml:"exited_at"`
	// LaunchToken repeats the holder's launch correlation token (see
	// Marker.LaunchToken). Empty for holders started without one.
	LaunchToken string `yaml:"launch_token,omitempty"`
}

// Marker records that an agent session was started and may still be
// running. Written by the holder at startup, removed when the agent
// exits through the holder. See MarkerPath for how its presence is
// interpreted.
type Marker struct {
	StartedAt time.Time `yaml:"started_at"`
	HolderPID int       `yaml:"holder_pid"`
	// LaunchToken is the correlation token the controller passed for this
	// launch (UnitSpec.LaunchToken). It binds the holder to one conversation
	// reservation; it is not a secret and grants nothing. Empty for holders
	// started without one, including every older build's.
	LaunchToken string `yaml:"launch_token,omitempty"`
}

// Identity names one holder incarnation: its PID and start time, which every
// build writes at holder startup. It is empty for an incomplete marker.
func (m Marker) Identity() string {
	if m.HolderPID <= 0 || m.StartedAt.IsZero() {
		return ""
	}
	return fmt.Sprintf("%d@%s", m.HolderPID, m.StartedAt.UTC().Format(time.RFC3339Nano))
}

// ValidLaunchToken reports whether t is a launch correlation token: 32
// lowercase hexadecimal characters (128 bits).
func ValidLaunchToken(t string) bool {
	if len(t) != 32 {
		return false
	}
	for _, c := range t {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// WriteTombstone writes t atomically (tmp + rename), creating the parent
// directory if needed.
func WriteTombstone(path string, t Tombstone) error {
	return writeYAML(path, t)
}

// WriteMarker writes m atomically, creating the parent directory if
// needed.
func WriteMarker(path string, m Marker) error {
	return writeYAML(path, m)
}

// RemoveMarker removes the marker; missing is fine.
func RemoveMarker(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeYAML atomically writes v as YAML at path, mode 0600, parent dirs
// 0700 — session state is root-only on the VM.
func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("session: marshal %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}
