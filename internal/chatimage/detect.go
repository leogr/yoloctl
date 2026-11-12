package chatimage

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// ProbeBudget is how long the host terminal has to answer the capability
// probe. There is one probe at a time and no automatic retry.
const ProbeBudget = time.Second

// Terminal is the host terminal as its XTVERSION reply names it.
type Terminal struct {
	Name                string // "kitty" or "ghostty"
	Major, Minor, Patch int
}

var versionReply = regexp.MustCompile(`^(?:(kitty)\((\d+)\.(\d+)\.(\d+)\)|(ghostty) (\d+)\.(\d+)\.(\d+))(?:[-+ ].*)?$`)

// ParseTerminalVersion recognizes Kitty ("kitty(0.35.2)") and Ghostty
// ("ghostty 1.3.1") replies. Anything else, including libghostty inside a
// TUI, is not recognized.
func ParseTerminalVersion(reply string) (Terminal, bool) {
	m := versionReply.FindStringSubmatch(reply)
	if m == nil {
		return Terminal{}, false
	}
	parts := m[1:5]
	if m[5] != "" {
		parts = m[5:9]
	}
	var n [3]int
	for i, p := range parts[1:] {
		v, err := strconv.Atoi(p)
		if err != nil || v > 1<<20 {
			return Terminal{}, false
		}
		n[i] = v
	}
	return Terminal{Name: parts[0], Major: n[0], Minor: n[1], Patch: n[2]}, true
}

// Supported reports whether the terminal is a family and version previews
// are qualified for: Kitty 0.28.0 or later, Ghostty 1.3.1 or later.
func (t Terminal) Supported() bool {
	minimum := map[string][3]int{"kitty": {0, 28, 0}, "ghostty": {1, 3, 1}}[t.Name]
	if t.Name == "" {
		return false
	}
	have := [3]int{t.Major, t.Minor, t.Patch}
	for i := range have {
		if have[i] != minimum[i] {
			return have[i] > minimum[i]
		}
	}
	return true
}

func (t Terminal) String() string {
	return fmt.Sprintf("%s %d.%d.%d", t.Name, t.Major, t.Minor, t.Patch)
}

// Probe is one capability check of the host terminal: a version reply, a
// correlated non-storing graphics query and measured cell pixels must all
// succeed within ProbeBudget. Anything else, including silence, means chips.
type Probe struct {
	Generation uint64
	queryID    int
	deadline   time.Time
	terminal   Terminal
	version    bool // a recognized, supported version reply arrived
	versionBad bool // a reply arrived but is not a supported terminal
	graphics   bool
	graphicsNo bool
}

// NewProbe starts a probe whose graphics query uses queryID (1–4294967295,
// chosen by the host and never reused by it for an image).
func NewProbe(generation uint64, queryID uint32, now time.Time) *Probe {
	return &Probe{Generation: generation, queryID: int(queryID), deadline: now.Add(ProbeBudget)}
}

// GraphicsQuery is the Kitty graphics query to write to the host terminal:
// one transparent RGB pixel transmitted directly with action query, which a
// terminal checks without storing it.
func (p *Probe) GraphicsQuery() string {
	return fmt.Sprintf("\x1b_Gi=%d,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\", p.queryID)
}

// Version records the XTVERSION reply.
func (p *Probe) Version(reply string) {
	t, ok := ParseTerminalVersion(reply)
	if ok && t.Supported() {
		p.terminal, p.version = t, true
		return
	}
	p.versionBad = true
}

// Graphics records a Kitty graphics reply. Only the reply to this probe's
// query counts; "OK" is success, anything else a refusal.
func (p *Probe) Graphics(id int, payload string) {
	if id != p.queryID {
		return
	}
	if payload == "OK" {
		p.graphics = true
	} else {
		p.graphicsNo = true
	}
}

// Result is the probe's outcome given whether cell pixels are measured now:
// done once it has failed or fully succeeded before the budget ended, or once
// the budget has passed (then unsupported, even if every signal is there).
func (p *Probe) Result(now time.Time, cellsMeasured bool) (supported, done bool) {
	if p.versionBad || p.graphicsNo || !now.Before(p.deadline) {
		return false, true // a failure, or the budget is over: nothing late counts
	}
	if p.version && p.graphics && cellsMeasured {
		return true, true
	}
	return false, false
}

// Expire ends the probe as its budget does: whatever has not succeeded by
// now fails.
func (p *Probe) Expire() { p.deadline = time.Time{} }

// Terminal is the recognized terminal, valid when Result reported support.
func (p *Probe) Terminal() Terminal { return p.terminal }
