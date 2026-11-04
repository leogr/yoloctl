package container

import "testing"

// Activation must independently match the real container PID/start instance.
// Container creation time and guessed defaults cannot provide that binding.
func TestInspectPreservesObservedProcessIdentityWithoutInventingIt(t *testing.T) {
	for _, row := range []struct {
		state   string
		pid     int
		started string
	}{
		{`"Running":true,"Pid":42,"StartedAt":"2026-10-30T20:00:00Z"`, 42, "2026-10-30T20:00:00Z"},
		{`"Running":false,"Pid":0,"StartedAt":"0001-01-01T00:00:00Z"`, 0, "0001-01-01T00:00:00Z"},
		{`"Running":true`, 0, ""},
	} {
		raw := []byte(`[{"Id":"container","Name":"/fixture","Created":"2025-01-01T00:00:00Z","State":{` + row.state + `},"Config":{}}]`)
		details, err := parseInspect(raw, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if details.PID != row.pid || details.StartedAt != row.started {
			t.Fatal("runtime process identity changed or was inferred", details.PID, details.StartedAt)
		}
	}
}
