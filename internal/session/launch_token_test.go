package session

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Markers and tombstones written by older holders carry no launch token and
// still decode; the token round-trips when present.
func TestLaunchTokenIsOptionalInSessionFiles(t *testing.T) {
	var m Marker
	if err := yaml.Unmarshal([]byte("started_at: 2026-10-21T10:00:00Z\nholder_pid: 28\n"), &m); err != nil || m.LaunchToken != "" || m.Identity() == "" {
		t.Fatalf("legacy marker = %+v, %v", m, err)
	}
	var ts Tombstone
	if err := yaml.Unmarshal([]byte("exit_code: 1\nexited_at: 2026-10-21T10:00:00Z\n"), &ts); err != nil || ts.LaunchToken != "" || ts.ExitCode != 1 {
		t.Fatalf("legacy tombstone = %+v, %v", ts, err)
	}
	token := strings.Repeat("0f", 16)
	raw, err := yaml.Marshal(Marker{StartedAt: time.Unix(1, 0), HolderPID: 2, LaunchToken: token})
	if err != nil {
		t.Fatal(err)
	}
	var back Marker
	if err := yaml.Unmarshal(raw, &back); err != nil || back.LaunchToken != token {
		t.Fatalf("marker round trip = %+v, %v", back, err)
	}
	if legacy, _ := yaml.Marshal(Tombstone{ExitCode: 0, ExitedAt: time.Unix(1, 0)}); strings.Contains(string(legacy), "launch_token") {
		t.Fatal("a tokenless tombstone writes the field")
	}
}

func TestValidLaunchToken(t *testing.T) {
	for token, want := range map[string]bool{
		strings.Repeat("a", 32):       true,
		strings.Repeat("0", 32):       true,
		strings.Repeat("A", 32):       false,
		strings.Repeat("a", 31):       false,
		strings.Repeat("a", 33):       false,
		strings.Repeat("g", 32):       false,
		"":                            false,
		strings.Repeat("a", 31) + "-": false,
	} {
		if got := ValidLaunchToken(token); got != want {
			t.Errorf("ValidLaunchToken(%q) = %v, want %v", token, got, want)
		}
	}
}
