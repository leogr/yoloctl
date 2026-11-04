package spacetelemetry

import (
	"testing"
	"time"
)

// Availability is the participant's own IRC state: connected, or an
// explicit AWAY with its reason. Nothing else is evidence.
func TestClassifyPresence(t *testing.T) {
	for _, c := range []struct {
		name  string
		e     PresenceEvidence
		want  Availability
		label string
	}{
		{"connected", PresenceEvidence{Connected: true}, AvailabilityConnected, ""},
		{"explicit away", PresenceEvidence{Connected: true, Away: true, AwayReason: "lunch"}, AvailabilityAway, "away"},
		{"not connected", PresenceEvidence{Away: true}, AvailabilityUnknown, "not connected"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if p := ClassifyPresence(c.e); p.Availability != c.want || p.Label != c.label {
				t.Fatalf("= %+v, want %v %q", p, c.want, c.label)
			}
		})
	}
	if p := ClassifyPresence(PresenceEvidence{Connected: true, Away: true, AwayReason: "lunch"}); p.Detail != "lunch" {
		t.Fatalf("away reason = %q", p.Detail)
	}
}

func TestActivityCurrentGate(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		live, paused bool
		last         time.Time
		why          string
	}{
		{true, false, now.Add(-time.Second), ""},
		{false, false, now, "telemetry not live"},
		{true, true, now, "updates paused"},
		{true, false, now.Add(-16 * time.Second), "no recent sample"},
	} {
		if ok, why := ActivityCurrent(c.live, c.paused, c.last, now, 5*time.Second); ok != (c.why == "") || why != c.why {
			t.Errorf("ActivityCurrent(%v, %v, %v) = %v %q", c.live, c.paused, now.Sub(c.last), ok, why)
		}
	}
}
