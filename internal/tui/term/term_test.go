package term

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/termio"
)

// Emulator-level fidelity tests: headless versions of the terminal validation
// battery's load-bearing checks, pinned here for CI. Session integration
// tests allocate a local PTY; pure emulator tests do not require one.

func TestOSC52InterceptedBeforeCore(t *testing.T) {
	e := newTestEmulator(t, 4, 40, nil, 0)
	var events []termio.OSC52
	e.SetOSC52Handler(func(event termio.OSC52) {
		events = append(events, event)
	})
	for _, chunk := range []string{"before\x1b]52;c;Y2", "9waWVk\x1b\\after"} {
		if _, err := e.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.GridText(); !strings.Contains(got, "beforeafter") {
		t.Fatalf("grid = %q, want ordinary output around OSC 52", got)
	}
	if len(events) != 1 || events[0].Selection != "c" || events[0].Payload != "Y29waWVk" {
		t.Fatalf("events = %#v", events)
	}

	// A non-clipboard OSC still belongs to the terminal core.
	if _, err := e.Write([]byte("\x1b]2;agent title\a")); err != nil {
		t.Fatal(err)
	}
	if got := e.Title(); got != "agent title" {
		t.Fatalf("title = %q", got)
	}
}

func TestOSC52InterceptorDoesNotHoldUTF8Prompt(t *testing.T) {
	e := newTestEmulator(t, 4, 80, nil, 0)
	e.SetOSC52Handler(func(termio.OSC52) {})

	// Write each byte separately to reproduce a PTY read boundary inside ❯
	// (E2 9D AF). Its 9D continuation byte is not a raw C1 OSC introducer.
	const prompt = "/workdir ❯ yoloctl box recreate claude-personal"
	for _, b := range []byte(prompt) {
		if _, err := e.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.GridText(); !strings.Contains(got, prompt) {
		t.Fatalf("grid = %q, want prompt rendered before filter Flush", got)
	}
}

func TestSessionRoutesOSC52ToHostBoundary(t *testing.T) {
	events := make(chan termio.OSC52, 1)
	s, err := Start(Options{
		Argv: []string{"sh", "-c", "printf '\\033]52;c;Y29waWVk\\007'"},
		Rows: 4, Cols: 40,
		OnOSC52: func(event termio.OSC52) { events <- event },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Selection != "c" || event.Payload != "Y29waWVk" {
			t.Fatalf("event = %#v", event)
		}
	default:
		t.Fatal("session did not route OSC 52")
	}
}

func TestQueryAnswers(t *testing.T) {
	cases := []struct {
		name, query, want string
	}{
		{"DA1", "\x1b[c", "\x1b[?62;22c"},
		{"DSR", "\x1b[5n", "\x1b[0n"},
		{"CPR", "\x1b[6n", "\x1b[1;1R"},
		// This is the reported current flags, not a refusal of Kitty support.
		// Negotiation behavior is checked separately by the fidelity corpus.
		{"kitty-current-flags", "\x1b[?u", "\x1b[?0u"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp bytes.Buffer
			e := newTestEmulator(t, 24, 80, &resp, 0)
			if _, err := e.Write([]byte(tc.query)); err != nil {
				t.Fatalf("write query: %v", err)
			}
			if got := resp.String(); got != tc.want {
				t.Errorf("answer = %q, want %q (byte-exact contract)", got, tc.want)
			}
		})
	}
}

func TestKeyboardProtocolSeqs(t *testing.T) {
	seqs, tail := keyboardProtocolSeqs([]byte("hello\x1b[>1uignored\x1b[?u\x1b[6n"))
	if len(tail) != 0 {
		t.Fatalf("tail = %q, want empty", tail)
	}
	if got, want := strings.Join(seqs, ","), "\"\\x1b[>1u\",\"\\x1b[?u\""; got != want {
		t.Fatalf("seqs = %s, want %s", got, want)
	}

	seqs, tail = keyboardProtocolSeqs([]byte("\x1b[=8"))
	if len(seqs) != 0 {
		t.Fatalf("partial seqs = %v, want none", seqs)
	}
	seqs, tail = keyboardProtocolSeqs(append(tail, []byte(";1u")...))
	if got, want := strings.Join(seqs, ","), "\"\\x1b[=8;1u\""; got != want {
		t.Fatalf("split seqs = %s, want %s", got, want)
	}
	if len(tail) != 0 {
		t.Fatalf("split tail = %q, want empty", tail)
	}
}

func TestModeSetConsumedAndExposed(t *testing.T) {
	e := newTestEmulator(t, 24, 80, nil, 0)
	// Bracketed paste on + text: the DECSET must not leak into the
	// grid, and the core must expose its mode change.
	if _, err := e.Write([]byte("\x1b[?2004hREADY")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if grid := e.GridText(); !strings.Contains(grid, "READY") || strings.Contains(grid, "2004") {
		t.Errorf("grid should contain READY and no DECSET residue:\n%s", grid)
	}
	if !e.BracketedPaste() {
		t.Error("DECSET 2004 did not change parsed input state")
	}
}

func TestScrollbackEvictionAndCap(t *testing.T) {
	e := newTestEmulator(t, 4, 20, nil, 10)
	for i := range 30 {
		if _, err := fmt.Fprintf(e, "line-%02d\r\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	sb := e.Scrollback()
	if len(sb) != 10 {
		t.Fatalf("scrollback len = %d, want cap 10", len(sb))
	}
	// Oldest retained line is the newest 10 evictions back; eviction is
	// in emission order.
	if !strings.HasPrefix(sb[0], "line-1") || !strings.HasPrefix(sb[9], strings.TrimSpace(sb[9])) {
		t.Errorf("unexpected scrollback window, first = %q last = %q", sb[0], sb[9])
	}
	for i := 1; i < len(sb); i++ {
		if sb[i] <= sb[i-1] {
			t.Errorf("scrollback out of order at %d: %q !> %q", i, sb[i], sb[i-1])
		}
	}
}

// Codex renders its normal conversation in an inline viewport. Finalized
// transcript rows are inserted into native terminal history by scrolling a
// top-anchored region that ends immediately above the live composer. A real
// terminal records the row evicted from the top even though the region does
// not cover the full screen.
func TestTopAnchoredPartialScrollProducesScrollback(t *testing.T) {
	e := newTestEmulator(t, 6, 30, nil, 0)
	stream := "" +
		"\x1b[1;1Hcodex-oldest" +
		"\x1b[2;1Hcodex-second" +
		"\x1b[3;1Hcodex-third" +
		"\x1b[4;1Hcodex-fourth" +
		"\x1b[1;4r\x1b[4;1H\r\ncodex-new\r\ncodex-newer\x1b[r"
	if _, err := e.Write([]byte(stream)); err != nil {
		t.Fatalf("write Codex-style history insertion: %v", err)
	}

	sb := e.Scrollback()
	if len(sb) != 2 {
		t.Fatalf("scrollback len = %d, want 2; content = %q", len(sb), sb)
	}
	for i, want := range []string{"codex-oldest", "codex-second"} {
		if got := strings.TrimSpace(sb[i]); got != want {
			t.Errorf("evicted history row %d = %q, want %q", i, got, want)
		}
	}
	if grid := e.GridText(); !strings.Contains(grid, "codex-newer") {
		t.Errorf("inserted history row missing from grid:\n%s", grid)
	}
}

func TestPartialScrollAwayFromTopDoesNotProduceScrollback(t *testing.T) {
	e := newTestEmulator(t, 6, 30, nil, 0)
	stream := "" +
		"\x1b[1;1Hscreen-header" +
		"\x1b[2;1Hregion-oldest" +
		"\x1b[2;4r\x1b[4;1H\r\nregion-new\x1b[r"
	if _, err := e.Write([]byte(stream)); err != nil {
		t.Fatalf("write partial-region scroll: %v", err)
	}
	if sb := e.Scrollback(); len(sb) != 0 {
		t.Errorf("non-top-anchored region polluted scrollback: %q", sb)
	}
}

func TestAltScreenPausesScrollbackTracking(t *testing.T) {
	e := newTestEmulator(t, 4, 20, nil, 0)
	if _, err := e.Write([]byte("\x1b[?1049h")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !e.IsAlt() {
		t.Fatal("IsAlt = false after 1049h")
	}
	if _, err := e.Write([]byte(
		"\x1b[1;1Halt-oldest\x1b[2;1Halt-second" +
			"\x1b[1;2r\x1b[2;1H\r\nalt-new\x1b[r",
	)); err != nil {
		t.Fatalf("write alternate-screen partial scroll: %v", err)
	}
	if sb := e.Scrollback(); len(sb) != 0 {
		t.Errorf("alternate-screen scroll polluted scrollback: %q", sb)
	}
	if _, err := e.Write([]byte("\x1b[?1049l")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if e.IsAlt() {
		t.Fatal("IsAlt = true after 1049l")
	}
}

func TestRenderTruecolorAndUnicode(t *testing.T) {
	e := newTestEmulator(t, 4, 40, nil, 0)
	if _, err := e.Write([]byte("\x1b[38;2;1;2;3mX\x1b[0m 漢🚀")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := e.RenderLine(0); !strings.Contains(got, "1;2;3") {
		t.Errorf("24-bit SGR not preserved in render: %q", got)
	}
	if grid := e.GridText(); !strings.Contains(grid, "漢") || !strings.Contains(grid, "🚀") {
		t.Errorf("wide unicode lost: %q", grid)
	}
}

func TestResize(t *testing.T) {
	e := newTestEmulator(t, 4, 20, nil, 0)
	e.Resize(10, 40)
	var resp bytes.Buffer
	e.input.setTarget(&resp)
	// CPR after cursor move to the new bottom-right proves the grid
	// really is 10x40 now.
	if _, err := e.Write([]byte("\x1b[10;40H\x1b[6n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := resp.String(); got != "\x1b[10;40R" {
		t.Errorf("CPR after resize = %q, want \\x1b[10;40R", got)
	}
}

// Session-level test: a real PTY child pumped to EOF.
func TestSessionPumpAndExit(t *testing.T) {
	s, err := Start(Options{
		Argv: []string{"printf", "hello-from-pty\\r\\n"},
		Rows: 6, Cols: 40,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatalf("child exit: %v", err)
	}
	grid := s.GridText()
	if !strings.Contains(grid, "hello-from-pty") {
		t.Errorf("pumped output missing from grid:\n%s", grid)
	}
	assertOneBlankAbove(t, grid, "✓ exit 0")
	select {
	case <-s.Done():
	default:
		t.Error("Done() not closed after Wait")
	}
}

// Regression: a command that exits but leaves a daemonized grandchild
// holding the PTY slave (stdout) open must NOT keep the session
// "running". This reproduces the `limactl start` host-agent hang —
// completion is gated on the direct child exiting, not PTY EOF. `sh`
// backgrounds a long `sleep` (which inherits fd 1 = the PTY slave) and
// exits 0; without the fix the pump blocks on Read until the sleep ends.
func TestSessionCompletesDespiteDaemonizedGrandchild(t *testing.T) {
	s, err := Start(Options{
		Argv: []string{"sh", "-c", "sleep 30 & exit 0"},
		Rows: 6, Cols: 40,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	start := time.Now()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done() never closed: a daemonized grandchild blocked completion")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("completion took %v; should finish at the child's exit, not the grandchild's", elapsed)
	}
}

// Console analog of the above: Busy() must clear at the direct child's
// exit even though a grandchild holds the PTY open.
func TestConsoleCompletesDespiteDaemonizedGrandchild(t *testing.T) {
	c := NewConsole(6, 40, 100, nil)
	t.Cleanup(func() { _ = c.Close() })
	ok, err := c.Run(nil, "", "sh", "-c", "sleep 30 & exit 0")
	if !ok || err != nil {
		t.Fatalf("Run: ok=%v err=%v", ok, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("console stayed Busy: a daemonized grandchild blocked completion")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSessionForcesEnv(t *testing.T) {
	s, err := Start(Options{
		Argv: []string{"env"},
		Env:  []string{"TERM=dumb", "KEEP=1"},
		Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatalf("child exit: %v", err)
	}
	grid := s.GridText()
	for _, want := range []string{"TERM=xterm-256color", "YOLOCTL_TUI=1", "KEEP=1"} {
		if !strings.Contains(grid, want) {
			t.Errorf("child env missing %s:\n%s", want, grid)
		}
	}
	if strings.Contains(grid, "TERM=dumb") {
		t.Error("host TERM leaked through (must never be forwarded)")
	}
}

// On EOF the session echoes the child's exit status (the same ✓/✗ line
// the palette console shows) and a close hint, so a tab reports how its
// command ended and how to dismiss it.
func TestSessionEchoesExitStatusAndHint(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		argv       []string
	}{
		{"success", "✓ exit 0", []string{"sh", "-c", "exit 0"}},
		{"failure", "✗ exit 3", []string{"sh", "-c", "exit 3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Start(Options{Argv: tc.argv, Rows: 6, Cols: 40})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Close()
			_ = s.Wait(5 * time.Second) // non-zero exit returns an error by design
			grid := s.GridText()
			if !strings.Contains(grid, tc.want) {
				t.Errorf("exit status %q missing from grid:\n%s", tc.want, grid)
			}
			if !strings.Contains(grid, "Press Enter to close this tab") {
				t.Errorf("close hint missing from grid:\n%s", grid)
			}
		})
	}
}

// dirtyFrame is a claude-like UI corpse: cursor visible, mouse/paste modes
// on, input-box borders, a prompt row, a status line below, and the cursor
// parked after the prompt — the screen a box stop leaves behind when it
// kills the session out from under an attached agent.
const dirtyFrame = "\x1b[?25h\x1b[?1003h\x1b[?1006h\x1b[?2004h" +
	"\x1b[5;1H----------------------------------------" +
	"\x1b[6;1H> " +
	"\x1b[7;1H----------------------------------------" +
	"\x1b[8;1Hxx bypass permissions on (shift+tab to cycle)" +
	"\x1b[6;3H"

// feed writes bytes into the emulator, failing the test on error.
func feed(t *testing.T, e *emulator, s string) {
	t.Helper()
	if _, err := e.Write([]byte(s)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// assertBannerClean fails if the exit banner spliced into leftover frame
// rows. Regression: box stop under an attached claude tab rendered
// "✗ exit 2ass permissions on (shift+tab to cycle)".
func assertBannerClean(t *testing.T, grid, status string) {
	t.Helper()
	if !strings.Contains(grid, status) || !strings.Contains(grid, "Press Enter to close this tab") {
		t.Fatalf("banner missing or incomplete:\n%s", grid)
	}
	for _, line := range strings.Split(grid, "\n") {
		if (strings.Contains(line, status) || strings.Contains(line, "Press Enter")) &&
			strings.Contains(line, "permissions") {
			t.Errorf("banner spliced into the dead frame: %q", line)
		}
	}
}

// assertOneBlankAbove fails unless the line holding needle has exactly one
// blank line above it — the block-separator invariant. Regression: fixed
// double-newlines in the client and the pump stacked two blank lines
// between the attach error and the exit status.
func assertOneBlankAbove(t *testing.T, grid, needle string) {
	t.Helper()
	lines := strings.Split(grid, "\n")
	for i, line := range lines {
		if !strings.Contains(line, needle) {
			continue
		}
		if i < 2 {
			t.Errorf("%q too close to the top:\n%s", needle, grid)
		} else if lines[i-1] != "" {
			t.Errorf("no blank line above %q (got %q):\n%s", needle, lines[i-1], grid)
		} else if lines[i-2] == "" {
			t.Errorf("more than one blank line above %q:\n%s", needle, grid)
		}
		return
	}
	t.Fatalf("%q not on screen:\n%s", needle, grid)
}

// A child that dies mid-frame — a full-screen agent left on the alternate
// screen, or an inline one with a partial scroll region — must get the
// exit banner on clean bottom lines of the SAME screen (the final frame
// stays readable above it), never spliced into leftover frame rows.
func TestExitBannerAfterDirtyFrame(t *testing.T) {
	for _, tc := range []struct {
		name, setup string
		wantAlt     bool
	}{
		// codex-style: inline UI committing rows through a top-anchored
		// partial scroll region on the main screen.
		{"main screen with scroll region", "\x1b[1;7r", false},
		// claude-style: everything on the alternate screen.
		{"alt screen", "\x1b[?1049h", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEmulator(t, 8, 80, nil, 0)
			feed(t, e, tc.setup+dirtyFrame)
			writeExitBanner(e, 7, "")
			if got := e.IsAlt(); got != tc.wantAlt {
				t.Errorf("IsAlt = %v, want %v — the banner belongs on the screen the child died on", got, tc.wantAlt)
			}
			if top, bottom := e.scrollRegion(); top != 0 || bottom != 7 {
				t.Errorf("scroll region survived the exit reset: %d..%d", top, bottom)
			}
			grid := e.GridText()
			if !strings.Contains(grid, "permissions on") {
				t.Errorf("dead frame no longer visible above the banner:\n%s", grid)
			}
			assertBannerClean(t, grid, "✗ exit 7")
			assertOneBlankAbove(t, grid, "✗ exit 7")
		})
	}
}

// The full tab flow for a box stop under an attached agent: the client
// resets in place and Cobra prints its error, then the child exits and the
// pump appends the banner. The corpse stays visible and exactly one blank
// line separates corpse → error → banner.
func TestExitBannerAfterClientError(t *testing.T) {
	e := newTestEmulator(t, 8, 80, nil, 0)
	feed(t, e, "\x1b[?1049h"+dirtyFrame)
	// What the embedded attach client emits, then Cobra's error line.
	feed(t, e, termio.SessionEndResetInPlace+"\r\n\r\n")
	feed(t, e, "Error: session: connection to holder closed without an exit status\r\n")
	writeExitBanner(e, 2, "")

	grid := e.GridText()
	if !e.IsAlt() {
		t.Error("tab switched off the agent's final screen")
	}
	if !strings.Contains(grid, "permissions on") {
		t.Errorf("dead frame no longer visible above the messages:\n%s", grid)
	}
	assertBannerClean(t, grid, "✗ exit 2")
	assertOneBlankAbove(t, grid, "Error: session:")
	assertOneBlankAbove(t, grid, "✗ exit 2")
	lines := strings.Split(grid, "\n")
	for i, line := range lines {
		if strings.Contains(line, "✗ exit 2") && !strings.Contains(lines[i+1], "Press Enter") {
			t.Errorf("close hint not directly under the exit status:\n%s", grid)
		}
	}
}

// The same scenario end-to-end through a real PTY child and the pump.
func TestSessionExitBannerAfterDirtyFrame(t *testing.T) {
	// printf '%b' expands the \033 escapes at runtime, one quoted frame per
	// subtest; the trailing CUP re-parks the cursor like a live agent.
	script := `printf '%b' "$1"; exit 7`
	for _, tc := range []struct {
		name, setup string
		wantAlt     bool
	}{
		{"main screen with scroll region", `\033[1;7r`, false},
		{"alt screen", `\033[?1049h`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := tc.setup + `\033[?25h\033[?1003h\033[?1006h\033[?2004h` +
				`\033[5;1H----------------------------------------` +
				`\033[6;1H> ` +
				`\033[7;1H----------------------------------------` +
				`\033[8;1Hxx bypass permissions on (shift+tab to cycle)` +
				`\033[6;3H`
			s, err := Start(Options{
				Argv: []string{"sh", "-c", script, "sh", frame},
				Rows: 8, Cols: 80,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Close()
			_ = s.Wait(5 * time.Second) // nonzero exit returns an error by design

			if got := s.IsAlt(); got != tc.wantAlt {
				t.Errorf("IsAlt = %v, want %v — the banner belongs on the screen the child died on", got, tc.wantAlt)
			}
			grid := s.GridText()
			if !strings.Contains(grid, "permissions on") {
				t.Errorf("dead frame no longer visible above the banner:\n%s", grid)
			}
			assertBannerClean(t, grid, "✗ exit 7")
			assertOneBlankAbove(t, grid, "✗ exit 7")
		})
	}
}

func TestModeTrackerFollowsChildModes(t *testing.T) {
	e := newTestEmulator(t, 4, 40, nil, 0)
	if e.MouseReporting() || e.BracketedPaste() {
		t.Fatal("modes should start off")
	}
	// claude-style: SGR mouse + any-motion + bracketed paste on.
	if _, err := e.Write([]byte("\x1b[?1006h\x1b[?1003h\x1b[?2004h")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !e.MouseReporting() || !e.MouseSGR() || !e.BracketedPaste() {
		t.Errorf("modes not tracked: mouse=%v sgr=%v paste=%v",
			e.MouseReporting(), e.MouseSGR(), e.BracketedPaste())
	}
	if _, err := e.Write([]byte("\x1b[?1003l\x1b[?2004l")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if e.MouseReporting() || e.BracketedPaste() {
		t.Errorf("mode reset not tracked: mouse=%v paste=%v",
			e.MouseReporting(), e.BracketedPaste())
	}
}

func TestModeTrackerSplitWrites(t *testing.T) {
	e := newTestEmulator(t, 4, 20, nil, 0)
	_, _ = e.Write([]byte("\x1b[?20"))
	_, _ = e.Write([]byte("04h"))
	if !e.BracketedPaste() {
		t.Error("mode sequence split across writes not tracked")
	}
}

// DECSET 1007 (alternate scroll) must survive a split across PTY reads.
func TestAlternateScrollModeTrackedFromRawChildStream(t *testing.T) {
	e := newTestEmulator(t, 4, 40, nil, 0)
	if !e.AlternateScroll() {
		t.Fatal("Ghostty alternate-scroll default should be on")
	}
	feed(t, e, "\x1b[?1007l")
	if _, err := e.Write([]byte("\x1b[?100")); err != nil {
		t.Fatalf("write set prefix: %v", err)
	}
	if _, err := e.Write([]byte("7h")); err != nil {
		t.Fatalf("write set suffix: %v", err)
	}
	if !e.AlternateScroll() {
		t.Fatal("split DECSET 1007 was not tracked")
	}
	if _, err := e.Write([]byte("\x1b[?1007l")); err != nil {
		t.Fatalf("write reset: %v", err)
	}
	if e.AlternateScroll() {
		t.Fatal("DECRST 1007 was not tracked")
	}
}

// Styled regions must not bleed into adjacent cells or the next row.
func TestRenderLineStyleSpans(t *testing.T) {
	e := newTestEmulator(t, 4, 20, nil, 0)
	if _, err := e.Write([]byte("ab\x1b[7mCD\x1b[27mef")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := e.RenderLine(0)
	iOn := strings.Index(out, "\x1b[7m")
	if iOn == -1 {
		t.Fatalf("reverse span lost: %q", out)
	}
	rest := out[iOn:]
	reset := strings.IndexAny(rest, "e") // first plain char after span
	if reset == -1 {
		t.Fatalf("plain text after span lost: %q", out)
	}
	// Between the styled span and 'e' there must be an attribute change
	// (27 or 0) — otherwise reverse video bleeds (finding E6).
	between := rest[:reset]
	if !strings.Contains(between, "[27m") && !strings.Contains(between, "[0m") &&
		!strings.Contains(between, "[m") {
		t.Errorf("reverse not closed before plain text: %q", out)
	}
}

// Cancelled attributes must not accumulate at a styled-region transition.
func TestRenderRegionTransitionResets(t *testing.T) {
	e := newTestEmulator(t, 4, 20, nil, 0)
	// Child: faint "ab", then cancel-dim + red "cd". The format model
	// tracks it correctly; the rendered transition must not leave
	// faint active over "cd".
	if _, err := e.Write([]byte("\x1b[2mab\x1b[22m\x1b[31mcd")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := e.RenderLine(0)
	iB := strings.IndexByte(out, 'b')
	iD := strings.IndexByte(out, 'd')
	if iB == -1 || iD == -1 {
		t.Fatalf("text lost: %q", out)
	}
	between := out[iB:iD]
	if !strings.Contains(between, "[0m") && !strings.Contains(between, "[0;") &&
		!strings.Contains(between, ";22") && !strings.Contains(between, "[22") {
		t.Errorf("faint not cleared at region transition (E6 leak): %q", out)
	}
}

// Rows contain no painted cursor; CursorPos supplies host-cursor placement.
func TestCursorSuppressedInRenderExposedAsPos(t *testing.T) {
	e := newTestEmulator(t, 4, 20, nil, 0)
	if _, err := e.Write([]byte("ab")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out := e.RenderLine(0); strings.Contains(out, "\x1b[7m") {
		t.Errorf("cursor cell painted in row output: %q", out)
	}
	col, row, visible := e.CursorPos()
	if col != 2 || row != 0 || !visible {
		t.Errorf("CursorPos = (%d,%d,%v), want (2,0,true)", col, row, visible)
	}
}

// Console end-to-end: output that overflows the grid must land in
// scrollback (the wheel scrolls exactly these lines).
func TestConsoleEvictsToScrollback(t *testing.T) {
	c := NewConsole(6, 40, 0, nil)
	t.Cleanup(func() { _ = c.Close() })
	ok, err := c.Run(nil, "", "seq", "1", "50")
	if !ok || err != nil {
		t.Fatalf("Run: ok=%v err=%v", ok, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.Busy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if c.Busy() {
		t.Fatal("console command did not finish")
	}
	sb := c.Scrollback()
	if len(sb) == 0 {
		t.Fatal("no eviction into console scrollback")
	}
	if !strings.Contains(strings.Join(sb, "\n"), "1") {
		t.Errorf("scrollback content unexpected: %q", sb[:min(3, len(sb))])
	}
}

// Shrinking, clearing, repainting and growing must preserve valid row widths.
func TestResizeNarrowThenChildRedrawRenders(t *testing.T) {
	// Start with a wide grid.
	e := newTestEmulator(t, 24, 201, nil, 0)
	// Shrink to a narrower width, as a window resize would.
	e.Resize(24, 95)
	// A full-screen child redraws after SIGWINCH: clear screen, home,
	// then paint new content.
	if _, err := e.Write([]byte("\x1b[2J\x1b[Hhello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Rendering every row must not panic on a stale-width format row.
	for r := 0; r < e.Rows(); r++ {
		_ = e.RenderLine(r)
	}
	// Growing taller after the shrink also appends rows;
	// then clear+paint again and re-render the whole (now taller) grid.
	e.Resize(40, 95)
	if _, err := e.Write([]byte("\x1b[2J\x1b[40;1Hbottom")); err != nil {
		t.Fatalf("write after grow: %v", err)
	}
	for r := 0; r < e.Rows(); r++ {
		_ = e.RenderLine(r)
	}
}

// Growing the grid must keep its height bounded and allow normal eviction.
func TestResizeGrowKeepsGridBounded(t *testing.T) {
	e := newTestEmulator(t, 24, 80, nil, 0)
	e.Resize(49, 200)
	if got := e.Rows(); got != 49 {
		t.Fatalf("rows after grow-resize = %d, want 49", got)
	}
	for i := range 60 {
		if _, err := fmt.Fprintf(e, "l%02d\r\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := e.Rows(); got != 49 {
		t.Errorf("rows after output = %d, want 49", got)
	}
	if len(e.Scrollback()) == 0 {
		t.Error("no eviction after overflowing the grown grid")
	}
}

// An end hint follows the close hint on the same banner line; without one
// the banner is unchanged.
func TestExitBannerEndHint(t *testing.T) {
	e := newTestEmulator(t, 8, 80, nil, 0)
	feed(t, e, "agent output\r\n")
	writeExitBanner(e, 137, "r to re-attach to api")
	grid := e.GridText()
	if !strings.Contains(grid, "✗ exit 137\n"+ExitCloseHint+" · r to re-attach to api") {
		t.Fatalf("banner with hint:\n%s", grid)
	}
	plain := newTestEmulator(t, 8, 80, nil, 0)
	writeExitBanner(plain, 0, "")
	if g := plain.GridText(); !strings.Contains(g, ExitCloseHint+"\n") || strings.Contains(g, "·") {
		t.Fatalf("banner without hint changed:\n%s", g)
	}
}
