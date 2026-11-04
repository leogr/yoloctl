package chatui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// TelemetryInterval is the sampling interval the host requests for the
// participants panel's box activity: space view's default, so activity is
// held as long in both views.
const TelemetryInterval = spacetelemetry.DefaultInterval

// inventoryStaleAfter is three inventory cadences: a box mapping older
// than this no longer attributes activity to a participant.
const inventoryStaleAfter = 15 * time.Second

// TelemetryMsg is one frame of the space's telemetry stream, the same read
// space view uses. It only adds box activity to participants; the roster
// and every participant's availability still come from the channel.
type TelemetryMsg spacetelemetry.Frame

// TelemetryLostMsg reports that the telemetry stream ended; the host
// reconnects on its own. Box activity is unknown until frames resume.
type TelemetryLostMsg struct{ Err error }

// pulseTickMsg carries its tick's time, the frame time active dots are
// drawn at, as space view draws at its own tick time.
type pulseTickMsg time.Time

// boxTelemetry is the panel's view of the telemetry stream. Times are host
// receipt times, so freshness never compares host and guest clocks.
type boxTelemetry struct {
	live       bool
	inv        *spacetelemetry.Inventory
	invSeen    time.Time
	sampleSeen time.Time
	activity   *spacetelemetry.ActivityTracker
	ticking    bool
	frame      time.Time // the breath's phase time
}

func (m *Model) applyTelemetry(msg tea.Msg) tea.Cmd {
	tm := &m.tele
	if tm.activity == nil {
		tm.activity = spacetelemetry.NewActivityTracker()
	}
	now := time.Now()
	tm.frame = now
	switch msg := msg.(type) {
	case TelemetryLostMsg:
		tm.live = false
		tm.activity.Rebaseline()
		return nil
	case TelemetryMsg:
		tm.live = true
		switch msg.Kind {
		case spacetelemetry.KindInventory:
			tm.inv, tm.invSeen = msg.Inventory, now
		case spacetelemetry.KindSample:
			if now.Sub(tm.sampleSeen) > spacetelemetry.StaleAfter(TelemetryInterval) {
				tm.activity.Rebaseline() // never compare across a stall
			}
			tm.sampleSeen = now
			tm.activity.Observe(msg.Sample)
		}
	}
	return m.pulse()
}

// pulse keeps a repaint tick running while a listed participant's box is
// active, so its dot can breathe; it stops when none is. With NoMotion the
// tick still repaints, so lapsed activity is cleared from a steady dot.
func (m *Model) pulse() tea.Cmd {
	if m.tele.ticking || !m.anyActive() {
		return nil
	}
	m.tele.ticking = true
	return tea.Tick(spaceview.PulseFrame, func(t time.Time) tea.Msg { return pulseTickMsg(t) })
}

func (m *Model) pulseTick(msg pulseTickMsg) tea.Cmd {
	m.tele.ticking = false
	m.tele.frame = time.Time(msg)
	return m.pulse()
}

func (m *Model) anyActive() bool {
	for _, mem := range m.members {
		if _, active := m.memberPresence(mem); active {
			return true
		}
	}
	return false
}

// participantBox is the box the managed room maps a trusted account to,
// while the inventory is current. A room that reports an error maps none.
func (m *Model) participantBox(account string) (string, bool) {
	tm := &m.tele
	if !tm.live || tm.inv == nil || account == "" || time.Since(tm.invSeen) > inventoryStaleAfter {
		return "", false
	}
	for _, r := range tm.inv.Rooms {
		if !r.Managed || r.Error != "" {
			continue
		}
		for _, p := range r.Participants {
			if p.Account != account {
				continue
			}
			for _, b := range tm.inv.Boxes {
				if b.Name == p.Box {
					return b.Name, true
				}
			}
		}
	}
	return "", false
}

// boxActivity is a participant box's current activity, or unknown when the
// telemetry stream, its inventory or its samples are not current.
func (m *Model) boxActivity(account string) spacetelemetry.Activity {
	if m.tele.activity == nil {
		return spacetelemetry.ActivityUnknown
	}
	box, ok := m.participantBox(account)
	if !ok {
		return spacetelemetry.ActivityUnknown
	}
	if current, _ := spacetelemetry.ActivityCurrent(m.tele.live, false, m.tele.sampleSeen, time.Now(), TelemetryInterval); !current {
		return spacetelemetry.ActivityUnknown
	}
	a, _ := m.tele.activity.State(box)
	return a
}

// memberPresence classifies a roster member by its own IRC state alone and
// reports whether its box shows observed activity. Only a trusted box
// participant gets box activity, and activity never changes availability.
func (m *Model) memberPresence(mem Member) (spacetelemetry.Presence, bool) {
	p := spacetelemetry.ClassifyPresence(spacetelemetry.PresenceEvidence{Connected: true, Away: mem.Away, AwayReason: mem.AwayReason})
	w := m.identify(mem.Nick, mem.Account)
	if !w.trusted || !w.box || w.human || m.tele.activity == nil {
		return p, false
	}
	box, ok := m.participantBox(mem.Account)
	if !ok {
		return p, false
	}
	if current, _ := spacetelemetry.ActivityCurrent(m.tele.live, false, m.tele.sampleSeen, time.Now(), TelemetryInterval); !current {
		return p, false
	}
	a, _ := m.tele.activity.State(box)
	return p, a == spacetelemetry.ActivityActive
}
