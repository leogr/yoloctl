package agentbox

import (
	"os"
	"strings"
	"testing"
)

func TestNativeGenerationTargetActualAdapterRetainsExplicitStatusWithoutInventingHistory(t *testing.T) {
	for _, state := range []string{
		nativeCleanupEmptyTaskJSON,
		`{"Status":"created","Running":false,"Pid":0,"StartedAt":"0001-01-01T00:00:00Z"}`,
		`{"Status":"exited","Running":false,"Pid":0,"StartedAt":"2026-10-30T20:00:00Z"}`,
	} {
		t.Run(state, func(t *testing.T) {
			_, log, set, read := nativeStopActualAdapterFixture(t, 0)
			set(state)
			observed, d, err := read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			target, err := nativeInactiveGenerationTarget(d, observed.ContainerID)
			if err != nil || target.Status != d.Status || target.EmptyTaskStateObserved != d.EmptyTaskStateObserved || target.ContainerID != d.ID || target.Running || target.PID != 0 {
				t.Fatal("adapter status was refused or normalized into history", target, err)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "nerdctl stop") || strings.Contains(string(raw), "nerdctl start") {
				t.Fatal("generation observation performed engine effects", string(raw))
			}
		})
	}
}
func TestNativeGenerationTargetActualAdapterRefusesAbsentUnknownLiveAndForeignFields(t *testing.T) {
	for _, state := range []string{
		`null`, `{}`, `{"Status":"","Running":false,"StartedAt":""}`,
		`{"Status":"unknown","Running":false,"Pid":0,"StartedAt":""}`,
		`{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-10-30T20:00:00Z"}`,
		`{"Status":"","Running":false,"Pid":0,"StartedAt":"","Paused":true}`,
	} {
		t.Run(state, func(t *testing.T) {
			_, _, set, read := nativeStopActualAdapterFixture(t, 0)
			set(state)
			observed, d, err := read(t.Context())
			if err != nil {
				return
			}
			if _, err := nativeInactiveGenerationTarget(d, observed.ContainerID); err == nil {
				t.Fatal("missing/unknown/live task became generation input", state)
			}
		})
	}
	_, _, set, read := nativeStopActualAdapterFixture(t, 0)
	set(nativeCleanupEmptyTaskJSON)
	observed, d, err := read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeInactiveGenerationTarget(d, strings.Repeat("b", 64)); err == nil {
		t.Fatal("foreign CID granted generation input")
	}
	d.AmbiguousTaskStateObserved = true
	if _, err := nativeInactiveGenerationTarget(d, observed.ContainerID); err == nil {
		t.Fatal("ambiguous task granted generation input")
	}
	if _, err := nativeInactiveGenerationTarget(nil, observed.ContainerID); err == nil {
		t.Fatal("missing inspection granted generation input")
	}
}
