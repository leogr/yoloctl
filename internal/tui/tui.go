// Package tui is the yoloctl multiplexer shell: a two-row top bar
// (brand + palette input, then the tab strip), tabs hosting yoloctl
// subprocesses under PTYs, and a
// transient palette overlay (VS-Code-style) covering the top portion
// of the view while the palette has focus — suggestions while typing,
// the running/last command's output after Enter. Non-TUI commands run
// in the overlay's backing buffer; interactive commands open tabs.
//
// Input model (spike-proven; H4): keys are decoded by bubbletea and
// re-encoded to PTY bytes — raw passthrough stays a documented
// follow-up if real use surfaces fidelity gaps. The palette key
// (default Ctrl+Y) toggles palette/view focus; pressing it again while
// the palette has focus sends the literal byte to the child.
package tui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/palette"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/tabs"
	"github.com/leogr/yoloctl/internal/tui/term"
	"github.com/leogr/yoloctl/internal/tui/yoloctlcli"
	"github.com/mattn/go-runewidth"
)

// Accent palette (256-color — we advertise xterm-256color): muted
// orange for brand/active accents, steel blue for the quiet chrome,
// gray for the quietest text. Foregrounds only — the one background we
// paint is the chrome "surface" (surfaceBg), derived from the detected
// terminal background as a raised panel (~15% of the channel range)
// that stays in the user's theme family rather than fighting it.
const (
	cOrange    = "\x1b[38;5;215m"
	cBlue      = "\x1b[38;5;67m"
	cDim       = "\x1b[38;5;243m"
	cBold      = "\x1b[1m"
	cUnderline = "\x1b[4m"
	cReset     = "\x1b[0m"
)

// barRows is the reserved top area: row 0 brand+input+status, row 1
// the tab strip.
const barRows = 2

type wakeMsg struct{}

// tabExitMsg reports a pane's subprocess exit by its session; the pane is
// looked up where it is now, since a move may have changed its tab.
type tabExitMsg struct{ session *term.Session }
type spinnerTickMsg struct{}
type osc52NoticeMsg struct{ notice string }

// contextMsg carries a freshly-resolved current space for the status-bar
// chip — sent at startup and whenever context.yaml changes underneath the
// TUI (watchContext).
type contextMsg struct{ space string }

// spinnerFrames replaces the seven-column brand while a console command is
// busy; spinnerInterval is the advance cadence (10 fps reads as smooth).
var spinnerFrames = []string{".:.    ", " .:.   ", "  .:.  ", "   .:. ", "    .:.", "   .:. ", "  .:.  ", " .:.   "}

const spinnerInterval = 100 * time.Millisecond

// suggestionsMsg delivers async __complete results for the overlay
// suggestion list; Line guards staleness (the palette may have moved
// on by the time the subprocess answers).
type suggestionsMsg struct {
	Line    string
	Items   []yoloctlcli.Candidate
	Nav     bool // arrive in menu-steering (the ↓-on-empty reveal); typing leaves it false
	NoSpace bool // Items' NoSpace directive bit: accepted values keep the token open
}

// debugTUI writes input-event diagnostics when YOLOCTL_TUI_DEBUG=1 for
// bisecting "nothing happens" reports into delivery vs model problems
// without instrumented builds.
var debugTUI = os.Getenv("YOLOCTL_TUI_DEBUG") == "1"

type model struct {
	standalone   bool
	initialArgs  []string
	attachPrefix *tea.Key
	tabs         tabs.Model
	console      *term.Console // the palette overlay's backing buffer
	pal          palette.Model
	program      *tea.Program // for wake/exit sends from session goroutines
	pending      *atomic.Bool // coalesces onData wakes
	cfg          *config.TUI

	width, height    int
	focusPalette     bool
	scrollOff        int // lines back into the focused surface's scrollback; 0 = live
	status           string
	osc52NoticeShown bool
	browserPrompts   map[*term.Session]browserPrompt
	browserDialog    *browserDialog
	chrome           *chromeGesture // a held left press on strip, bar or border
	confirm          *closeConfirm  // the open "close a running command?" popup
	keyOwners        map[rune]keyTerminal
	consumedKeys     map[rune]bool // keys the prefix layers took, until their release
	hostKitty        bool
	hostGraphemes    bool // mirrors Bubble Tea's mode-2027 width negotiation
	hostBlurred      bool
	hostGeometry     hostGeometry
	frames           *displayFrames // scoped to one View call
	// placeholderFocus is the empty pane the palette last opened for
	// (placeholder.go).
	placeholderFocus *panes.Pane

	// The pane prefix (pane_prefix.go): its resolved chord, an armed prefix,
	// the resize repeat window, prefix c's intent, the tab picker, and the
	// menu notices: the inert notice popup, the menu whose key is acting
	// (pane_menu.go) and a counter of notices posted.
	prefixChord    chord
	prefixResolved bool
	prefix         *prefixState
	resizeTab      *tabs.Tab
	resizeUntil    time.Time
	newTab         *newTabIntent
	picker         *tabPicker
	notice         *noticePopup
	acting         *prefixState
	noticeSeq      int

	mouseOwners          map[tea.MouseButton]mouseOwner
	hostMouseShift       hostMouseShift
	hostMouseEncoding    hostMouseEncoding
	selectionPointer     *tea.Mouse
	selectionTickPending bool

	// Current space for the always-shown status-bar chip. Seeded at startup
	// and kept live by watchContext (re-resolved on every context.yaml
	// change).
	ctxSpace string

	sugg        []yoloctlcli.Candidate // last suggestion list (overlay, while typing)
	suggSel     int                    // selected index into sugg: the ghost / TAB target
	suggNav     bool                   // user is steering the menu with ↑/↓ (Enter then picks, like TAB)
	suggNoSpace bool                   // sugg's NoSpace directive bit (dir prefixes: accepting keeps the token open)

	// lastInterrupt is when the last Ctrl+C was sent to a running console
	// command; a second within 2s escalates from SIGINT to a force-kill.
	lastInterrupt time.Time

	// spinnerFrame indexes spinnerFrames for the "running" indicator;
	// spinning guards a single tea.Tick loop (started per command).
	spinnerFrame int
	spinning     bool

	// Terminal background, from the OSC 11 reply (tea.RequestBackground
	// Color in Init). Until it arrives, surfaceBg assumes dark — the
	// common terminal default. bgIsDark/bgR,G,B are valid only once
	// bgDetected is set.
	bgDetected             bool
	bgIsDark               bool
	bgR, bgG, bgB          uint8
	foreground, background color.Color
	hostPalette            [256]color.Color
	paletteBatch           hostPaletteBatch
}

// Run starts the TUI and blocks until the user quits. ctx cancellation
// (SIGINT/SIGTERM) also stops it; tab subprocesses are killed on the
// way out — agents themselves keep running because clients detach.
func Run(ctx context.Context) error {
	cfg, err := config.LoadTUI()
	if err != nil {
		return err
	}
	m := &model{pending: &atomic.Bool{}, focusPalette: true, cfg: cfg}
	m.prefixChord, m.status = resolvePanePrefix(cfg) // a fallback says so once
	m.prefixResolved = true
	defer m.resetHostMouse(os.Stdout)
	// runProgram owns context cancellation and graceful terminal cleanup.
	p := tea.NewProgram(m, m.programOptions()...)
	m.program = p
	m.console = term.NewConsole(24, 80, cfg.ScrollbackLines, m.wake)
	defer func() { _ = m.console.Close() }()
	m.console.SetOSC52Handler(m.handleOSC52Output)
	m.console.SetBellHandler(m.handleBellOutput)
	m.ctxSpace = m.resolveContext()            // seed the status-bar chip
	if stop := m.watchContext(); stop != nil { // live-update on context.yaml changes
		defer stop()
	}
	err = runProgram(ctx, p)
	m.eachSession(func(p *panes.Pane) { p.Session.Close() })
	if err != nil && ctx.Err() != nil {
		return nil // outer signal: normal shutdown
	}
	return err
}

// runProgram turns CLI cancellation (including process signals) into a graceful
// quit. WithContext would use Tea's kill path, which closes its cancel reader
// without joining the read loop. Its own signal handler can also deadlock on a
// QuitMsg send after that cancellation. Callers disable that second handler;
// Quit uses the normal event loop and Tea's bounded wait for input cleanup.
func runProgram(ctx context.Context, p *tea.Program) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			p.Quit()
		case <-done:
		}
	}()
	_, err := p.Run()
	return err
}

// wake schedules a re-render. Send must NOT be called synchronously
// from inside Update (the event loop isn't reading while Update runs —
// instant self-deadlock); the goroutine makes it safe from any calling
// context, the CAS coalesces bursts.
func (m *model) wake() {
	if m.program == nil { // model under test, no event loop
		m.pending.Store(false)
		return
	}
	if m.pending.CompareAndSwap(false, true) {
		go m.program.Send(wakeMsg{})
	}
}

// handleOSC52Output crosses from a PTY pump goroutine to Bubble Tea's event
// loop. An allowed write becomes RawMsg directly: Bubble Tea executes those
// synchronously in receive order, so two rapid copies cannot land reversed.
// Block notices remain model messages because the status line is model state.
func (m *model) handleOSC52Output(event termio.OSC52) {
	if m.program == nil {
		return
	}
	m.program.Send(m.osc52HostMsg(event))
}

func (m *model) osc52HostMsg(event termio.OSC52) tea.Msg {
	sequence, notice := termio.EvaluateOSC52(event, m.cfg.ClipboardEnabled())
	if sequence != "" {
		return tea.RawMsg{Msg: sequence}
	}
	return osc52NoticeMsg{notice: notice}
}

// resolveContext resolves the ambient space the way a no-flag CLI command
// would (context.yaml pin → "default") for the status-bar chip. Re-reads
// context.yaml every call. Best-effort: any error falls back to the default.
func (m *model) resolveContext() (space string) {
	space, _, err := config.ResolveSpace("", false)
	if err != nil || space == "" {
		space = config.DefaultSpaceName
	}
	return space
}

// watchContext live-updates the space chip when context.yaml
// changes underneath the TUI (a `yoloctl space use ...` in another terminal, or
// a hand edit). It watches the config DIRECTORY — robust against the
// atomic tmp+rename save and the file being absent — and re-resolves on
// any event touching context.yaml. Returns a stop func, or nil if a
// watcher couldn't start (the chip still shows the startup value).
func (m *model) watchContext() func() {
	path, err := layout.ContextPath()
	if err != nil {
		return nil
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	if err := w.Add(filepath.Dir(path)); err != nil {
		_ = w.Close()
		return nil
	}
	go func() {
		target := filepath.Clean(path)
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Clean(ev.Name) == target {
					m.program.Send(contextMsg{space: m.resolveContext()})
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return func() { _ = w.Close() }
}

// spinnerCmd schedules the next spinner advance; the loop continues by
// returning it again from the spinnerTickMsg handler while busy.
func spinnerCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// startSpinner kicks the spinner tick loop for a freshly-started console
// command. Returns nil when a loop is already running (it covers the new
// command), so concurrent commands never stack ticks.
func (m *model) startSpinner() tea.Cmd {
	if m.spinning {
		return nil
	}
	m.spinning = true
	m.spinnerFrame = 0
	return spinnerCmd()
}

// Init measures host color defaults and enables mouse reporting. Color
// requests are read-only; child palette changes never reach the host terminal.
func (m *model) Init() tea.Cmd {
	return tea.Batch(tea.RequestForegroundColor, tea.RequestBackgroundColor, requestHostPalette, m.initHostMouse())
}

func (m *model) updateTerminalColors() {
	m.console.SetColors(m.foreground, m.background)
	m.eachSession(func(p *panes.Pane) { p.Session.SetColors(m.foreground, m.background) })
}

// viewRows is the tab grid height: total minus the two bar rows.
func (m *model) viewRows() int {
	if m.standalone {
		return max(m.height, 1)
	}
	return max(m.height-m.chromeRows(), 3)
}

// tabStripRow is the screen row (0-indexed) the tab strip renders on. It
// MUST mirror View()'s composition: row 1 when a tab is focused, but pushed
// to 1+overlayContentRows() when the palette is focused (the overlay opens
// above it). Reuses the same clamped overlayContentRows() View() uses, so a
// mouse hit-test resolves against the strip's on-screen position exactly —
// including a configured palette_min_height that keeps an empty overlay open.
func (m *model) tabStripRow() int {
	if m.focusPalette {
		return 1 + m.overlayContentRows()
	}
	return 1
}

// overlayContentRows is the palette overlay's DISPLAY height: sized to
// fit the current content (the suggestion list, or the command buffer's
// used rows), clamped to the configured bounds. Recomputed every render,
// so the overlay grows and shrinks as its content changes.
func (m *model) overlayContentRows() int {
	minC, maxC := m.overlayBounds()
	return min(max(m.desiredOverlayContent(), minC), maxC)
}

// overlayPage is the PgUp/PgDown scroll step: the visible overlay height
// less a row of overlap (at least one line).
func (m *model) overlayPage() int {
	return max(m.overlayContentRows()-1, 1)
}

// desiredOverlayContent is the overlay's natural (unclamped) height: the
// content body — one row per suggestion when the menu is up, else the
// console buffer's used rows (output including its exit-code line) — plus
// one trailing blank row that opens a gap before the tab strip below. With
// nothing to show it returns 0, so the overlay collapses to just the input
// line.
func (m *model) desiredOverlayContent() int {
	if prompt := m.modalPopup(1 + m.overlayMaxContentRows()); len(prompt) > 0 {
		return len(prompt) - 1 // top border occupies the usual input row
	}
	var body int
	if len(m.sugg) > 0 {
		body = len(m.sugg)
	} else {
		body = m.consoleContentRows()
	}
	if body == 0 {
		return 0
	}
	return body + 1
}

// consoleContentRows counts the rows the console's output occupies:
// evicted scrollback plus the live grid rows down to the last non-blank
// one.
func (m *model) consoleContentRows() int {
	return m.consoleFrame().ContentRows
}

// consoleGridRows is the number of grid rows down to the last non-blank
// one (GridText right-trims each row, so trailing blanks drop out). The
// console grid is sized to the max, so short output leaves a blank tail
// that this excludes — the overlay then displays only the used region.
func (m *model) consoleGridRows() int {
	f := m.consoleFrame()
	return f.ContentRows - f.History
}

// overlayMaxContentRows is the console grid height: the overlay's max
// content rows. The grid is sized to the max so the child PTY's winsize
// stays stable across commands; overflow scrolls into scrollback and the
// displayed window shrinks to fit via overlayContentRows.
func (m *model) overlayMaxContentRows() int {
	_, maxC := m.overlayBounds()
	return maxC
}

// overlayBounds resolves the palette area bounds (tui.yaml
// palette_min_height / palette_max_height) into overlay CONTENT rows
// (the area minus its status-bar row). Hard rails from the spec: the
// area is never below 1 row nor above 75% of the terminal height; the
// max also leaves at least one tab row below (the push-down invariant).
// Each bound is "" (default), absolute rows ("5"), or a percentage of
// the terminal height ("75%").
func (m *model) overlayBounds() (minContent, maxContent int) {
	const floorArea = 1
	ceilArea := max(m.height*75/100, floorArea)

	minArea := max(resolveHeightSpec(m.cfg.GetPaletteMinHeight(), m.height, floorArea), floorArea)
	maxArea := min(resolveHeightSpec(m.cfg.GetPaletteMaxHeight(), m.height, ceilArea), ceilArea)

	maxContent = max(min(maxArea-1, m.viewRows()-1), 0)
	minContent = min(max(minArea-1, 0), maxContent)
	return minContent, maxContent
}

// resolveHeightSpec turns a palette-bound spec into absolute rows: "" →
// def, "N%" → that percent of the terminal height, "N" → N rows;
// anything unparseable or non-positive → def. Callers apply the rails.
func resolveHeightSpec(spec string, height, def int) int {
	if spec == "" {
		return def
	}
	if pct, ok := strings.CutSuffix(spec, "%"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(pct)); err == nil && n > 0 {
			return max(height*n/100, 1)
		}
		return def
	}
	if n, err := strconv.Atoi(spec); err == nil && n > 0 {
		return n
	}
	return def
}

// opensLiveSurface reports whether a submitted palette argv hands the
// user an ongoing terminal context that belongs in its own tab, rather
// than the palette's command/result overlay. The split follows the
// placement rule: palette = yoloctl
// command/result surface, tabs = live terminal surfaces. A tab opens
// only for argv that
//
//   - attaches to an agent/session — `attach` / `box attach`;
//   - opens a sandbox/VM terminal — `box exec`, `space ssh` / `space shell`;
//   - shows a live full-screen view — `space view`;
//   - reads a value from stdin — `account secret set` without `--from-env`
//     (the palette overlay can't reliably feed multi-char/pasted stdin to the
//     running command);
//   - opens $EDITOR — `box edit`, `account edit`, `<catalog> edit`;
//   - follows/watches a stream until interrupted — `box logs --follow`,
//     `space logs --follow`.
//
// Everything else stays in the palette, including help (`-h` / `--help` /
// `help`), list/show, config mutations, lifecycle commands, and the
// FINITE forms of logs — slowness or large bounded output is not a
// tab criterion. Aliases share their canonical command's placement (`sp` =
// `space`). The predicate is local, deterministic, and side-effect-free: it
// parses only enough to tell
// finite from streaming (`--follow` / `-f`) and to spot editor-backed
// subcommands, and never reads the agent's own args past `--`.
func opensLiveSurface(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "chat":
		return chatOpensLiveSurface(args[1:])
	case "attach": // root alias for `box attach` — joins a live agent session
		return true
	case "box":
		return boxOpensLiveSurface(args[1:])
	case "space", "sp":
		return spaceOpensLiveSurface(args[1:])
	case "account":
		return accountOpensLiveSurface(args[1:])
	case "agent", "feature", "capability":
		// Catalog command trees: only the editor-backed
		// `<kind> edit` opens a tab; ls / show / copy / destroy (and agent's
		// other tooling verbs) are finite palette commands.
		return catalogOpensLiveSurface(args[1:])
	}
	return false
}

func chatOpensLiveSurface(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--help" || a == "-h" || a == "help" {
			return false
		}
		if a == "--space" || a == "--box" || a == "--human" {
			i++
			if i >= len(args) {
				return false
			}
			continue
		}
		if strings.HasPrefix(a, "--space=") || strings.HasPrefix(a, "--box=") || strings.HasPrefix(a, "--human=") {
			continue
		}
		// Subcommands are finite controller operations. Unknown arguments are
		// also left in the palette to show Cobra's normal diagnostic.
		return false
	}
	return true
}

// catalogOpensLiveSurface decides placement within a catalog subtree (agent /
// feature / capability): only the editor-backed `edit` opens a tab.
func catalogOpensLiveSurface(sub []string) bool {
	return len(sub) > 0 && sub[0] == "edit"
}

// accountOpensLiveSurface decides placement within the `account` subtree. A tab
// opens for the editor-backed `account edit` and `account secret set` WITHOUT
// --from-env (which reads the value from stdin). ls / show / create / destroy,
// the `account auth` verbs (import/status), and `account secret set --from-env`
// / unset / ls are finite palette commands.
func accountOpensLiveSurface(sub []string) bool {
	if len(sub) == 0 {
		return false
	}
	switch sub[0] {
	case "edit":
		return true
	case "secret":
		// `account secret set` reads the secret VALUE from stdin (a full line)
		// unless --from-env names an environment variable. A stdin-reading command
		// must own a tab: the palette overlay can't reliably feed multi-char /
		// pasted stdin to the running command. --from-env reads no
		// stdin, so it stays in the palette, as do unset / ls.
		return len(sub) > 1 && sub[1] == "set" && !hasFromEnv(sub[2:])
	}
	return false
}

// boxOpensLiveSurface decides placement within the `box` subtree: the
// Live attach/exec and the editor-backed `box edit`
// always open a tab; `logs` opens one only when streaming. The lifecycle verbs
// create/copy/start/restart/recreate auto-attach by default, so they open a
// tab too — UNLESS the argv suppresses the attach (--no-attach,
// --manifest-only, or help). Remaining verbs (ls/stop/destroy/…) stay in the
// palette (the default false).
func boxOpensLiveSurface(sub []string) bool {
	if len(sub) == 0 {
		return false
	}
	switch sub[0] {
	case "attach", "exec", "edit":
		return true
	case "create", "copy", "start", "restart", "recreate":
		return !boxLifecycleAttachSuppressed(sub[0], sub[1:])
	case "logs":
		return hasFollow(sub[1:])
	case "use":
		return boxUseReadsStdin(sub[1:])
	}
	return false
}

// Finite box-use responses belong in the palette. Its explicit stdin forms
// need a tab to own input, just like account secret set without --from-env.
func boxUseReadsStdin(args []string) bool {
	if hasHelp(args) {
		return false
	}
	stdin := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--stdin" {
			stdin = true
		}
		if value, ok := strings.CutPrefix(arg, "--stdin="); ok {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return false
			}
			stdin = parsed
		}
	}
	return stdin
}

// boxLifecycleAttachSuppressed reports whether a lifecycle argv suppresses
// the default auto-attach: --no-attach, --manifest-only (create/copy write
// YAML only — nothing to attach), or a help request. Mirrors cobra's boolean
// semantics — bare flag = true, `=VAL` parses, clustered boolean shorthands
// are evaluated left to right, and the LAST value of a repeated flag wins —
// and treats an invalid boolean value as suppressed: cobra will fail fast
// with a flag error, which belongs in the palette, not a tab. Recreate's `-f`
// is --force and does not itself suppress; the scan stops at the `--`
// separator so an agent's own args are never read.
func boxLifecycleAttachSuppressed(verb string, args []string) bool {
	noAttach, manifestOnly, help, invalid := false, false, false, false
	track := func(a, name string, current bool) (bool, bool) {
		if a == name {
			return true, true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				// Invalid bool: cobra fails on the FIRST bad value, so a
				// later valid occurrence cannot rescue the command — latch
				// it (separately from the last-wins value) and stay in the
				// palette, where the flag error renders.
				invalid = true
				return current, true
			}
			return b, true
		}
		return current, false
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if v, ok := track(a, "--no-attach", noAttach); ok {
			noAttach = v
			continue
		}
		if v, ok := track(a, "--manifest-only", manifestOnly); ok {
			manifestOnly = v
			continue
		}
		if v, ok := track(a, "--help", help); ok {
			help = v
			continue
		}
		if v, ok := track(a, "-h", help); ok {
			help = v
			continue
		}
		// pflag permits clusters of boolean shorthands. Of these lifecycle
		// commands only recreate defines another shorthand (`-f`/--force),
		// so `recreate -fh` is force + help while `recreate -fh=false`
		// leaves help disabled. Unknown shorthand characters are flag errors
		// and therefore belong in the palette too.
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 2 {
			cluster := a[1:]
			for len(cluster) > 0 {
				shorthand := cluster[0]
				cluster = cluster[1:]
				if shorthand != 'h' && (verb != "recreate" || shorthand != 'f') {
					invalid = true
					break
				}
				value := true
				if strings.HasPrefix(cluster, "=") {
					var err error
					value, err = strconv.ParseBool(cluster[1:])
					if err != nil {
						invalid = true
					}
					cluster = ""
				}
				if shorthand == 'h' {
					help = value
				}
			}
		}
	}
	return noAttach || manifestOnly || help || invalid
}

// spaceOpensLiveSurface decides placement within the `space` subtree
// (shared by the `sp` alias): ssh/shell open a VM terminal, `view` is a live
// full-screen view unless help was asked for, and `logs` opens a tab only
// when streaming. Resource editing owns a terminal for $EDITOR.
func spaceOpensLiveSurface(sub []string) bool {
	if len(sub) == 0 {
		return false
	}
	switch sub[0] {
	case "ssh", "shell":
		return true
	case "view", "edit":
		return !hasHelp(sub[1:])
	case "logs":
		return hasFollow(sub[1:])
	}
	return false
}

// isSpaceView reports a `space view` tab, which may ask for attach tabs.
func isSpaceView(args []string) bool {
	return len(args) > 1 && (args[0] == "space" || args[0] == "sp") && args[1] == "view"
}

// hasHelp reports a help request before the `--` separator, honouring an
// explicit `--help=false`.
func hasHelp(args []string) bool {
	help := false
	for _, a := range args {
		switch {
		case a == "--":
			return help
		case a == "-h" || a == "--help":
			help = true
		case strings.HasPrefix(a, "--help="):
			help, _ = strconv.ParseBool(strings.TrimPrefix(a, "--help="))
		}
	}
	return help
}

// hasFollow reports whether a yoloctl argv requests streaming mode
// (`--follow` / `-f`). It is consulted only for commands that have both a
// finite and a streaming form (box logs, space logs), so mode —
// not command name — decides their placement. The scan stops at
// the `--` agent-arg separator so an agent's own `-f` is never counted,
// and it honours an explicit `--follow=false`.
func hasFollow(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--follow" || a == "-f" {
			return true
		}
		if v, ok := strings.CutPrefix(a, "--follow="); ok {
			b, _ := strconv.ParseBool(v)
			return b
		}
	}
	return false
}

// hasFromEnv reports whether a yoloctl argv carries the --from-env flag (bare,
// `--from-env=VAR`, or `--from-env VAR`). `account secret set` with it reads the
// value from an environment variable instead of stdin, so it needs no tab. The
// scan stops at the `--` separator.
func hasFromEnv(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--from-env" || strings.HasPrefix(a, "--from-env=") {
			return true
		}
	}
	return false
}

// dispatch routes a submitted palette line: a live-surface command opens
// its own tab, everything else runs in the overlay buffer.
// Verbatim argv either way. Returns the spinner tick Cmd when a console
// command starts (nil otherwise), so the "running" label animates.
func (m *model) dispatch(args []string) tea.Cmd {
	m.sugg = nil
	if n := m.newTab; n != nil {
		// Prefix c: a live command opens a new tab, bypassing an empty pane;
		// the draft set aside comes back either way.
		m.newTab = nil
		defer m.pal.Restore(n.draft)
		if opensLiveSurface(args) {
			m.openTab(args)
			return nil
		}
	}
	if opensLiveSurface(args) {
		// The current tab's empty pane takes a live command; else a new tab.
		if t := m.tabs.Current(); t != nil && t.Layout.Empty() != nil {
			m.fillPlaceholder(t, t.Layout.Empty(), args)
		} else {
			m.openTab(args)
		}
		return nil
	}
	self, err := executable()
	if err != nil {
		m.status = "cannot locate yoloctl binary: " + err.Error()
		return nil
	}
	started, err := m.console.Run(nil, promptLine(args), append([]string{self}, args...)...)
	if !started {
		m.status = "a palette command is still running — wait for it"
		return nil
	}
	if err != nil {
		return nil // Run echoed the error; nothing is running to spin for
	}
	m.scrollToLive()
	m.status = ""
	return m.startSpinner()
}

// promptLine echoes the public command without the host working directory.
func promptLine(args []string) string {
	return cOrange + "❯ " + cReset + "yoloctl " + strings.Join(args, " ")
}

// openTab launches `yoloctl args...` as a subprocess tab.
func (m *model) openTab(args []string) {
	p := m.startPane(args, m.viewRows(), max(m.width, 20))
	if p == nil {
		return
	}
	t := tabs.New(p)
	m.tabs.Add(t)
	m.layoutTab(t)
	m.focusPalette = false
	m.clearConsoleOutput() // opening a tab takes focus off the palette
	m.status = ""
	m.watchPaneExit(p)
}

// closeFocusedPane closes the current tab's focused pane (a detach for
// managed views), asking first when it runs a command that closing may
// end; the surviving panes take its space and are resized. Its gesture
// owners and selection are dropped.
func (m *model) closeFocusedPane() {
	if t, p := m.tabs.Current(), m.tabs.Focused(); p != nil {
		m.requestDetachPane(t, p)
	}
}

// executable locates the yoloctl binary a pane runs; a seam for tests,
// which must never run the test binary with CLI arguments.
var executable = os.Executable

// startPane starts `yoloctl args...` under a PTY of rows×cols for a pane.
func (m *model) startPane(args []string, rows, cols int) *panes.Pane {
	exe, err := executable()
	if err != nil {
		m.status = "cannot locate yoloctl binary: " + err.Error()
		return nil
	}
	// A pane's requests name it, so an empty pane of its tab can take them;
	// one arriving before Start returns names no pane.
	var self atomic.Pointer[term.Session]
	s, err := term.Start(term.Options{
		TrustedView:     true,
		Argv:            append([]string{exe}, args...),
		Rows:            max(rows, 1),
		Cols:            max(cols, 1),
		CellWidth:       m.hostGeometry.width,
		CellHeight:      m.hostGeometry.height,
		ScrollbackLines: m.cfg.ScrollbackLines,
		OnData:          m.wake,
		OnOSC52:         m.handleOSC52Output,
		OnBell:          m.paneBellHandler(&self),
		OnBrowser:       m.browserHandler(),
		OnTabRequest:    m.tabRequestHandler(args, &self),
		EndHint:         m.reattachHint(args),
		Foreground:      m.foreground,
		Background:      m.background,
		Palette:         m.hostPalette,
	})
	if err != nil {
		m.status = err.Error()
		return nil
	}
	self.Store(s)
	return &panes.Pane{Session: s, Command: "yoloctl " + strings.Join(args, " "), Args: slices.Clone(args)}
}

// watchPaneExit reports a pane's subprocess exit to the event loop.
func (m *model) watchPaneExit(p *panes.Pane) {
	s := p.Session
	go func() {
		<-s.Done()
		if m.program != nil { // nil only in model tests
			m.program.Send(tabExitMsg{session: s})
		}
	}()
}

func (m *model) Update(msg tea.Msg) (next tea.Model, cmd tea.Cmd) {
	defer func() {
		m.checkPrefix()
		m.checkPicker()
		m.checkChrome()
		m.checkConfirm()
		m.syncPlaceholder()
		m.syncFocus()
		cmd = tea.Batch(cmd, m.syncMouseShift(), m.syncHostGeometry(), m.syncHostMouseEncoding())
	}()
	switch msg := msg.(type) {
	case tea.RawMsg:
		// Bubble Tea has queued this sequence before invoking Update. Only
		// our typed host request acknowledges a transition; child output
		// and unrelated raw effects cannot impersonate this state.
		switch request := msg.Msg.(type) {
		case mouseShiftRequest:
			m.hostMouseShift.pending = false
			m.hostMouseShift.known = true
			m.hostMouseShift.capture = bool(request)
		case mouseInitRequest:
			m.hostMouseEncoding.pending = false
			m.hostMouseEncoding.tracking = 1002
		case mouseTrackingRequest:
			m.hostMouseEncoding.pending = false
			m.hostMouseEncoding.tracking = int(request)
		}
		return m, nil
	case tea.ModeReportMsg:
		if msg.Mode == ansi.ModeMouseExtSgrPixel {
			m.mouseEncodingReport(msg.Value)
		}
		if msg.Mode == ansi.ModeUnicodeCore && (msg.Value == ansi.ModeReset || msg.Value == ansi.ModeSet || msg.Value == ansi.ModePermanentlySet) {
			m.hostGraphemes = true
		}
		return m, nil
	case mouseEncodingTimeout:
		h := &m.hostMouseEncoding
		if uint64(msg) == h.epoch && h.negotiating {
			h.negotiating, h.failed = false, true
			m.status = "host mouse encoding was not confirmed; reopen yoloctl to restore mouse reporting"
			return m, tea.Raw(mouseTrackingOff + mouseEncodingOff)
		}
		return m, nil
	case tea.FocusMsg:
		m.hostBlurred = false
		return m, tea.Batch(requestHostPalette, m.measureHostGeometry())
	case uv.CellSizeEvent:
		m.acceptHostCellSize(msg.Width, msg.Height)
		return m, nil
	case geometryTimeout:
		if uint64(msg) == m.hostGeometry.request {
			m.hostGeometry.pending = false
		}
		return m, nil
	case tea.BlurMsg:
		m.hostBlurred = true
		m.cancelChrome() // host focus loss cancels a chrome drag
		m.disarmPrefix() // and disarms the prefix; an open picker stays
		return m, nil
	case wakeMsg:
		m.pending.Store(false)
		m.reportInputErrors()
		return m, nil

	case contextMsg:
		m.ctxSpace = msg.space
		return m, nil

	case prefixTickMsg: // a prefix window or notice expired: repaint
		return m, nil

	case spinnerTickMsg:
		// Advance only while a command is still running; stop the loop
		// (drop the reschedule) once idle, restarted by the next dispatch.
		if m.console.Busy() {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, spinnerCmd()
		}
		m.spinning = false
		return m, nil

	case osc52NoticeMsg:
		if msg.notice == "" {
			return m, nil
		}
		if msg.notice == termio.OSC52DisabledNotice {
			if m.osc52NoticeShown {
				return m, nil
			}
			m.osc52NoticeShown = true
		}
		m.status = msg.notice
		return m, nil

	case tea.ForegroundColorMsg:
		if msg.Color != nil {
			m.foreground = msg.Color
			m.updateTerminalColors()
		}
		return m, nil

	case hostPaletteStart:
		return m, hostPaletteDeadline(uint64(msg))

	case hostPaletteFlush:
		m.flushHostPalette(uint64(msg))
		return m, nil

	case uv.UnknownOscEvent:
		m.updateHostPalette(string(msg))
		return m, nil

	case tea.BackgroundColorMsg:
		if msg.Color != nil { // nil when the terminal didn't answer
			m.background = msg.Color
			r, g, b, _ := msg.RGBA() // 16-bit, alpha-premultiplied
			m.bgDetected = true
			m.bgIsDark = msg.IsDark()
			m.bgR, m.bgG, m.bgB = uint8(r>>8), uint8(g>>8), uint8(b>>8)
			m.updateTerminalColors()
		}
		return m, nil

	case tea.KeyboardEnhancementsMsg:
		// Even CSI ?0u is a support response: zero means no active flags.
		m.hostKitty = true
		if debugTUI {
			debugLogf("host-keyboard-enhancements flags=%d disambiguation=%t event_types=%t alternate_keys=%t all_keys_escape=%t associated_text=%t",
				msg.Flags,
				msg.SupportsKeyDisambiguation(),
				msg.SupportsEventTypes(),
				msg.SupportsAlternateKeys(),
				msg.SupportsAllKeysAsEscapeCodes(),
				msg.SupportsAssociatedText(),
			)
		}
		return m, nil

	case suggestionsMsg:
		if line, _ := m.pal.Line(); m.focusPalette && line == msg.Line {
			// A fresh list resets the selection to the first item (the
			// default ghost / TAB target); typing re-anchors here and stays
			// out of steering, a ↓-on-empty reveal arrives already steering.
			m.sugg, m.suggSel = msg.Items, 0
			m.suggNav = msg.Nav && len(msg.Items) > 0
			m.suggNoSpace = msg.NoSpace
		}
		return m, nil

	case tabRequestMsg:
		m.handleTabRequest(msg.req, msg.chat, msg.from)
		return m, nil

	case tabExitMsg:
		t, p := m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == msg.session })
		if p == nil {
			return m, nil // its view was closed first
		}
		p.Exited = true
		m.reportInputErrors()
		if m.standalone {
			return m, tea.Quit
		}
		m.markUnfocused(t, p) // exited while unfocused: flag it
		return m, nil

	case paneBellMsg:
		if t, p := m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == msg.from }); p != nil {
			m.markUnfocused(t, p)
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.standalone && len(m.initialArgs) > 0 {
			args := m.initialArgs
			m.initialArgs = nil
			m.openTab(args)
			if m.tabs.Current() == nil {
				return m, tea.Quit
			}
		}
		m.resizeSurfaces()
		return m, m.measureHostGeometry()

	case tea.KeyPressMsg:
		if cmd, handled := m.browserKey(normalizeHostKey(msg.Key(), m.hostKitty)); handled {
			return m, cmd
		}
		if m.confirm != nil {
			m.confirmKey(normalizeHostKey(msg.Key(), m.hostKitty)) // modal: every key is its
			return m, nil
		}
		if k := msg.Key(); m.chrome != nil && k.Code == tea.KeyEscape {
			m.cancelChrome() // Esc cancels a chrome drag and is consumed
			return m, nil
		}
		if m.standalone {
			return m.attachedKey(normalizeHostKey(msg.Key(), m.hostKitty))
		}
		if cmd, taken := m.prefixLayer(normalizeHostKey(msg.Key(), m.hostKitty)); taken {
			return m, cmd
		}
		return m.handleKey(tea.KeyPressMsg(normalizeHostKey(msg.Key(), m.hostKitty)))
	case browserRequestMsg:
		return m, m.browserRequest(msg)
	case browserFinishedMsg:
		m.finishBrowserPrompt(msg.surface, msg.id)
		if msg.err != nil {
			m.status = msg.err.Error()
		}
		return m, nil
	case tea.KeyReleaseMsg:
		k := normalizeHostKey(msg.Key(), m.hostKitty)
		if m.takeConsumedRelease(k) {
			return m, nil
		}
		if owner := m.keyOwners[keyIdentity(k)]; owner != nil {
			m.sendKey(owner, k, true)
			delete(m.keyOwners, keyIdentity(k))
		}
		return m, nil

	case tea.PasteMsg:
		m.disarmPrefix()
		if m.browserDialog != nil || m.confirm != nil || m.picker != nil {
			return m, nil
		}
		m.clearTabSelection()
		if m.focusPalette && m.console.Busy() {
			m.sendInput(m.console, pasteBytes(msg.Content, m.console.BracketedPaste()))
		} else if m.focusPalette {
			m.pal.Insert(msg.Content)
			m.refreshSuggestions()
		} else if t := m.tabs.Focused(); t != nil && t.Empty() {
			m.status = "nothing takes input here" // the empty pane's input is the palette
		} else if t != nil && !t.Exited {
			// Bracket only when the child enabled DECSET 2004
			// (mode tracker; H2).
			m.sendInput(t.Session, pasteBytes(msg.Content, t.Session.BracketedPaste()))
		}
		return m, nil

	case selectionTickMsg:
		m.selectionTickPending = false
		return m, m.scrollSelection()
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.MouseMotionMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// Input is enqueued without blocking the event loop. Rejected input is explicit;
// asynchronous PTY failures are collected on a wake from the writer.
func (m *model) sendInput(surface interface{ SendInput([]byte) error }, p []byte) {
	if err := surface.SendInput(p); err != nil {
		m.status = err.Error()
	}
}

func (m *model) reportInputErrors() {
	if m.console != nil {
		if err := m.console.TakeInputError(); err != nil {
			m.status = "console: " + err.Error()
		}
	}
	m.eachSession(func(p *panes.Pane) {
		if err := p.Session.TakeInputError(); err != nil {
			m.status = p.Title() + ": " + err.Error()
		}
	})
	m.reportViewErrors()
}

// Managed admission/stream failures are command results, not status chips.
// Leave them pending on their session while the palette is busy, and don't
// steal input focus from another live tab for a background failure.
func (m *model) reportViewErrors() {
	if !m.standalone && m.console.Busy() {
		return
	}
	var messages []string
	focus := m.focusPalette
	m.eachPane(func(tab *tabs.Tab, p *panes.Pane) {
		if p.Empty() {
			return
		}
		if err := p.Session.TakeViewError(); err != nil {
			message := p.Title() + ": " + err.Error()
			if m.standalone {
				m.status = message
				return
			}
			messages = append(messages, message)
			if p == m.tabs.Focused() {
				focus = true
			} else if tab != m.tabs.Current() {
				tab.Badge = true
			}
		}
	})
	if len(messages) > 0 && m.console.ShowError(strings.Join(messages, "\n\n")) {
		m.clearSuggestions()
		m.status = ""
		m.focusPalette = focus
		if focus {
			m.scrollToLive()
		}
	}
}

func pasteBytes(content string, bracketed bool) []byte {
	if bracketed {
		return []byte("\x1b[200~" + content + "\x1b[201~")
	}
	return []byte(content)
}

func alternateScrollKey(button tea.MouseButton, applicationCursor bool) []byte {
	switch button {
	case tea.MouseWheelUp:
		return cursorKey('A', applicationCursor)
	case tea.MouseWheelDown:
		return cursorKey('B', applicationCursor)
	default:
		return nil
	}
}

// handleChromeClick routes a left-button PRESS that landed on yoloctl's own
// chrome — the palette row / overlay, the tab strip, or (only while the
// palette is focused) the view body — to focus/tab activation. Returns true
// when it consumed the click, so handleMouse neither forwards it to the child
// nor scrolls, and retains ownership of the entire gesture. stripRow is read up front, so
// the hit-test reflects the screen at click time even though the actions move
// focus (which would move the strip on the next render).
func (m *model) handleChromeClick(mo tea.Mouse) bool {
	if m.standalone {
		return false
	}
	stripRow := m.tabStripRow()
	switch {
	case mo.Y < stripRow:
		// Row 0 (the input line) and, while focused, the overlay rows above
		// the strip — "the palette area". A click focuses it.
		m.enterPalette()
		return true
	case mo.Y == stripRow:
		// The tab strip: a click on a tab activates and focuses it; a click on
		// the margin / arrows / trailing pad does nothing. Either way the
		// chrome row never reaches a child.
		if i := m.tabAt(mo.X); i >= 0 {
			m.activateTab(i)
		}
		return true
	default:
		// The view body. While the palette is focused, a click returns focus
		// to the view. While a tab is focused, fall through so
		// the existing session passthrough forwards the click to the child.
		p := m.paneAt(m.mouseHit(mo))
		if t := m.tabs.Current(); p != nil && p.Empty() && t != nil {
			// The empty pane's input is the palette: a press focuses it and
			// opens the palette.
			t.Layout.SetFocus(p)
			m.enterPalette()
			return true
		}
		if m.focusPalette {
			// The click's pane, if any, takes focus with the view.
			m.focusView()
			if t := m.tabs.Current(); p != nil && t != nil {
				t.Layout.SetFocus(p)
			}
			return true
		}
		return false
	}
}

// enterPalette gives the palette focus from a tab — mirrors Ctrl+Y-from-tab
// (focus + jump to live, no console wipe). A HARD no-op when the palette is
// already focused: it must not reset scrollOff (the user may be scrolled back
// in the overlay) nor wipe a running command's output.
func (m *model) enterPalette() {
	if m.focusPalette {
		return
	}
	m.focusPalette = true
	m.scrollToLive()
}

// leavePaletteForTab moves focus from the palette to the active tab. A
// running console command detaches with its output preserved (mirrors Ctrl+Y
// while busy); an idle palette wipes the ephemeral overlay (mirrors Esc).
// Callers ensure a tab exists.
func (m *model) leavePaletteForTab() {
	m.focusPalette = false
	if m.console.Busy() {
		m.scrollToLive()
	} else {
		m.clearConsoleOutput() // also resets scrollOff and suggNav
	}
}

// activateTab selects tab i and gives it focus, mirroring a keyboard tab
// switch (tabs.Select clears the tab's badge) plus the leave-palette side
// effects when the click came from the palette.
func (m *model) activateTab(i int) {
	wasPalette := m.focusPalette
	m.tabs.Select(i)
	if wasPalette {
		m.leavePaletteForTab()
	} else {
		m.scrollToLive() // already on a tab: jump to live, like a keyboard switch
	}
}

// activatePane switches to a tab and focuses one of its panes, leaving the
// palette, as reusing a tab for a request always did.
func (m *model) activatePane(t *tabs.Tab, p *panes.Pane) {
	for i, tab := range m.tabs.Tabs {
		if tab == t {
			m.tabs.Select(i)
			t.Layout.SetFocus(p)
			m.focusPalette = false
			return
		}
	}
}

// focusView returns focus from the palette to the active tab without
// switching tabs (the "click in the view returns focus to the view"
// gesture). No-op when no tab exists — the click then stays in the palette.
func (m *model) focusView() {
	if m.tabs.Current() == nil {
		return
	}
	m.leavePaletteForTab()
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()
	paletteKey := isPaletteKey(msg, m.cfg.GetPaletteKey())
	if !paletteKey {
		if cmd, copied := m.copySelection(k); copied {
			return m, cmd
		}
		// Modifier-only reports must not erase a selection before its copy key.
		if k.Code < tea.KeyLeftShift || k.Code > tea.KeyIsoLevel5Shift {
			m.clearTabSelection()
		}
	}
	if debugTUI {
		consoleBusy := false
		if m.console != nil {
			consoleBusy = m.console.Busy()
		}
		debugLogf("key-press stroke=%q code=%d text=%q mod=%d shifted=%d base=%d repeat=%t focus_palette=%t palette_key=%t console_busy=%t",
			msg.Keystroke(), k.Code, k.Text, k.Mod, k.ShiftedCode, k.BaseCode, k.IsRepeat, m.focusPalette, paletteKey, consoleBusy)
	}

	if !m.focusPalette {
		if paletteKey {
			m.focusPalette = true
			m.scrollToLive()
			return m, nil
		}
		cur := m.tabs.Focused()
		switch {
		case cur == nil:
			m.focusPalette = true // nothing else can take input
		case cur.Empty():
			m.focusPalette = true // the empty pane's input is the palette
		case cur.Exited:
			// The child is gone; the tab shows its exit status + a close
			// hint. Chrome keys still work; Enter/Ctrl+W dismiss it.
			return m, m.handleExitedTabKey(k)
		default:
			m.scrollToLive()
			m.sendKey(cur.Session, k, false)
			return m, nil
		}
	}

	// Palette focus. While a console command runs, the overlay is its
	// terminal: keys feed its stdin (like a tab), only chrome reserved.
	if m.console.Busy() {
		return m.handleRunningKey(k, paletteKey)
	}

	cur := m.tabs.Focused()
	switch {
	case paletteKey && cur != nil && cur.Empty():
		// An empty pane takes no key: the palette key hides the palette,
		// and the next key typed opens it again.
		m.focusPalette = false
		m.clearConsoleOutput()
	case paletteKey: // double-tap: pass the literal key to the child
		if cur != nil {
			m.sendKey(cur.Session, k, false)
			m.focusPalette = false
			m.clearConsoleOutput() // focus left the palette: wipe output
		}
	case k.Code == tea.KeyEscape && m.newTab != nil:
		m.cancelNewTab() // Esc ends prefix c; an empty pane stays
	case k.Code == tea.KeyEscape && m.inChooser():
		// Esc on the empty pane's empty line removes it; the sibling takes
		// the space and the palette hides.
		t := m.tabs.Current()
		m.detachPane(t, t.Focused())
		if m.tabs.Current() != nil {
			m.focusPalette = false
			m.clearConsoleOutput()
		}
	case k.Code == tea.KeyEscape:
		m.focusPalette = false // hide the overlay
		m.clearConsoleOutput() // focus left the palette: wipe output
	case k.Code == tea.KeyPgUp: // page the overlay's scrollback up
		m.scrollBy(m.overlayPage())
	case k.Code == tea.KeyPgDown:
		m.scrollBy(-m.overlayPage())
	case k.Code == tea.KeyEnter:
		// While steering the menu with ↑/↓, Enter picks the selection
		// (like TAB) instead of submitting — you confirm, then run.
		if m.suggNav && len(m.sugg) > 0 {
			m.completePalette()
			m.refreshSuggestions()
		} else if args := m.pal.Submit(); args != nil {
			return m, m.dispatch(args)
		}
	case k.Code == tea.KeyBackspace:
		m.pal.Backspace()
		m.refreshSuggestions()
	case k.Code == tea.KeyDelete:
		m.pal.Delete() // forward delete (Del key, ESC[3~)
		m.refreshSuggestions()
	case k.Code == tea.KeyTab && k.Mod & ^(tea.ModCapsLock|tea.ModNumLock) == 0:
		m.completePalette()
		m.refreshSuggestions()
	// Tab switching: Ctrl/Alt+arrows cycle, Ctrl/Alt+digit jumps —
	// where the host terminal doesn't shadow them (eyeball finding E3:
	// ghostty claims Alt+digit; plain arrows stay line-editing keys).
	case k.Code == tea.KeyLeft && k.Mod&(tea.ModCtrl|tea.ModAlt) != 0:
		m.tabs.Prev()
		m.scrollToLive()
	case k.Code == tea.KeyRight && k.Mod&(tea.ModCtrl|tea.ModAlt) != 0:
		m.tabs.Next()
		m.scrollToLive()
	case k.Code >= '1' && k.Code <= '9' && k.Mod&(tea.ModAlt|tea.ModCtrl) != 0:
		m.tabs.Select(int(k.Code - '1'))
		m.scrollToLive()
	case k.Code == 'w' && k.Mod&tea.ModCtrl != 0:
		m.closeFocusedPane() // detach: the agent keeps running
		m.scrollToLive()
	case k.Code == 'd' && k.Mod&tea.ModCtrl != 0:
		if line, _ := m.pal.Line(); line == "" {
			return m, tea.Quit
		}
	// Up/Down behave like a shell: a single vertical axis running
	// [older history … newer history] → [draft] → [suggestion menu]. Up
	// walks toward older history and always advances while history remains
	// (recalling a line must NOT reopen the menu, or the next Up would
	// hijack into it — the old bug); Down walks toward newer history, then
	// past the empty draft reveals the menu, then steers it. Once the menu
	// is up its selection is the ghost / TAB / Enter target (suggNav flips
	// Enter to "pick").
	case k.Code == tea.KeyUp:
		line, _ := m.pal.Line()
		switch {
		case len(m.sugg) > 0 && m.suggSel > 0: // steer the menu up
			m.suggSel, m.suggNav = m.suggSel-1, true
		case len(m.sugg) > 0 && line == "": // top of the revealed menu: back to the draft
			m.clearSuggestions()
		case len(m.sugg) > 0: // top of a typed menu: hold (don't drop the typed line)
			m.suggSel, m.suggNav = 0, true
		default: // browse history back — no menu, so the next Up keeps going
			m.clearSuggestions()
			m.pal.HistPrev()
		}
	case k.Code == tea.KeyDown:
		switch line, _ := m.pal.Line(); {
		case len(m.sugg) > 0: // steer the menu down
			m.suggSel, m.suggNav = min(m.suggSel+1, len(m.sugg)-1), true
		case line == "":
			m.showAllSuggestions() // past the draft: ↓ reveals the full menu
		default: // browse history forward — no menu hijack
			m.clearSuggestions()
			m.pal.HistNext()
		}
	case k.Code == tea.KeyLeft:
		m.pal.Left()
	case k.Code == tea.KeyRight:
		m.pal.Right()
	case k.Code == tea.KeyHome:
		m.pal.Home()
	case k.Code == tea.KeyEnd:
		m.pal.End()
	default:
		if k.Text != "" && k.Mod&^(tea.ModShift|tea.ModCapsLock|tea.ModNumLock) == 0 {
			m.pal.Insert(k.Text)
			m.refreshSuggestions()
		}
	}
	return m, nil
}

// handleRunningKey routes a keypress while a console command is running:
// the overlay is its terminal. Reserved chrome — detach (palette key,
// keeps the command running), tab nav, and scroll — is intercepted;
// Ctrl+C interrupts (SIGINT, then a force-kill on a repeat within 2s);
// everything else is encoded to the command's stdin (the PTY echoes it).
func (m *model) handleRunningKey(k tea.Key, paletteKey bool) (tea.Model, tea.Cmd) {
	switch {
	case paletteKey:
		m.focusPalette = false // detach; the command keeps running, output kept
		m.scrollToLive()
	case isInterruptKey(k):
		if time.Since(m.lastInterrupt) < 2*time.Second {
			m.console.Kill() // escalate: force-kill the process group
		} else {
			if debugTUI {
				debugLogf("key-forward target=console code=%d text=%q mod=%d bytes=%q reason=interrupt", k.Code, k.Text, k.Mod, []byte{0x03})
			}
			m.sendInput(m.console, []byte{0x03}) // SIGINT via the PTY
		}
		m.lastInterrupt = time.Now()
	case k.Code == tea.KeyPgUp:
		m.scrollBy(m.overlayPage())
	case k.Code == tea.KeyPgDown:
		m.scrollBy(-m.overlayPage())
	case k.Code == tea.KeyLeft && k.Mod&(tea.ModCtrl|tea.ModAlt) != 0:
		m.tabs.Prev()
		m.scrollToLive()
	case k.Code == tea.KeyRight && k.Mod&(tea.ModCtrl|tea.ModAlt) != 0:
		m.tabs.Next()
		m.scrollToLive()
	case k.Code >= '1' && k.Code <= '9' && k.Mod&(tea.ModAlt|tea.ModCtrl) != 0:
		m.tabs.Select(int(k.Code - '1'))
		m.scrollToLive()
	case k.Code == 'w' && k.Mod&tea.ModCtrl != 0:
		m.closeFocusedPane()
		m.scrollToLive()
	default:
		m.scrollToLive()
		m.sendKey(m.console, k, false)
	}
	return m, nil
}

// handleExitedTabKey routes keys for a focused tab whose child has
// exited. The tab keeps showing the command's last screen plus its
// exit-status + close hint; Enter (the advertised affordance) or Ctrl+W
// closes it — the child already exited, so this is a plain dismiss, not
// a session detach. On an ended box attach, r re-attaches in place.
// Scroll and tab-nav chrome stay live (there is no child left to feed);
// every other key is inert.
func (m *model) handleExitedTabKey(k tea.Key) tea.Cmd {
	switch {
	case k.Code == tea.KeyEnter,
		k.Code == 'w' && k.Mod&tea.ModCtrl != 0:
		m.closeFocusedPane()
		m.scrollToLive()
		if m.tabs.Current() == nil {
			m.focusPalette = true // no tabs left: route input to the palette
		}
	case k.Code == 'r' && k.Mod == 0:
		m.reattachPane(m.tabs.Current(), m.tabs.Focused())
	case k.Code == tea.KeyPgUp:
		page := max(m.viewRows()-1, 1)
		m.scrollBy(page)
	case k.Code == tea.KeyPgDown:
		page := max(m.viewRows()-1, 1)
		m.scrollBy(-page)
	case k.Code == tea.KeyLeft && k.Mod&(tea.ModCtrl|tea.ModAlt) != 0:
		m.tabs.Prev()
		m.scrollToLive()
	case k.Code == tea.KeyRight && k.Mod&(tea.ModCtrl|tea.ModAlt) != 0:
		m.tabs.Next()
		m.scrollToLive()
	case k.Code >= '1' && k.Code <= '9' && k.Mod&(tea.ModAlt|tea.ModCtrl) != 0:
		m.tabs.Select(int(k.Code - '1'))
		m.scrollToLive()
	}
	return nil
}

// clearConsoleOutput drops the palette overlay's command buffer and any
// scroll offset. The overlay output is ephemeral (decided 2026-06-15):
// it's wiped when focus leaves the palette, or when other content (the
// suggestion list) takes over the overlay — never lingering behind. A
// running command is left running; only its rendered buffer is reset.
func (m *model) clearConsoleOutput() {
	m.console.Clear()
	m.scrollToLive()
	m.suggNav = false // focus left the palette: forget menu-steering
}

// clearSuggestions drops the as-you-type menu and its steering state. Used
// when history navigation takes over the arrows, so a recalled line never
// leaves a populated menu that would hijack the next Up/Down.
func (m *model) clearSuggestions() {
	m.sugg, m.suggSel, m.suggNav, m.suggNoSpace = nil, 0, false, false
}

// refreshSuggestions recomputes the overlay's as-you-type list off the
// event loop (a `yoloctl __complete` subprocess answers in a few ms
// against the on-disk registries; staleness is guarded by line
// comparison on delivery).
func (m *model) refreshSuggestions() {
	m.suggNav = false // any line edit exits menu-steering (Enter runs again)
	line, _ := m.pal.Line()
	if line == "" {
		m.sugg, m.suggSel = nil, 0
		return
	}
	// Typing replaces any shown command output with suggestions: wipe the
	// buffer so backspacing back to an empty line doesn't resurface it.
	if !m.console.Busy() && m.consoleContentRows() > 0 {
		m.clearConsoleOutput()
	}
	if m.program == nil { // model under test
		return
	}
	words, last, ok := m.pal.CompletionInput()
	if !ok {
		return
	}
	go func() {
		items, directive := yoloctlcli.Complete(context.Background(), words, last)
		matched := matchCandidates(items, last)
		m.program.Send(suggestionsMsg{
			Line:    line,
			Items:   matched,
			NoSpace: noSpaceGoverns(matched, directive),
		})
	}()
}

// showAllSuggestions reveals the full top-level menu for an empty palette
// and enters menu-steering — the special case where ↓ on a bare prompt
// lists every command (a shell's TAB-on-empty). Async like
// refreshSuggestions; the result lands with Nav set so it arrives steering.
func (m *model) showAllSuggestions() {
	if m.program == nil { // no event loop under test
		return
	}
	go func() {
		items, _ := yoloctlcli.Complete(context.Background(), nil, "") // top-level commands: always space-terminated
		m.program.Send(suggestionsMsg{Line: "", Items: items, Nav: true})
	}()
}

// matchCandidates keeps the completions whose value extends the token
// under the cursor (cobra returns flag/subcommand candidates for an empty
// token too, so the palette filters by prefix on its side).
func matchCandidates(items []yoloctlcli.Candidate, last string) []yoloctlcli.Candidate {
	matched := make([]yoloctlcli.Candidate, 0, len(items))
	for _, c := range items {
		if strings.HasPrefix(c.Value, last) {
			matched = append(matched, c)
		}
	}
	return matched
}

// completePalette resolves TAB: it inserts the selected suggestion — the
// one shown as the inline ghost — so what you see is what you get. Prefers
// the on-screen list (already filtered + selected via the arrows); falls
// back to a synchronous `yoloctl __complete` when the async list hasn't
// arrived yet (or under test, where there's no event loop). Same
// machinery as shell completion, so there's zero drift.
func (m *model) completePalette() {
	m.suggNav = false // accepting exits menu-steering
	words, last, ok := m.pal.CompletionInput()
	if !ok {
		return
	}
	// Prefer the on-screen selection (matches the ghost) while it still
	// extends the current token; otherwise recompute synchronously — the
	// async list lagged a keystroke, or there's no event loop under test.
	if len(m.sugg) > 0 {
		if v := m.sugg[m.selectedSuggestion()].Value; strings.HasPrefix(v, last) {
			m.pal.ApplyCompletion(v + completionTerminator(v, m.suggNoSpace))
			return
		}
	}
	items, directive := yoloctlcli.Complete(context.Background(), words, last)
	if matched := matchCandidates(items, last); len(matched) > 0 {
		v := matched[0].Value
		m.pal.ApplyCompletion(v + completionTerminator(v, noSpaceGoverns(matched, directive)))
	}
}

// noSpaceGoverns decides whether the NoSpace directive should apply to
// candidates NOT ending in "/". Cobra's directive is response-wide, so in a
// path list holding directories it reflects only the directories — there the
// trailing "/" is the per-candidate signal (completionTerminator) and file
// candidates keep their terminating space. Only a homogeneous no-"/" list
// carries the directive through as-is.
func noSpaceGoverns(items []yoloctlcli.Candidate, d yoloctlcli.Directive) bool {
	if !d.NoSpace() {
		return false
	}
	for _, c := range items {
		if strings.HasSuffix(c.Value, "/") {
			return false // path-style list: per-candidate via the "/" suffix
		}
	}
	return true
}

// completionTerminator returns what closes an accepted completion: nothing
// for a directory prefix (the token stays open so the next TAB completes
// deeper into the path) or when the NoSpace directive governs this value —
// else the usual space that moves the palette to the next token.
func completionTerminator(value string, noSpace bool) string {
	if noSpace || strings.HasSuffix(value, "/") {
		return ""
	}
	return " "
}

// selectedSuggestion is suggSel clamped to the current list (the async
// list can shrink between a keypress and a render).
func (m *model) selectedSuggestion() int {
	if len(m.sugg) == 0 {
		return 0
	}
	return min(max(m.suggSel, 0), len(m.sugg)-1)
}

// composePanes composes a tab with pane bars; a seam so tests can prove an
// undivided tab never takes the per-cell path.
var composePanes = panes.Compose

func (m *model) View() tea.View {
	frames := &displayFrames{console: m.console.Frame()}
	if t := m.tabs.Current(); t != nil {
		if l := m.laidOut(t); l.Bars() || l.Dropping() {
			// One snapshot per visible pane, composed with the pane bars
			// (zoomed too) or a dock target; the focused one doubles as the
			// tab frame for scroll and cursor.
			frames.panes = make(map[*panes.Pane]terminal.Frame)
			for _, p := range t.Layout.Visible() {
				if !p.Empty() { // an empty pane draws its hint, not a frame
					frames.panes[p] = p.Session.Frame()
				}
			}
			frames.tab = frames.panes[t.Focused()]
		} else if p := t.Focused(); !p.Empty() {
			frames.tab = p.Session.Frame()
		}
	}
	m.frames = frames
	defer func() { m.frames = nil }()
	m.scrollOff = frames.tab.Offset
	if m.focusPalette {
		m.scrollOff = frames.console.Offset
	}
	rows := m.viewRows()

	// Base layer: the active tab (or the welcome hint).
	var base []string
	if cur := m.tabs.Current(); cur != nil && frames.panes != nil {
		rows := make(map[*panes.Pane][]string, len(frames.panes))
		for p, f := range frames.panes {
			rows[p] = selectionLines(f)
		}
		if e := cur.Layout.Empty(); e != nil {
			rows[e] = m.placeholderRows()
		}
		base = composePanes(cur.Layout, rows)
	} else if cur != nil {
		// An undivided tab keeps the single-frame path unchanged.
		base = selectionLines(frames.tab)
	} else {
		base = []string{"",
			cDim + "  type a yoloctl command and press Enter" + cReset,
			cDim + "  e.g.: box create coder --agent claude · box attach coder · space ls" + cReset,
		}
	}
	for len(base) < rows {
		base = append(base, "")
	}
	base = base[:rows]
	for i, line := range base {
		base[i] = ansi.Truncate(line, max(m.width, 1), "")
	}
	// An ended box view's re-attach button, then the pane menu or inert
	// notice, over whichever path drew the base.
	m.applyReattachButtons(base)
	m.applyOverlay(base)

	// Compose top-to-bottom. With the palette focused the overlay
	// opens under row 0 and PUSHES the tab strip + tab content down
	// (the tab keeps its full PTY; its bottom rows are just clipped).
	lines := []string{m.inputLine()}
	if m.focusPalette {
		overlayRows := m.overlayContentRows()
		lines = append(lines, m.overlayContent(overlayRows)...)
		lines = append(lines, m.tabStrip())
		lines = append(lines, base[:max(rows-overlayRows, 0)]...)
	} else {
		lines = append(lines, m.tabStrip())
		lines = append(lines, base...)
	}

	if m.standalone {
		lines = base
		if m.status != "" && len(lines) > 0 {
			lines[len(lines)-1] = cOrange + ansi.Truncate(ansi.Strip(m.status), max(m.width, 1), "") + cReset
		}
		if prompt := m.browserPopup(len(lines)); len(prompt) > 0 {
			copy(lines[len(lines)-len(prompt):], prompt)
		}
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.ReportFocus = true
	v.KeyboardEnhancements = tea.KeyboardEnhancements{ReportEventTypes: true, ReportAlternateKeys: true, ReportAllKeysAsEscapeCodes: true, ReportAssociatedText: true}
	// Host tracking and encoding are serialized together in host_mouse_encoding.
	v.MouseMode = tea.MouseModeNone
	v.WindowTitle = "yoloctl"
	if t := m.tabs.Current(); t != nil {
		v.WindowTitle = windowTitle(t.Title())
	}
	v.Cursor = m.hostCursor()
	return v
}

// titleWrapper is the generated window-title prefix; a nested yoloctl tab
// reports a title that already carries it.
const titleWrapper = "yoloctl — "

// windowTitle wraps a tab title once. Only exact leading generated wrappers
// collapse: interior text, case variants and other dashes stay. An empty or
// app-name-only remainder (including an older nested build's empty wrapper,
// which Tab.Title trims) yields the bare name. The stored title is unchanged.
func windowTitle(title string) string {
	for strings.HasPrefix(title, titleWrapper) {
		title = title[len(titleWrapper):]
	}
	switch title {
	case "", "yoloctl", strings.TrimSpace(titleWrapper):
		return "yoloctl"
	}
	return titleWrapper + title
}

// overlayContent renders the palette overlay: the suggestion menu when
// one is up (including the ↓-on-empty reveal), the command buffer
// otherwise. The height reserves one extra row (desiredOverlayContent), so
// the padding below leaves a trailing blank row — the gap before the tab
// strip.
func (m *model) overlayContent(rows int) []string {
	if rows <= 0 {
		return nil
	}
	bg := m.surfaceBg()
	var content []string
	if prompt := m.modalPopup(1 + rows); len(prompt) > 0 {
		content = prompt[1:]
	} else if len(m.sugg) > 0 {
		content = m.renderSuggestions(rows, bg)
	} else {
		grid := m.consoleFrame().Lines
		grid = grid[max(len(grid)-rows, 0):]
		// Native rows may omit unoccupied trailing cells. Paint those cells
		// explicitly so the overlay surface fills the whole viewport width.
		for _, g := range grid {
			g += strings.Repeat(" ", max(m.width-ansi.StringWidth(g), 0))
			content = append(content, paintRow(g, bg))
		}
	}
	for len(content) < rows {
		content = append(content, m.overlayLine("", "", bg))
	}
	return content[:rows]
}

// overlayLine builds one full-width overlay row from PLAIN text: pad to
// the view width so the surface fills edge to edge, apply the style,
// then lay it on the surface bg.
func (m *model) overlayLine(style, text, bg string) string {
	if pad := m.width - runewidth.StringWidth(text); pad > 0 {
		text += strings.Repeat(" ", pad)
	}
	return paintRow(style+text, bg)
}

// renderSuggestions paints the as-you-type completion menu: one row per
// candidate (value + cobra's dim description), the selected row marked and
// accented — it's the ghost / TAB target. When the list is taller than
// the window it scrolls to keep the selection centred; a short list is
// padded by the caller, preserving the trailing blank before the tab
// strip.
func (m *model) renderSuggestions(rows int, bg string) []string {
	n := len(m.sugg)
	if n == 0 || rows <= 0 {
		return nil
	}
	sel := m.selectedSuggestion()

	// Value column: as wide as the widest value, capped so descriptions
	// always keep room (long values truncate with an ellipsis).
	valW := 0
	for _, c := range m.sugg {
		valW = max(valW, runewidth.StringWidth(c.Value))
	}
	valW = min(valW, max(m.width/2, 12))

	// Scroll window: centre the selection, clamped to the list ends.
	start := 0
	if n > rows {
		start = min(max(sel-rows/2, 0), n-rows)
	}
	end := min(start+rows, n)

	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, m.suggestionRow(m.sugg[i], i == sel, valW, bg))
	}
	return out
}

// suggestionRow renders one menu entry: a 2-column marker (accented "▸"
// when selected), the value padded to the shared column width, then the
// dim description truncated to the remaining space. Built with inline SGR
// and padded by visible width (overlayLine can't — it measures raw text).
func (m *model) suggestionRow(c yoloctlcli.Candidate, selected bool, valW int, bg string) string {
	marker, valStyle := "  ", cBlue
	if selected {
		marker, valStyle = "▸", cOrange+cBold
		if w := runewidth.StringWidth(marker); w < 2 { // keep the column at 2 cols
			marker += strings.Repeat(" ", 2-w)
		}
	}
	val := c.Value
	if runewidth.StringWidth(val) > valW {
		val = runewidth.Truncate(val, valW, "…")
	}
	valPad := strings.Repeat(" ", max(valW-runewidth.StringWidth(val), 0))

	const gap = "  "
	visW := 2 + valW + len(gap) // marker + value column + gap
	desc := ""
	if c.Desc != "" {
		if avail := m.width - visW; avail > 1 {
			d := runewidth.Truncate(c.Desc, avail, "…")
			desc, visW = cDim+d+cReset, visW+runewidth.StringWidth(d)
		}
	}
	styled := valStyle + marker + val + valPad + cReset + gap + desc
	if pad := m.width - visW; pad > 0 {
		styled += strings.Repeat(" ", pad)
	}
	return paintRow(styled, bg)
}

// hostCursor positions the terminal's real cursor: at the running
// command's cursor cell in the overlay while one is being fed input, at
// the palette's edit point while the palette has focus, at the child's
// cursor cell while a tab has focus, hidden when scrolled back.
func (m *model) hostCursor() *tea.Cursor {
	if m.browserDialog != nil || m.confirm != nil || m.picker != nil || m.activeOverlay() != nil {
		return nil
	}
	if m.focusPalette {
		if m.console.Busy() {
			return m.runningCursor()
		}
		line, cur := m.pal.Line()
		// Map the cursor through the same window inputLine renders, so a long
		// line's cursor tracks the visible (rightmost) slice instead of
		// running off-screen. Rune count for the brand: ❯ is 3 bytes, 1 col.
		_, col, _, _ := visibleLine(line, cur, m.paletteRegion())
		return tea.NewCursor(len([]rune(brandPrefix()))+col, 0)
	}
	if t := m.tabs.Current(); t != nil {
		m.laidOut(t)
	}
	if p := m.tabs.Focused(); p != nil && !p.Exited && !p.Empty() {
		// An exited pane has no input to take — the close hint is its only
		// affordance, so don't park a blinking cursor in a dead pane; an
		// empty pane has none either. Only the focused pane has a cursor:
		// other panes show none.
		c, r := m.tabFrame().Cursor, p.Rect()
		if c.Visible && c.Y < r.H && c.X < r.W && r.Y+c.Y < m.viewRows() && r.X+c.X < m.width {
			cursor := frameCursor(c, r.Y+c.Y+m.chromeRows())
			cursor.X += r.X
			return cursor
		}
	}
	return nil
}

// runningCursor maps a running command's cursor into the overlay, so its
// prompts read naturally. The overlay (output mode) shows the last
// overlayContentRows lines of the content (scrollback + grid) starting at
// view row 1; the cursor sits at content line len(scrollback)+cy. Hidden
// when scrolled back or when the cell falls outside the visible window.
func (m *model) runningCursor() *tea.Cursor {
	f := m.consoleFrame()
	if !f.Cursor.Visible {
		return nil
	}
	rows := m.overlayContentRows()
	if disp := f.Cursor.Y - max(len(f.Lines)-rows, 0); disp >= 0 && disp < rows {
		return frameCursor(f.Cursor, 1+disp) // +1: overlay starts under the input row
	}
	return nil
}

// brandPrefix is row 0's left chrome before the editable line (plain
// text; the styled variant is built in barLines).
func brandPrefix() string { return "❯ yoloctl " }

// surfaceBg is the SGR that sets the chrome "surface" background, or ""
// when disabled. It resolves the tui.yaml palette_bg spec:
//
//	"" / "auto"  -> derive from the detected terminal background
//	"off"/"none" -> "" (no surface)
//	0..255       -> 256-color index
//	"#rrggbb"    -> truecolor
func (m *model) surfaceBg() string {
	spec := m.cfg.GetPaletteBg()
	switch strings.ToLower(spec) {
	case "off", "none", "disabled":
		return ""
	case "", "auto":
		return m.autoSurfaceBg()
	default:
		if sgr, ok := bgSGR(spec); ok {
			return sgr
		}
		return m.autoSurfaceBg() // unparseable -> auto
	}
}

// autoSurfaceBg derives a raised surface from the terminal background:
// nudge the detected color's channels toward the opposite end — ~15% of
// the 0-255 range, lighter on dark themes, darker on light — and emit it
// as truecolor. Before the OSC 11 reply (or if the terminal never
// answers), assume a near-black surface.
func (m *model) autoSurfaceBg() string {
	if !m.bgDetected {
		return "\x1b[48;5;235m"
	}
	const delta = 38 // ~15% of the 0-255 channel range
	nudge := func(c uint8) uint8 {
		if m.bgIsDark {
			return uint8(min(int(c)+delta, 255))
		}
		return uint8(max(int(c)-delta, 0))
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", nudge(m.bgR), nudge(m.bgG), nudge(m.bgB))
}

// bgSGR parses an explicit palette_bg spec into a background SGR: a
// 0..255 256-color index or a #rrggbb truecolor value.
func bgSGR(spec string) (string, bool) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "#") && len(spec) == 7 {
		var r, g, b int
		if n, err := fmt.Sscanf(spec, "#%02x%02x%02x", &r, &g, &b); n == 3 && err == nil {
			return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b), true
		}
		return "", false
	}
	if n, err := strconv.Atoi(spec); err == nil && n >= 0 && n <= 255 {
		return fmt.Sprintf("\x1b[48;5;%dm", n), true
	}
	return "", false
}

// paintRow lays a fully-built styled row on the surface bg. A bare
// cReset (ours, or — in overlay grid lines — the core's per-region
// reset) clears the background, so re-assert it after every interior
// reset, then add one final reset so the surface doesn't bleed past the
// row. The re-assert is also what paints the surface behind the core's
// blank cells. No-op when bg is "".
func paintRow(row, bg string) string {
	if bg == "" {
		return row
	}
	return bg + strings.ReplaceAll(row, cReset, cReset+bg) + cReset
}

// contextChip is the always-shown current context for the status bar:
// the current space in parentheses. Empty context has no chip.
func (m *model) contextChip() string {
	if m.ctxSpace == "" {
		return ""
	}
	return runewidth.Truncate("("+m.ctxSpace+")", max(m.width-len([]rune(brandPrefix()))-3, 0), "…")
}

// paletteRegion is the display width the editable line gets on row 0: the
// terminal minus the brand prefix, the always-shown space chip, and the
// trailing slack the layout needs (a 1-col gap before the chip plus the
// final surface-paint column). The line is windowed into it (visibleLine)
// so a long command slides instead of wrapping the row.
func (m *model) paletteRegion() int {
	region := m.width - len([]rune(brandPrefix())) - runewidth.StringWidth(m.contextChip()) - 2
	if region < 1 {
		region = 1
	}
	return region
}

// visibleLine windows a single-line buffer into avail display columns so
// the editing cursor stays visible. It prefers to reveal text to the
// cursor's left — the rightmost slice of a long line, the typing-at-end
// case — and reports whether text is hidden off each edge (for the ‹ ›
// overflow markers). Measuring is by rune: the palette is ASCII-dominant
// (commands, paths, flags) and the cursor from palette.Model is a rune
// index, so rune math keeps render and cursor in lockstep. The cursor is
// kept within [0, avail-1]; at end-of-line it sits on the trailing cell
// (one fewer rune shown, which is what surfaces the ‹ marker).
func visibleLine(line string, cursor, avail int) (text string, col int, hidLeft, hidRight bool) {
	r := []rune(line)
	n := len(r)
	if avail < 1 {
		avail = 1
	}
	if cursor < 0 {
		cursor = 0
	} else if cursor > n {
		cursor = n
	}
	off := 0
	if cursor > avail-1 { // cursor would fall at/past the edge: slide left
		off = cursor - (avail - 1)
	}
	end := min(off+avail, n)
	return string(r[off:end]), cursor - off, off > 0, end < n
}

// stampEdges overlays the ‹ › overflow markers onto the windowed line's
// edge cells (a scroll shadow over the off-screen-adjacent rune). The
// cursor cell always wins — a marker on the cursor's own column is skipped
// that frame — so the cursor is never hidden under a glyph.
func stampEdges(text string, col int, hidLeft, hidRight bool) string {
	r := []rune(text)
	if len(r) == 0 {
		return text
	}
	if hidLeft && col != 0 {
		r[0] = '‹'
	}
	if hidRight && col != len(r)-1 {
		r[len(r)-1] = '›'
	}
	return string(r)
}

// inputLine renders row 0: brand + editable line on the left; on the right
// the state legend/status followed by the bold space chip. The right
// side is budgeted chip-first (the chip never yields), then the inline
// ghost, then the legend (truncated or dropped under width pressure).
// newTabPlaceholder fills the empty input while prefix c waits for a command.
const newTabPlaceholder = "new tab: type a command · Esc cancels"

func (m *model) inputLine() string {
	if prompt := m.modalPopup(1 + m.overlayMaxContentRows()); len(prompt) > 0 {
		return prompt[0]
	}
	line, cursor := m.pal.Line()
	brandW := len([]rune(brandPrefix()))

	// Window the editable line into its region so a long command slides to
	// keep the cursor visible (the rightmost slice) instead of wrapping the
	// row and pushing the cursor off-screen; ‹ › mark text hidden off each
	// edge (‹ is the common case — typing at the end). A short line returns
	// whole and markerless, so the layout below is byte-identical to an
	// unwindowed render.
	disp, curCol, hidL, hidR := visibleLine(line, cursor, m.paletteRegion())
	windowed := hidL || hidR
	disp = stampEdges(disp, curCol, hidL, hidR)
	lineW := runewidth.StringWidth(disp)

	prefix := m.panePrefix().String()
	legend, legendStyle := "^Y palette · "+prefix+" panes", cDim
	newTab := m.newTab != nil && m.focusPalette && !m.console.Busy()
	switch {
	case newTab && line != "":
		legend = "new tab · Esc cancels" // the placeholder went with the first character
	case m.status != "":
		legend, legendStyle = m.status, cOrange
	case m.focusPalette:
		legend = "Esc hide palette · " + prefix + " panes · ^←/→ tabs · ^D quit"
		switch {
		case m.console.Busy(): // keys feed the running command, not the editor
			legend = "running · ^C interrupt · ^Y detach · ^←/→ tabs"
		case m.suggNav && len(m.sugg) > 0: // steering the menu: Enter picks
			legend = "↑↓ move · Enter/TAB pick · Esc hide"
		}
		if m.scrollOff > 0 {
			legend = fmt.Sprintf("scroll -%d · %s", m.scrollOff, legend)
		}
	}
	if windowed {
		// A long, windowed line keeps only the always-shown chip on the
		// right: there's no room for the legend, and the cursor's empty
		// end-of-line cell would otherwise free just enough width for a
		// 1–2 col legend stub that overruns the row.
		legend = ""
	}

	chip := m.contextChip()
	chipW := runewidth.StringWidth(chip)
	budget := max(m.width-brandW-lineW-2, 0) // leave a gap and the final painted column

	// Inline ghost (fish-style): foreground, but it yields to the
	// always-shown chip — cap it so line + ghost + chip still fits.
	// Suppressed once the line is windowed: it would render past the region,
	// and a line that long has no room for it anyway.
	ghost := ""
	switch {
	case newTab && line == "":
		ghost = newTabPlaceholder // prefix c: a dim placeholder inside the empty input
	case !windowed:
		ghost = m.ghostSuffix()
	}
	if ghost != "" {
		switch maxGhost := budget - chipW; {
		case maxGhost < 1:
			ghost = ""
		case runewidth.StringWidth(ghost) > maxGhost:
			ghost = runewidth.Truncate(ghost, maxGhost, "")
		}
	}
	ghostW := runewidth.StringWidth(ghost)

	// Legend takes what's left after the chip and ghost; truncate, or drop
	// it (and its separator) when too cramped. The separator is a dim dot
	// (never the chip's highlight), matching the · separators used elsewhere.
	sep := ""
	if chip != "" && legend != "" {
		sep = " · "
	}
	sepW := runewidth.StringWidth(sep)
	if avail := budget - chipW - ghostW - sepW; legend != "" {
		switch {
		case avail < 1:
			legend, sep, sepW = "", "", 0
		case runewidth.StringWidth(legend) > avail:
			legend = runewidth.Truncate(legend, avail, "…")
		}
	}

	right := ""
	if legend != "" {
		right = legendStyle + legend + cReset
	}
	if chip != "" {
		if sep != "" {
			right += cDim + sep + cReset
		}
		right += cBlue + cBold + chip + cReset
	}
	rightW := chipW + sepW + runewidth.StringWidth(legend)

	styledGhost := ""
	if ghost != "" {
		styledGhost = cDim + ghost + cReset
	}
	leftW := brandW + lineW + ghostW
	// pad right-aligns `right`; the trailing space then takes the last
	// column so the surface bg paints the full width. A space there
	// (never a legend glyph) avoids last-cell autowrap; without it the
	// last column keeps the terminal's own bg — a stray dark cell.
	pad := max(m.width-leftW-rightW-1, 1)
	brand := "yoloctl"
	if m.console.Busy() {
		brand = spinnerFrames[m.spinnerFrame]
	}
	row := cOrange + "❯ " + cBold + brand + cReset + " " + disp + styledGhost +
		strings.Repeat(" ", pad) + right + " "
	return paintRow(row, m.surfaceBg())
}

// ghostSuffix is the inline autosuggestion: the part of the selected
// suggestion that extends the token under the cursor. Shown only with
// palette focus, no running command, the cursor at end of line, and a
// non-empty token the selection strictly extends — so it always matches
// what TAB would insert. "" otherwise (including the just-typed-a-space
// case, where the menu lists options but there's nothing to ghost yet).
func (m *model) ghostSuffix() string {
	if !m.focusPalette || m.console.Busy() || len(m.sugg) == 0 {
		return ""
	}
	_, last, ok := m.pal.CompletionInput()
	if !ok || last == "" {
		return ""
	}
	v := m.sugg[m.selectedSuggestion()].Value
	if len(v) > len(last) && strings.HasPrefix(v, last) {
		return v[len(last):]
	}
	return ""
}

// tabStrip renders the tab row: a 1-col margin opens the strip, then
// each tab carries 1 col of padding per side (tabs are separated only by
// that adjoining padding — no extra joint). Two reinforcing cues mark
// the active tab as open into the view below: the full-width underline
// separation line is broken at it, and the chrome surface bg (carried by
// every other segment) is absent on it and its own padding — so the
// active tab shares the view's background and reads as connected to it.
// All measuring follows the host renderer's width mode — child-set titles
// carry wide and ambiguous glyphs that byte/rune math gets wrong. (bg
// is "" when the surface is disabled, making those prefixes no-ops.)
//
// When the tabs overrun the width, the strip windows to a contiguous run
// that includes the active tab — the active tab stays visible with one
// tab of look-ahead to its right when one exists (fill-left-then-right),
// so it never pins to the extreme edge — and ‹ / › mark hidden tabs off
// each side. The circled-number labels keep their absolute value (Ctrl+digit
// selects by it), so windowing never renumbers a tab.
// circledNumber renders a 1-based tab index as a single negative-circled
// glyph — ❶–❿ (U+2776–277F) for 1–10, ⓫–⓴ (U+24EB–24F4) for 11–20, which
// measure as 1 column. Past 20 there is no matching glyph, so it
// falls back to the plain decimal (Ctrl/Alt+digit only addresses 1–9 anyway).
func circledNumber(n int) string {
	switch {
	case n >= 1 && n <= 10:
		return string(rune(0x2775 + n)) // ❶ (U+2776) at n==1
	case n >= 11 && n <= 20:
		return string(rune(0x24E0 + n)) // ⓫ (U+24EB) at n==11
	default:
		return strconv.Itoa(n)
	}
}

// visibleTab is one rendered tab-strip segment: the absolute tab index
// (windowing never renumbers), the label AFTER both truncations (the
// active/inactive titleMax cap and the single-tab oversize clamp), and the
// rendered width in display columns — the label plus its 1 col of padding on
// each side.
type visibleTab struct {
	index int
	label string
	width int
}

// tabStripMargin is the leading strip margin (always present).
const tabStripMargin = 1

// tabCloseW is the close button every label ends with: a space and ✕. It
// is drawn after the (possibly truncated) label, so a clamped title keeps
// its number and its ✕.
const tabCloseW = 2

// tabSegments windows pre-composed labels into the strip: the visible
// segments in display order, whether tabs are hidden off each side, and
// firstX — the column of the first segment's left edge (after the margin and
// any ‹). Pure and escape-free (the styling lives in the renderer): it does
// the per-tab width, the [lo,hi] window around the active tab, and the
// single-tab oversize clamp, exactly as tabStrip used to inline. Shared by
// tabStrip (render) and tabAt (hit-test) so the two can never disagree.
func tabSegments(labels []string, active, width int, method ansi.Method) (segs []visibleTab, hidLeft, hidRight bool, firstX int) {
	n := len(labels)
	if n == 0 {
		return nil, false, false, tabStripMargin
	}
	active = min(max(active, 0), n-1)
	segW := make([]int, n) // each tab's rendered width: 1 col padding per side
	total := 0
	for i := range segW {
		segW[i] = 2 + method.StringWidth(labels[i]) + tabCloseW
		total += segW[i]
	}
	avail := max(width-tabStripMargin, 1)
	// Reserve up to two arrow columns when overflowing. Over-reserving by
	// one (only one side actually hidden) just widens the trailing pad.
	budget := avail
	if total > avail {
		budget = max(avail-2, 1)
	}

	// Window [lo,hi] (inclusive) around the active tab: seed with active,
	// add one right look-ahead, then fill left, then fill right — all bounded
	// by the budget.
	lo, hi := active, active
	used := min(segW[active], budget)
	if hi+1 < n && used+segW[hi+1] <= budget {
		hi++
		used += segW[hi]
	}
	for lo-1 >= 0 && used+segW[lo-1] <= budget {
		lo--
		used += segW[lo]
	}
	for hi+1 < n && used+segW[hi+1] <= budget {
		hi++
		used += segW[hi]
	}
	hidLeft, hidRight = lo > 0, hi < n-1

	firstX = tabStripMargin
	if hidLeft {
		firstX++
	}
	for i := lo; i <= hi; i++ {
		lbl := labels[i]
		if 2+method.StringWidth(lbl)+tabCloseW > budget { // a single oversized tab: clamp to fit
			lbl = truncateTabText(lbl, max(budget-2-tabCloseW, 1), method)
		}
		segs = append(segs, visibleTab{index: i, label: lbl, width: 2 + method.StringWidth(lbl) + tabCloseW})
	}
	return segs, hidLeft, hidRight, firstX
}

// tabStripLabels composes the per-tab labels exactly as the strip renders
// them: the circled absolute number, the title truncated to the active or
// inactive cap (display cols; tui.yaml tab_title_max_active /
// tab_title_max_inactive, defaults 40/30 — the focused tab's title reads
// further while the rest stay compact), and a ` •` attention badge.
// Empty when there are no tabs.
func (m *model) tabStripLabels() []string {
	n := len(m.tabs.Tabs)
	if n == 0 {
		return nil
	}
	active := min(max(m.tabs.Active, 0), n-1)
	icons := m.cfg.GetAgentIcons()
	method := m.tabWidthMethod()
	labels := make([]string, n)
	for i := range labels {
		titleMax := m.cfg.GetTabTitleMaxInactive()
		if i == active {
			titleMax = m.cfg.GetTabTitleMaxActive()
		}
		title := tabIconTitle(strings.TrimPrefix(m.tabs.Tabs[i].Title(), titleWrapper), icons, method)
		l := circledNumber(i+1) + " " + truncateTabText(title, titleMax, method)
		if m.tabs.Tabs[i].Badge {
			l += " •"
		}
		labels[i] = l
	}
	return labels
}

// tabStripLayout is the model-bound geometry: compose labels, then window
// them at the current width. Returns no segments when there are no tabs.
func (m *model) tabStripLayout() (segs []visibleTab, hidLeft, hidRight bool, firstX int) {
	labels := m.tabStripLabels()
	if len(labels) == 0 {
		return nil, false, false, tabStripMargin
	}
	return tabSegments(labels, m.tabs.Active, m.width, m.tabWidthMethod())
}

func (m *model) tabStrip() string {
	bg := m.surfaceBg()
	segs, hidLeft, hidRight, _ := m.tabStripLayout()
	if len(segs) == 0 {
		var b strings.Builder
		b.WriteString(bg + cDim + cUnderline + " (no tabs)" + cReset)
		visible := len(" (no tabs)")
		if pad := max(m.width, 1) - visible; pad > 0 {
			b.WriteString(bg + cBlue + cUnderline + strings.Repeat(" ", pad) + cReset)
		}
		return b.String()
	}
	active := min(max(m.tabs.Active, 0), len(m.tabs.Tabs)-1)

	var b strings.Builder
	visible := tabStripMargin
	b.WriteString(bg + cBlue + cUnderline + " " + cReset) // leading margin
	if hidLeft {
		b.WriteString(bg + cBlue + cUnderline + "‹" + cReset)
		visible++
	}
	icons := m.cfg.GetAgentIcons()
	dragged := -1 // a dock drag's tab shows its label in reverse video
	if g := m.chrome; g != nil && g.kind == chromeDock {
		dragged = m.tabs.Index(g.tab)
	}
	for _, s := range segs {
		// 1 col of padding per side. The active tab drops the surface bg +
		// underline (it reads as open into the view below — the break in
		// the separation line); inactive tabs keep both, so the underline
		// runs continuously across them and their adjoining padding.
		style := bg + cBlue + cBold + cUnderline
		label := s.label
		// The close button: dim on an inactive tab (its underline stays the
		// strip's blue), orange on the active one.
		closeBtn := cReset + bg + cDim + cUnderline + "\x1b[58;5;67m" + "✕" + cReset + style
		if s.index == active {
			style = cOrange + cBold
			label = m.tabIconLabel(label, style, icons)
			closeBtn = "✕"
		}
		if s.index == dragged {
			style += "\x1b[7m"
			closeBtn = "✕"
		}
		b.WriteString(style + " " + label + " " + closeBtn + " " + cReset)
		visible += s.width
	}
	if hidRight {
		b.WriteString(bg + cBlue + cUnderline + "›" + cReset)
		visible++
	}
	// Underlined surface padding completes the separation line to the edge.
	if pad := max(m.width, 1) - visible; pad > 0 {
		b.WriteString(bg + cBlue + cUnderline + strings.Repeat(" ", pad) + cReset)
	}
	return b.String()
}

// tabCloseAt reports the tab whose close button is at a strip column, or
// -1: the ✕ is a segment's second-to-last column.
func (m *model) tabCloseAt(x int) int {
	segs, _, _, firstX := m.tabStripLayout()
	col := firstX
	for _, s := range segs {
		if x == col+s.width-2 {
			return s.index
		}
		col += s.width
	}
	return -1
}

// tabAt maps a tab-strip column to an absolute tab index, or -1 when the
// column lands on the leading margin, an arrow, the trailing pad, or there
// are no tabs. Walks display columns from firstX over the same segments
// tabStrip renders (the 1-col side padding is part of a tab's clickable
// span), so a click resolves to exactly the tab under the cursor.
func (m *model) tabAt(x int) int {
	segs, _, _, firstX := m.tabStripLayout()
	col := firstX
	for _, s := range segs {
		if x >= col && x < col+s.width {
			return s.index
		}
		col += s.width
	}
	return -1
}
