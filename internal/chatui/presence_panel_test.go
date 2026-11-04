package chatui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func presenceModel(t *testing.T) *Model {
	t.Helper()
	m := New(Options{Space: "trial", Channel: "#space", Nick: "leo", Account: "mleo", NoMotion: true, Identities: map[string]Identity{
		"mleo": {Name: "leo", Human: true}, "mopus": {Name: "opus", Box: true, Agent: "claude"}, "msol": {Name: "sol", Box: true},
		"mfable": {Name: "fable", Box: true, Agent: "claude"}, "mqa": {Name: "qa", Box: true},
	}})
	m.Update(ConnectionMsg{Connected: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(MembersMsg{Channel: "#space", Members: []Member{
		{Nick: "leo", Account: "mleo"}, {Nick: "opus", Account: "mopus"}, {Nick: "sol", Account: "msol"},
		{Nick: "fable", Account: "mfable", Away: true, AwayReason: "paused"}, {Nick: "qa", Account: "mqa"},
	}})
	return m
}

// presenceInventory maps every box participant, including boxes whose
// agent session is absent or lost: evidence that once narrowed availability.
func presenceInventory() *spacetelemetry.Inventory {
	return &spacetelemetry.Inventory{
		Boxes: []spacetelemetry.Box{
			{Name: "opus", Session: "running"}, {Name: "sol", Session: "lost"}, {Name: "fable", Session: "running"}, {Name: "qa", Session: "none"},
		},
		Rooms: []spacetelemetry.Room{{Server: "chat", Managed: true, Participants: []spacetelemetry.Participant{
			{Box: "opus", Account: "mopus", Connections: 1},
			{Box: "sol", Account: "msol", Connections: 1},
			{Box: "fable", Account: "mfable", Connections: 1},
			{Box: "qa", Account: "mqa", Connections: 1},
		}}},
	}
}

func presenceOf(m *Model, account string) (spacetelemetry.Presence, bool) {
	for _, mem := range m.members {
		if mem.Account == account {
			return m.memberPresence(mem)
		}
	}
	panic("no member " + account)
}

func inventoryFrame(inv *spacetelemetry.Inventory) TelemetryMsg {
	return TelemetryMsg(spacetelemetry.Frame{Kind: spacetelemetry.KindInventory, Inventory: inv})
}

func sampleFrame(boxes map[string]spacetelemetry.BoxSample) TelemetryMsg {
	return TelemetryMsg(spacetelemetry.Frame{Kind: spacetelemetry.KindSample, Sample: &spacetelemetry.Sample{Boxes: boxes}})
}

// Availability is each participant's own IRC state and nothing else: agent
// sessions, room errors and stale or lost telemetry never change a dot or
// add a label, and the human and boxes follow the same rule.
func TestAvailabilityIsExplicitIRCStateOnly(t *testing.T) {
	m := presenceModel(t)
	check := func(stage string) {
		t.Helper()
		for _, c := range []struct {
			account string
			want    spacetelemetry.Availability
			label   string
			glyph   string
			color   spaceview.Color
		}{
			{"mleo", spacetelemetry.AvailabilityConnected, "", "●", m.theme.OK},
			{"mopus", spacetelemetry.AvailabilityConnected, "", "●", m.theme.OK},
			{"msol", spacetelemetry.AvailabilityConnected, "", "●", m.theme.OK},
			{"mfable", spacetelemetry.AvailabilityAway, "paused", "●", m.theme.Warn},
			{"mqa", spacetelemetry.AvailabilityConnected, "", "●", m.theme.OK},
		} {
			p, active := presenceOf(m, c.account)
			if active {
				t.Errorf("%s: %s active without samples", stage, c.account)
			}
			if p.Availability != c.want || presenceLabel(p) != c.label {
				t.Errorf("%s: %s = %+v %q, want %v %q", stage, c.account, p, presenceLabel(p), c.want, c.label)
			}
			if d := m.presenceDot(p, active, 0); d.text != c.glyph || d.style.FG != c.color {
				t.Errorf("%s: %s dot = %q %v, want %q %v", stage, c.account, d.text, d.style.FG, c.glyph, c.color)
			}
		}
		panel := view(m)
		if !strings.Contains(panel, "       paused") || strings.Contains(panel, "claude") || strings.Contains(panel, "away:") || strings.Contains(panel, "unknown") {
			t.Errorf("%s: panel must show the bare reason without the redundant away label:\n%s", stage, panel)
		}
		for _, inferred := range []string{"mentions only", "no agent session", "status error", "listener"} {
			if strings.Contains(panel, inferred) {
				t.Errorf("%s: panel infers %q:\n%s", stage, inferred, panel)
			}
		}
	}
	check("without telemetry")
	m.Update(inventoryFrame(presenceInventory()))
	check("absent and lost agent sessions")
	failed := presenceInventory()
	failed.Rooms[0] = spacetelemetry.Room{Server: "chat", Managed: true, Error: "managed server status unavailable: peer mismatch"}
	m.Update(inventoryFrame(failed))
	check("room evidence error")
	m.Update(inventoryFrame(presenceInventory()))
	m.tele.invSeen = time.Now().Add(-time.Minute)
	check("stale inventory")
	m.Update(TelemetryLostMsg{})
	check("lost stream")
}

// AWAY and back, as the channel reports them, are the only availability
// changes; the reason travels with the claim.
func TestExplicitAwayAndBack(t *testing.T) {
	m := presenceModel(t)
	m.Update(inventoryFrame(presenceInventory()))
	set := func(away bool, reason string) {
		m.Update(MembersMsg{Channel: "#space", Members: []Member{
			{Nick: "leo", Account: "mleo"}, {Nick: "opus", Account: "mopus", Away: away, AwayReason: reason},
		}})
	}
	set(true, "standby; awaiting assignment")
	p, _ := presenceOf(m, "mopus")
	if p.Availability != spacetelemetry.AvailabilityAway || presenceLabel(p) != "standby; awaiting assignment" {
		t.Fatalf("explicit away = %+v %q", p, presenceLabel(p))
	}
	if d := m.presenceDot(p, false, 0); d.text != "●" || d.style.FG != m.theme.Warn {
		t.Fatalf("away dot = %q %v", d.text, d.style.FG)
	}
	if out := view(m); !strings.Contains(out, "       standby") || strings.Contains(out, "claude") {
		t.Fatal("panel lacks the away reason alone in place of an unknown model")
	}
	set(true, "")
	p, _ = presenceOf(m, "mopus")
	if p.Availability != spacetelemetry.AvailabilityAway || presenceLabel(p) != "" {
		t.Fatalf("away without a reason = %+v %q", p, presenceLabel(p))
	}
	if d := m.presenceDot(p, false, 0); d.style.FG != m.theme.Warn {
		t.Fatal("away without a reason lost the yellow dot")
	}
	set(false, "")
	p, _ = presenceOf(m, "mopus")
	if p.Availability != spacetelemetry.AvailabilityConnected || presenceLabel(p) != "" {
		t.Fatalf("back = %+v %q", p, presenceLabel(p))
	}
	if d := m.presenceDot(p, false, 0); d.text != "●" || d.style.FG != m.theme.OK {
		t.Fatalf("back dot = %q %v", d.text, d.style.FG)
	}
}

// A long away reason must not hide the model, even in the smaller panel;
// without a known model a box's reason stands alone; a human's row reads
// "human" first.
// Compact rows add no kind or trust tag: only " you" and the reason.
func TestParticipantAwayDetails(t *testing.T) {
	for _, width := range []int{80, 100} {
		m := presenceModel(t)
		m.opts.Identities["mlee"] = Identity{Name: "lee", Human: true}
		m.Update(AgentsMsg{At: time.Now(), Agents: map[string]string{"mopus": "claude"}, Runtime: map[string]AgentRuntime{
			"mopus": {Model: RuntimeValue{"claude-opus-5-5", SourceStatusLine}},
		}})
		mem := Member{Nick: "opus", Account: "mopus", Away: true, AwayReason: "standby; awaiting assignment"}
		lee := Member{Nick: "lee", Account: "mlee", Away: true, AwayReason: "afk"}
		m.Update(MembersMsg{Channel: "#space", Members: []Member{mem, lee}})
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		out := view(m)
		if !strings.Contains(out, "Opus 5.5 · st") || !strings.Contains(out, "…") || strings.Contains(out, "away:") || strings.Contains(out, "claude") || strings.Contains(out, "unknown") {
			t.Fatalf("width %d: the model must precede a truncated away reason:\n%s", width, out)
		}
		if got := cardRole(m, "mlee"); got != "human · afk" {
			t.Fatalf("width %d: a human's row keeps its label before the reason: %q", width, got)
		}
		mem.AwayReason = "paused"
		m.Update(MembersMsg{Channel: "#space", Members: []Member{
			{Nick: "leo", Account: "mleo"}, mem, lee, {Nick: "ghost", Account: "mghost", Away: true, AwayReason: "brb"},
		}})
		m.Update(tea.WindowSizeMsg{Width: width, Height: 14})
		out = view(m)
		if m.layout.cardWidth != 0 {
			t.Fatalf("width %d: the test needs one-row participants", width)
		}
		for _, want := range []string{"opus paused", "leo you", "lee afk", "ghost brb"} {
			if !strings.Contains(out, want) {
				t.Fatalf("width %d: compact rows lack %q:\n%s", width, want, out)
			}
		}
		for _, banned := range []string{"away:", "claude", "Opus 5.5", " human", "ghost ?", "unverified"} {
			if strings.Contains(out, banned) {
				t.Fatalf("width %d: compact rows show %q:\n%s", width, banned, out)
			}
		}
	}
}

// Box activity comes from the shared tracker and gate, only for box
// participants. It animates the dot without touching availability, so an
// explicitly away participant stays away while active. Idle, stale or lost
// evidence draws a steady dot and stops the tick.
func TestParticipantActivityUsesSharedTracker(t *testing.T) {
	m := presenceModel(t)
	m.opts.NoMotion = false
	m.Update(inventoryFrame(presenceInventory()))
	output := func(n uint64) *spacetelemetry.PTYCounters {
		return &spacetelemetry.PTYCounters{Session: "s", OutputBytes: n}
	}
	sample := func(n uint64) tea.Cmd {
		_, cmd := m.Update(sampleFrame(map[string]spacetelemetry.BoxSample{
			"opus": {PTY: output(n)}, "fable": {PTY: output(n)}, "sol": {NetRx: n, IOWrite: n, PTY: &spacetelemetry.PTYCounters{Session: "s", InputPackets: n}}, "leo": {PTY: output(n)},
		}))
		return cmd
	}
	if cmd := sample(0); cmd != nil {
		t.Fatal("a baseline sample started the pulse")
	}
	if _, active := presenceOf(m, "mopus"); active {
		t.Fatal("one sample is only a baseline")
	}
	if cmd := sample(5); cmd == nil {
		t.Fatal("an active participant does not start the pulse")
	}
	for _, c := range []struct {
		account string
		want    spacetelemetry.Availability
		active  bool
	}{
		{"mopus", spacetelemetry.AvailabilityConnected, true},
		{"mfable", spacetelemetry.AvailabilityAway, true},
		{"msol", spacetelemetry.AvailabilityConnected, false},
		{"mleo", spacetelemetry.AvailabilityConnected, false},
	} {
		if p, active := presenceOf(m, c.account); p.Availability != c.want || active != c.active {
			t.Errorf("%s = %+v active=%v, want %v active=%v", c.account, p, active, c.want, c.active)
		}
	}
	p, _ := presenceOf(m, "msol")
	for step := range 4 {
		m.tele.frame = time.Unix(0, 0).Add(time.Duration(step) * 500 * time.Millisecond)
		if d := m.presenceDot(p, false, 0); d.style.FG != m.theme.OK {
			t.Fatalf("an idle dot animates: %v", d.style.FG)
		}
	}
	if cmd := m.pulse(); cmd != nil {
		t.Fatal("a second pulse tick started while one runs")
	}
	seen := m.tele.sampleSeen
	m.tele.sampleSeen = time.Now().Add(-time.Minute)
	if _, active := presenceOf(m, "mopus"); active {
		t.Fatal("activity outlived a stalled stream")
	}
	if cmd := m.pulseTick(pulseTickMsg(time.Now())); cmd != nil {
		t.Fatal("the pulse kept ticking without current activity")
	}
	m.tele.sampleSeen = seen
	m.tele.invSeen = time.Now().Add(-time.Minute)
	if _, active := presenceOf(m, "mopus"); active {
		t.Fatal("activity was attributed through a stale box mapping")
	}
	m.Update(inventoryFrame(presenceInventory()))
	m.Update(TelemetryLostMsg{})
	if _, active := presenceOf(m, "mopus"); active {
		t.Fatal("activity outlived the stream")
	}
}

// An active dot is drawn with space view's shared ActiveTone at the pulse
// tick's time: the same gentle breath for the green connected dot and the
// yellow away dot, with no blink to another color, and a steady tone
// without motion.
func TestActiveDotsShareSpaceViewWaveform(t *testing.T) {
	m := presenceModel(t)
	m.opts.NoMotion = false
	start := time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC)
	m.Update(pulseTickMsg(start))
	if !m.tele.frame.Equal(start) {
		t.Fatalf("pulse tick did not set the frame time: %v", m.tele.frame)
	}
	for _, c := range []struct {
		p    spacetelemetry.Presence
		base spaceview.Color
	}{
		{spacetelemetry.Presence{Availability: spacetelemetry.AvailabilityConnected}, m.theme.OK},
		{spacetelemetry.Presence{Availability: spacetelemetry.AvailabilityAway, Label: "away"}, m.theme.Warn},
	} {
		seen := map[spaceview.Color]bool{}
		for step := range int(spaceview.PulseCycle / spaceview.PulseFrame) {
			m.tele.frame = start.Add(time.Duration(step) * spaceview.PulseFrame)
			got := m.presenceDot(c.p, true, 0).style.FG
			if want := m.theme.ActiveTone(c.base, true, m.tele.frame); got != want {
				t.Fatalf("step %d: dot %v, want shared tone %v", step, got, want)
			}
			if got == m.theme.Dim {
				t.Fatalf("step %d: the dot blinked to dim", step)
			}
			seen[got] = true
		}
		if len(seen) < 4 {
			t.Fatalf("active dot over %v does not breathe: %v", c.base, seen)
		}
		m.opts.NoMotion = true
		steady := m.theme.ActiveTone(c.base, false, start)
		for _, at := range []time.Time{start, start.Add(time.Second / 3), start.Add(time.Second)} {
			m.tele.frame = at
			if got := m.presenceDot(c.p, true, 0).style.FG; got != steady {
				t.Fatalf("motion off: dot %v, want steady %v", got, steady)
			}
		}
		m.opts.NoMotion = false
	}
}

// The dot is always a solid ●, and its color is the availability alone:
// green, yellow or gray. Activity breathes whichever color is shown with
// the same waveform, never changing glyph, hue or state, and the away
// reason only adds words.
func TestDotIsSolidAndActivityIsIndependentOfState(t *testing.T) {
	m := presenceModel(t)
	m.opts.NoMotion = false
	at := time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC).Add(spaceview.PulseCycle / 2)
	m.tele.frame = at
	for _, c := range []struct {
		p    spacetelemetry.Presence
		base spaceview.Color
	}{
		{spacetelemetry.ClassifyPresence(spacetelemetry.PresenceEvidence{Connected: true}), m.theme.OK},
		{spacetelemetry.ClassifyPresence(spacetelemetry.PresenceEvidence{Connected: true, Away: true}), m.theme.Warn},
		{spacetelemetry.ClassifyPresence(spacetelemetry.PresenceEvidence{Connected: true, Away: true, AwayReason: "lunch"}), m.theme.Warn},
		{spacetelemetry.ClassifyPresence(spacetelemetry.PresenceEvidence{}), m.theme.Dim},
	} {
		idle, busy := m.presenceDot(c.p, false, 0), m.presenceDot(c.p, true, 0)
		if idle.text != "●" || busy.text != "●" {
			t.Fatalf("%+v: glyphs %q %q, want a solid dot", c.p, idle.text, busy.text)
		}
		if idle.style.FG != c.base {
			t.Fatalf("%+v: idle color %v, want %v", c.p, idle.style.FG, c.base)
		}
		if want := m.theme.ActiveTone(c.base, true, at); busy.style.FG != want || busy.style.FG == c.base {
			t.Fatalf("%+v: active color %v, want the shared breath %v over %v", c.p, busy.style.FG, want, c.base)
		}
	}
	if m.theme.OK == m.theme.Warn || m.theme.Warn == m.theme.Dim || m.theme.OK == m.theme.Dim {
		t.Fatal("availability colors are not distinct")
	}
}
