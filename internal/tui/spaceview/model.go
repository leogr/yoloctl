// Package spaceview renders `yoloctl space view`: a live, clickable
// infographic of one space. It shows the host workdirs and ports that cross
// into the VM, the shared CPU and memory budget, every box with its identity,
// grants and live usage, chat rooms observed between boxes, and the accounts
// on the native data disk. Data arrives on one long-lived controller stream;
// clicking a box attaches to it and clicking anything else shows the same
// information the matching CLI command prints.
package spaceview

import (
	"context"
	"image/color"
	"log/slog"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// VMInfo is host-side knowledge about the VM; the guest reports CPUs and
// memory itself.
type VMInfo struct {
	Backend string
}

// Options wires the view to the CLI. Every external effect goes through a
// function here so the model can be tested without a VM or terminal.
type Options struct {
	Space    string
	Interval time.Duration
	VM       VMInfo
	Home     string
	Icons    map[string]config.AgentIcon
	Motion   bool
	// Connect runs one telemetry stream until it fails or ctx ends, calling
	// send for every frame. It must never start a VM.
	Connect func(ctx context.Context, send func(spacetelemetry.Frame)) error
	// Run executes this yoloctl binary and returns stdout (popup details).
	Run func(ctx context.Context, args ...string) ([]byte, error)
	// AttachCmd builds the command that takes over the terminal to attach.
	AttachCmd func(box string) *exec.Cmd
	// OpenTab asks the hosting TUI to open an attach tab instead; nil when
	// the view runs in a plain terminal.
	OpenTab func(box string) error
	// OpenURL opens a published port on the host; nil disables it.
	OpenURL func(ctx context.Context, url string) error
}

type connPhase int

const (
	connConnecting connPhase = iota
	connLive
	connLost
)

type connState struct {
	state   connPhase
	err     string
	retryAt time.Time
}

type toast struct {
	text  string
	kind  prettylog.Kind
	bad   bool
	until time.Time
}

// Messages from the stream goroutine and timers.
type (
	frameMsg spacetelemetry.Frame
	lostMsg  struct {
		err     error
		retryAt time.Time
	}
	tickMsg     time.Time
	attachedMsg struct {
		box string
		err error
	}
)

const (
	tweenFor   = 450 * time.Millisecond
	fastTick   = 40 * time.Millisecond
	slowTick   = PulseFrame
	toastFor   = 4 * time.Second
	sortName   = 0
	sortCPU    = 1
	sortMemory = 2
)

// Model is the Bubble Tea model behind the view.
type Model struct {
	opts  Options
	d     *data
	theme Theme
	dark  bool
	bg    color.Color

	w, h int
	now  time.Time

	focus       string
	hover       string // target key under the pointer
	lastTargets []target
	scroll      int
	sortMode    int
	filter      string
	filtering   bool
	paused      bool
	sampleSeen  time.Time // host time the last sample arrived
	motion      bool
	pulse       int
	tweenFrom   map[string]float64
	tweenStart  time.Time
	shown       map[string]float64 // tweened CPU cores by box; "" is VM busy

	popup *popup
	toast *toast
	conn  connState
	last  *frame
	ctx   context.Context
	retry func() // skips the reconnect backoff; set by Run
}

// New builds a model; the caller runs it in a tea.Program.
func New(ctx context.Context, opts Options) *Model {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	m := &Model{
		opts:      opts,
		d:         newData(),
		dark:      true,
		motion:    opts.Motion,
		shown:     map[string]float64{},
		tweenFrom: map[string]float64{},
		now:       time.Now(),
		ctx:       ctx,
	}
	m.theme = newTheme(true, nil, opts.Icons)
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.tick())
}

func (m *Model) tick() tea.Cmd {
	d := slowTick
	if m.motion && m.now.Sub(m.tweenStart) < tweenFor {
		d = fastTick
	}
	if !m.motion {
		d = 500 * time.Millisecond
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// ---- model queries used by layout ----------------------------------------------------

func (m *Model) box(name string) (spacetelemetry.Box, bool) {
	if m.d.inv == nil {
		return spacetelemetry.Box{}, false
	}
	for _, b := range m.d.inv.Boxes {
		if b.Name == name {
			return b, true
		}
	}
	return spacetelemetry.Box{}, false
}

func (m *Model) sortedBoxes() []spacetelemetry.Box {
	if m.d.inv == nil {
		return nil
	}
	boxes := slices.Clone(m.d.inv.Boxes)
	sort.SliceStable(boxes, func(i, j int) bool { return boxes[i].Name < boxes[j].Name })
	return boxes
}

// visibleBoxes applies the filter and the chosen sort.
func (m *Model) visibleBoxes() []spacetelemetry.Box {
	boxes := m.sortedBoxes()
	if m.filter != "" {
		q := strings.ToLower(m.filter)
		boxes = slices.DeleteFunc(boxes, func(b spacetelemetry.Box) bool {
			hay := strings.ToLower(strings.Join([]string{b.Name, b.Agent, b.Account, b.Workdir, b.State, strings.Join(b.Features, " ")}, " "))
			return !strings.Contains(hay, q)
		})
	}
	switch m.sortMode {
	case sortCPU:
		sort.SliceStable(boxes, func(i, j int) bool { return m.shownCPU(boxes[i].Name) > m.shownCPU(boxes[j].Name) })
	case sortMemory:
		// By current total charge; boxes without a current reading follow.
		mem := func(b spacetelemetry.Box) (uint64, bool) {
			bm, why := m.memory(b)
			return bm.Current, why == ""
		}
		sort.SliceStable(boxes, func(i, j int) bool {
			a, aok := mem(boxes[i])
			b, bok := mem(boxes[j])
			if aok != bok {
				return aok
			}
			return a > b
		})
	}
	return boxes
}

func (m *Model) accent(name string) Color {
	i, ok := m.d.accents[name]
	if !ok || len(m.theme.Accents) == 0 {
		return m.theme.Label
	}
	return m.theme.Accents[i%len(m.theme.Accents)]
}

// agentIcon returns the agent's icon in a slot at least two columns wide.
// Pictographs such as ✳ measure one column, but many fonts draw them wider
// and would cover a single following space; the tab strip pads the same way.
func (m *Model) agentIcon(agent string) string {
	icon := "$"
	switch configured, ok := m.opts.Icons[agent]; {
	case ok && configured.Icon != "":
		icon = configured.Icon
	case agent != "" && agent != "none":
		icon = string([]rune(agent)[0])
	}
	return icon + strings.Repeat(" ", max(2-textWidth(icon), 0))
}

func (m *Model) icon(b spacetelemetry.Box) string { return m.agentIcon(b.Agent) }

func (m *Model) roomOf(name string) (int, bool) {
	if m.d.inv == nil {
		return 0, false
	}
	for i, r := range m.d.inv.Rooms {
		if r.Server == name || slices.Contains(r.Members, name) {
			return i, true
		}
	}
	return 0, false
}

func (m *Model) roomColor(i int) (Color, Color) {
	if i == 0 {
		return m.theme.Room, m.theme.RoomHi
	}
	c := m.theme.Accents[(i+5)%len(m.theme.Accents)]
	return c, c
}

// spotlight is the chat room highlighted by the pointer: hovering a room
// chip, or a box that serves a room.
func (m *Model) spotlight() string {
	if m.d.inv == nil || m.hover == "" {
		return ""
	}
	for _, r := range m.d.inv.Rooms {
		if m.hover == (target{tRoom, r.Server}).key() || m.hover == (target{tBox, r.Server}).key() {
			return r.Server
		}
	}
	return ""
}

// staleAfter is how long the last sample remains current evidence.
func (m *Model) staleAfter() time.Duration { return spacetelemetry.StaleAfter(m.opts.Interval) }

// sampleCurrent is the shared freshness gate for the last sample's readings:
// only a live, unpaused stream with a recent sample is current.
func (m *Model) sampleCurrent() (bool, string) {
	return spacetelemetry.ActivityCurrent(m.conn.state == connLive, m.paused, m.sampleSeen, m.now, m.opts.Interval)
}

// activity is a box's observed activity and, when unknown, why. Evidence is
// current only while samples keep arriving: a lost, paused or stalled stream
// never keeps a box looking active or idle.
func (m *Model) activity(name string) (activity, string) {
	if ok, why := m.sampleCurrent(); !ok {
		return activityUnknown, why
	}
	return m.d.act.State(name)
}

func (m *Model) statusText() string {
	switch {
	case m.d.inv != nil && m.d.inv.BoxesError != "":
		return "box inventory unavailable"
	case m.d.last != nil && m.d.last.Error != "":
		return "sample incomplete: " + m.d.last.Error
	default:
		return "click a box to attach · click anything else to inspect it"
	}
}

// shownCPU is the tweened value so bars glide between samples.
func (m *Model) shownCPU(name string) float64 {
	live := m.d.boxes[name]
	if live == nil {
		return 0
	}
	return m.tweened(name, live.CPU)
}

func (m *Model) shownVM() float64 { return m.tweened("", m.d.vmBusy) }

func (m *Model) tweened(key string, target float64) float64 {
	if !m.motion {
		return target
	}
	from, ok := m.tweenFrom[key]
	if !ok {
		return target
	}
	p := m.now.Sub(m.tweenStart).Seconds() / tweenFor.Seconds()
	if p >= 1 {
		return target
	}
	p = 1 - (1-p)*(1-p)*(1-p) // ease out
	return from + (target-from)*p
}

// startTween remembers the currently displayed values before new data lands.
func (m *Model) startTween() {
	from := map[string]float64{"": m.shownVM()}
	for name := range m.d.boxes {
		from[name] = m.shownCPU(name)
	}
	m.tweenFrom, m.tweenStart = from, m.now
}

func (m *Model) say(text string, bad bool) {
	level := prettylog.LevelNotice
	if bad {
		level = prettylog.LevelError
	}
	kind := prettylog.KindOf(slog.NewRecord(time.Time{}, level, text, 0))
	m.toast = &toast{text: text, kind: kind, bad: bad, until: m.now.Add(toastFor)}
}

// ---- update ---------------------------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.dark, m.bg = msg.IsDark(), msg.Color
		m.theme = newTheme(m.dark, m.bg, m.opts.Icons)
	case tickMsg:
		m.now = time.Time(msg)
		m.pulse = int(m.now.UnixMilli() / PulseFrame.Milliseconds())
		if m.toast != nil && !m.now.Before(m.toast.until) {
			m.toast = nil
		}
		return m, m.tick()
	case connectingMsg:
		m.conn = connState{state: connConnecting}
	case lostMsg:
		m.conn = connState{state: connLost, err: msg.err.Error(), retryAt: msg.retryAt}
		m.d.rebaseline()
	case frameMsg:
		m.conn.state = connLive
		if m.paused {
			return m, nil
		}
		m.now = time.Now()
		f := spacetelemetry.Frame(msg)
		var events []event
		switch f.Kind {
		case spacetelemetry.KindInventory:
			events = m.d.applyInventory(f.At, f.Inventory)
			m.keepFocus()
		case spacetelemetry.KindSample:
			m.startTween()
			if m.now.Sub(m.sampleSeen) > m.staleAfter() {
				m.d.rebaseline() // a stalled stream resumed: no rates across the gap
			}
			m.sampleSeen = m.now
			events = m.d.applySample(f.At, f.Sample)
		}
		for _, e := range events {
			text := e.text
			if e.box != "" {
				text = e.box + " " + e.text
			}
			m.say(text, e.bad)
			if e.warn {
				m.toast.kind = prettylog.KindWarn
			}
		}
	case popupLoadedMsg:
		if m.popup != nil && m.popup.id == msg.id {
			m.popup.load(m, msg)
		}
	case urlOpenedMsg:
		if msg.err != nil {
			m.say("could not open "+msg.url+": "+msg.err.Error(), true)
		} else {
			m.say("opened "+msg.url+" on the host", false)
		}
	case attachedMsg:
		if msg.err != nil {
			m.say("attach to "+msg.box+" ended: "+msg.err.Error(), true)
		} else {
			m.say("detached from "+msg.box+"; its session keeps running", false)
		}
		return m, tea.RequestWindowSize
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseMsg:
		return m.mouse(msg)
	}
	return m, nil
}

// keepFocus moves focus to a visible box when the focused one disappears.
func (m *Model) keepFocus() {
	boxes := m.visibleBoxes()
	if len(boxes) == 0 {
		m.focus = ""
		return
	}
	if !slices.ContainsFunc(boxes, func(b spacetelemetry.Box) bool { return b.Name == m.focus }) {
		m.focus = boxes[0].Name
	}
}

func (m *Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.popup != nil {
		return m.popupKey(k)
	}
	if m.filtering {
		switch k.String() {
		case "esc":
			m.filtering, m.filter = false, ""
		case "enter":
			m.filtering = false
		case "backspace":
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		default:
			if k.Text != "" {
				m.filter += k.Text
			}
		}
		m.keepFocus()
		return m, nil
	}
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.keepFocus()
		}
	case "?":
		m.openHelp()
	case "/":
		m.filtering = true
	case "s":
		m.sortMode = (m.sortMode + 1) % 3
		m.say("sorted by "+[]string{"name", "CPU", "memory"}[m.sortMode], false)
	case "p":
		m.paused = !m.paused
		if !m.paused {
			m.d.rebaseline() // samples were dropped while paused
		}
	case "m":
		m.motion = !m.motion
		m.say(map[bool]string{true: "motion on", false: "motion off"}[m.motion], false)
	case "r":
		if m.conn.state != connLive && m.retry != nil {
			m.retry()
			m.say("reconnecting…", false)
		}
	case "enter", "space":
		if m.focus != "" {
			return m, m.activateBox(m.focus)
		}
	case "i":
		if m.focus != "" {
			m.openInspect(m.focus)
			return m, m.popup.cmd
		}
	case "up", "k":
		m.moveFocus(0, -1)
	case "down", "j":
		m.moveFocus(0, 1)
	case "left", "h":
		m.moveFocus(-1, 0)
	case "right", "l":
		m.moveFocus(1, 0)
	case "tab":
		m.moveFocus(1, 0)
	case "shift+tab":
		m.moveFocus(-1, 0)
	case "pgdown", "ctrl+d":
		m.scroll += max(m.h-4, 1)
	case "pgup", "ctrl+u":
		m.scroll -= max(m.h-4, 1)
	case "home", "g":
		m.scroll = 0
	case "end", "G":
		m.scroll = 1 << 20
	}
	if m.popup != nil {
		return m, m.popup.cmd
	}
	return m, nil
}

// moveFocus walks the card grid; dy moves a whole row.
func (m *Model) moveFocus(dx, dy int) {
	f := m.last
	if f == nil || len(f.order) == 0 {
		return
	}
	i := slices.Index(f.order, m.focus)
	if i < 0 {
		m.focus = f.order[0]
		return
	}
	i += dx + dy*max(f.cols, 1)
	i = min(max(i, 0), len(f.order)-1)
	m.focus = f.order[i]
	if rows, ok := f.boxRows[m.focus]; ok {
		if rows[0] < f.scroll {
			m.scroll = rows[0]
		} else if rows[1] > f.scroll+f.bodyH {
			m.scroll = rows[1] - f.bodyH
		}
	}
}

func (m *Model) mouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	mo := msg.Mouse()
	tg, ok := m.last.targetAt(mo.X, mo.Y)
	switch msg.(type) {
	case tea.MouseMotionMsg:
		// Only the popup's own controls react while it is open.
		if ok && (m.popup == nil || tg.kind == tPopupClose || tg.kind == tAction) {
			m.hover = tg.key()
		} else {
			m.hover = ""
		}
		return m, nil
	case tea.MouseWheelMsg:
		if m.popup != nil {
			m.popup.scrollBy(map[bool]int{true: -3, false: 3}[mo.Button == tea.MouseWheelUp])
			return m, nil
		}
		switch mo.Button {
		case tea.MouseWheelUp:
			m.scroll -= 3
		case tea.MouseWheelDown:
			m.scroll += 3
		}
		if m.last != nil {
			m.scroll = clampScroll(m.scroll, m.last.contentH, m.last.bodyH)
		}
		return m, nil
	case tea.MouseClickMsg:
	default:
		return m, nil
	}
	if m.popup != nil {
		if !ok || tg.kind == tPopupClose {
			m.popup = nil
			return m, nil
		}
		if tg.kind == tAction {
			return m, m.popup.run(m, tg.id)
		}
		if tg.kind == tPopup {
			return m, nil
		}
		// A click on the dashboard behind the popup only closes it.
		m.popup = nil
		return m, nil
	}
	if !ok {
		return m, nil
	}
	if mo.Button == tea.MouseRight {
		if tg.kind == tBox || tg.kind == tBoxMem {
			m.focus = tg.id
			m.openInspect(tg.id)
			return m, m.popup.cmd
		}
		return m, nil
	}
	if mo.Button != tea.MouseLeft {
		return m, nil
	}
	switch tg.kind {
	case tBox:
		m.focus = tg.id
		return m, m.activateBox(tg.id)
	case tHelp:
		m.openHelp()
		return m, nil
	case tQuit:
		return m, tea.Quit
	default:
		m.openTarget(tg)
		if m.popup != nil {
			return m, m.popup.cmd
		}
	}
	return m, nil
}

// activateBox attaches, or asks first when attaching would start something.
func (m *Model) activateBox(name string) tea.Cmd {
	box, ok := m.box(name)
	if !ok {
		return nil
	}
	switch attachVerb(box) {
	case "attach":
		return m.attach(name)
	case "":
		m.openInspect(name)
		return m.popup.cmd
	default:
		m.openConfirm(box)
		return nil
	}
}

func (m *Model) attach(name string) tea.Cmd {
	if m.opts.OpenTab != nil {
		if err := m.opts.OpenTab(name); err != nil {
			m.say("could not open a tab for "+name+": "+err.Error(), true)
			return nil
		}
		m.say("opened "+name+" in a new tab", false)
		return nil
	}
	if m.opts.AttachCmd == nil {
		return nil
	}
	cmd := m.opts.AttachCmd(name)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return attachedMsg{box: name, err: err} })
}

func (m *Model) View() tea.View {
	if m.w == 0 || m.h == 0 {
		return tea.NewView("")
	}
	f := m.render()
	m.last = f
	m.lastTargets = f.targets
	m.scroll = f.scroll
	v := tea.NewView(f.c.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = "yoloctl — space view"
	return v
}
