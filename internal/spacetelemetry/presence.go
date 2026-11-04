package spacetelemetry

// Availability is what a chat participant's own IRC state claims. It is
// independent of observed box activity.
type Availability int

const (
	AvailabilityUnknown   Availability = iota // not connected
	AvailabilityConnected                     // connected and not away
	AvailabilityAway                          // an explicit AWAY claim
)

// PresenceEvidence is what the channel says about one chat participant: its
// authenticated connection and its own AWAY state. Nothing else counts.
type PresenceEvidence struct {
	Connected  bool // in the channel roster
	Away       bool // explicit AWAY state; never inferred
	AwayReason string
}

// Presence is the classified state: a short label ("" for plain connected)
// and an optional detail.
type Presence struct {
	Availability Availability
	Label        string
	Detail       string
}

// ClassifyPresence is the one availability rule for every chat surface.
// Participants set it themselves with IRC AWAY and back; it is never
// derived from listeners, agent sessions, box activity, silence or the
// freshness of any telemetry.
func ClassifyPresence(e PresenceEvidence) Presence {
	switch {
	case !e.Connected:
		return Presence{AvailabilityUnknown, "not connected", ""}
	case e.Away:
		return Presence{AvailabilityAway, "away", e.AwayReason}
	}
	return Presence{Availability: AvailabilityConnected}
}
