package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/termio"
	"golang.org/x/term"
)

// The panes acceptance driver (PLAN-r1.3 §5 amendment). It runs the real TUI
// program (Bubble Tea input decoding, the model and its renderer state) with
// real pane sessions: managed `box attach` views of a real holder (a fake
// sandbox behind it), a space view that sends real attach requests, shells,
// mouse-echo children and a bell. Each step is driven by the bytes a host
// terminal would send (the palette key, chord keys, SGR mouse reports at
// screen positions), and the composed screen after each step is written to
// $YOLOCTL_PANES_ACCEPT_OUT as NN-step.txt (plain) and NN-step.ans (styled).
//
// Not covered here: browser and image requests (they need the holder's
// in-sandbox host-integration helper) and real host terminal rendering.

// TestFakeSpaceViewChild is the space view stand-in: on "a" it asks the TUI
// for `box attach coder --space dev` over its real request channel.
func TestFakeSpaceViewChild(t *testing.T) {
	if os.Getenv("YP_FAKE_SPACE_VIEW") != "1" {
		return
	}
	requests, err := termio.InheritedTabRequests()
	if err != nil || requests == nil {
		os.Exit(70)
	}
	if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
		os.Exit(71)
	}
	_, _ = os.Stdout.WriteString("SPACE VIEW dev\r\n* coder  running\r\n(a: attach coder)\r\n")
	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			os.Exit(0)
		}
		if bytes.Contains(buf[:n], []byte("a")) {
			_ = termio.WriteTabRequest(requests, termio.TabRequest{Op: "attach", Space: "dev", Box: "coder"})
			_, _ = os.Stdout.WriteString("requested coder\r\n")
		}
	}
}

// mouseEcho prints every input chunk it reads, escaped, after enabling drag
// tracking in SGR cell (1006) or SGR pixel (1016) format.
const mouseEcho = `import os,sys,tty
tty.setraw(0)
os.write(1,('\x1b[?1002h\x1b[?%sh' % sys.argv[1]).encode()+('MOUSE %s ready\r\n' % sys.argv[1]).encode())
while True:
 d=os.read(0,4096)
 if not d: break
 os.write(1,(repr(d)+'\r\n').encode())
`

// driverScript maps the palette commands the driver types to pane children.
const driverScript = `#!/bin/sh
case "$1 $2" in
"space view") exec env YP_FAKE_SPACE_VIEW=1 "$YP_TESTBIN" -test.run='^TestFakeSpaceViewChild$' ;;
"box attach") exec "$YP_TESTBIN" -test.run='^TestManagedRelayChild$' ;;
"attach shell") exec sh -c 'printf "shell \$ "; exec cat' ;;
"attach mouse-cell") exec python3 -c "$YP_MOUSE_ECHO" 1006 ;;
"attach mouse-pixel") exec python3 -c "$YP_MOUSE_ECHO" 1016 ;;
"attach bell") exec sh -c 'printf "bell in 1s"; sleep 1; printf "\a rang"; exec sleep 600' ;;
esac
exit 3
`

// fakeHost is the host terminal side: it records the TUI's output and
// answers the cell-size and pixel-mouse queries a measuring terminal would.
type fakeHost struct {
	mu    sync.Mutex
	out   bytes.Buffer
	seen  int
	input io.Writer
	cellW int
	cellH int
}

func (h *fakeHost) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.out.Write(p)
	fresh := h.out.String()[h.seen:]
	h.seen = h.out.Len()
	var reply strings.Builder
	if strings.Contains(fresh, "\x1b[16t") {
		fmt.Fprintf(&reply, "\x1b[6;%d;%dt", h.cellH, h.cellW)
	}
	if strings.Contains(fresh, "\x1b[?1016$p") {
		reply.WriteString("\x1b[?1016;2$y")
	}
	if strings.Contains(fresh, mouseEncodingRequest(true).String()) {
		reply.WriteString("\x1b[?1016;1$y")
	}
	if strings.Contains(fresh, mouseEncodingRequest(false).String()) {
		reply.WriteString("\x1b[?1016;2$y")
	}
	if reply.Len() > 0 {
		go func(s string) { _, _ = io.WriteString(h.input, s) }(reply.String())
	}
	return n, err
}

type captureMsg struct{ reply chan [2]string }

// driverModel lets the driver read the screen from inside the program loop.
type driverModel struct{ *model }

func (d driverModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if c, ok := msg.(captureMsg); ok {
		v := d.View()
		cursor := ""
		if v.Cursor != nil {
			cursor = fmt.Sprintf("host cursor at column %d, row %d", v.Cursor.X, v.Cursor.Y)
		} else {
			cursor = "no host cursor"
		}
		c.reply <- [2]string{v.Content, fmt.Sprintf("title %q; %s; status %q", v.WindowTitle, cursor, d.status)}
		return d, nil
	}
	_, cmd := d.model.Update(msg)
	return d, cmd
}

func TestPanesAcceptanceDriver(t *testing.T) {
	out := os.Getenv("YOLOCTL_PANES_ACCEPT_OUT")
	if out == "" {
		t.Skip("set YOLOCTL_PANES_ACCEPT_OUT to a directory to run the panes acceptance driver")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	sock, agent := startManagedHolder(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "yoloctl")
	if err := os.WriteFile(script, []byte(driverScript), 0o755); err != nil {
		t.Fatal(err)
	}
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return script, nil }
	t.Setenv("YP_TESTBIN", os.Args[0])
	t.Setenv("YP_MOUSE_ECHO", mouseEcho)
	t.Setenv("YOLOCTL_TEST_RELAY_SOCK", sock)

	const width, height = 120, 34
	m := newTestModel(t, width, height)
	m.hostGeometry = hostGeometry{}
	inR, inW := io.Pipe()
	host := &fakeHost{input: inW, cellW: 8, cellH: 16}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	p := tea.NewProgram(driverModel{m}, tea.WithContext(ctx), tea.WithInput(inR), tea.WithOutput(host),
		tea.WithWindowSize(width, height), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
	m.program = p
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	t.Cleanup(func() {
		p.Quit()
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the TUI program did not exit")
		}
	})

	var log strings.Builder
	step := 0
	send := func(s string) {
		t.Helper()
		if _, err := io.WriteString(inW, s); err != nil {
			t.Fatal(err)
		}
	}
	screen := func() (string, string) {
		reply := make(chan [2]string, 1)
		p.Send(captureMsg{reply: reply})
		select {
		case s := <-reply:
			return s[0], s[1]
		case <-time.After(10 * time.Second):
			t.Fatal("the program did not answer a capture")
			return "", ""
		}
	}
	// await waits until the plain screen satisfies cond, then records it.
	await := func(name, what string, cond func(plain string) bool) string {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			styled, facts := screen()
			plain := ansi.Strip(styled)
			if cond(plain) {
				step++
				base := fmt.Sprintf("%02d-%s", step, name)
				_ = os.WriteFile(filepath.Join(out, base+".txt"), []byte(plain+"\n"), 0o644)
				_ = os.WriteFile(filepath.Join(out, base+".ans"), []byte("\x1b[0m\x1b[H\x1b[2J"+strings.ReplaceAll(styled, "\n", "\r\n")+"\x1b[0m\r\n"), 0o644)
				fmt.Fprintf(&log, "%02d %s: %s\n   %s\n", step, name, what, facts)
				return plain
			}
			if time.Now().After(deadline) {
				_ = os.WriteFile(filepath.Join(out, fmt.Sprintf("FAILED-%s.txt", name)), []byte(plain+"\n"), 0o644)
				_ = os.WriteFile(filepath.Join(out, "steps.txt"), []byte(log.String()), 0o644)
				t.Fatalf("step %s (%s) never happened:\n%s", name, what, plain)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	const paletteKey = "\x19" // Ctrl+Y
	const prefixKey = "\x11"  // Ctrl+Q, the default pane prefix
	palette := func(cmd string) { send(paletteKey + cmd + "\r") }
	chord := func(keys string) { send(prefixKey + keys) }
	click := func(x, y int) { send(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+1, y+1)) }
	panes := func() int { return len(m.tabs.Current().Layout.Panes()) }
	_ = panes

	// 1. A space view tab.
	palette("space view --space dev")
	await("space-view", "a space view opens in a new tab", func(s string) bool { return strings.Contains(s, "SPACE VIEW dev") })
	// 2. D6 default: its attach request opens a new tab.
	send("a")
	agent.emit(t, "coder agent: ready\r\n")
	await("attach-new-tab", "an unarmed attach request opens a new tab (D6)", func(s string) bool {
		return strings.Contains(s, "coder agent: ready") && strings.Contains(s, "❷")
	})
	// 3. Back to the space view, open an empty pane right of it, return to
	// the space view and request again: the identical argv is reused (focus
	// moves to the coder tab) — reuse wins over the empty pane, and says so.
	send(paletteKey + "\x1b[1;5D") // in the palette, Ctrl+← selects the previous tab
	await("space-view-again", "Ctrl+Y Ctrl+← returns to the space view tab", func(string) bool {
		return m.tabs.Focused().Command == "yoloctl space view --space dev"
	})
	send(prefixKey)
	await("prefix-armed", "Ctrl+Q opens the pane menu at the focused pane's top-left", func(s string) bool {
		return strings.Contains(s, "Split right") && strings.Contains(s, menuHint) && m.prefix != nil
	})
	send("|") // the prefix is taken from the palette too
	await("empty-pane", "Ctrl+Q | opens an empty pane right of the space view: its hint, its bar, the palette open for it", func(s string) bool {
		return strings.Contains(s, "what goes here?") && strings.Contains(s, " ▸ empty") && m.focusPalette
	})
	chord("\x1b[D") // ←: back to the space view
	send("a")
	await("reuse", "an identical attach is reused: the coder tab is selected, and the status says where", func(s string) bool {
		return strings.Contains(s, "coder agent: ready") && !strings.Contains(s, "SPACE VIEW") && strings.Contains(s, "already open in tab 2")
	})
	// 4. Close the coder tab, back to the space view, whose empty pane stayed
	// through the switches; the request now fills it.
	send(paletteKey + "\x17") // Ctrl+Y Ctrl+W closes the focused pane (its tab)
	send("\x1b")              // Esc: leave the palette
	await("closed-tab", "Ctrl+W closes the coder tab; the space view and its empty pane remain", func(s string) bool {
		return strings.Contains(s, "SPACE VIEW dev") && !strings.Contains(s, "❷") && !m.focusPalette && strings.Contains(s, "what goes here?")
	})
	send("a")
	await("filled-by-request", "the space view's request fills its tab's empty pane: coder opens right of it", func(s string) bool {
		return strings.Contains(s, "SPACE VIEW dev") && strings.Contains(s, "coder agent: ready") && !strings.Contains(s, "what goes here?")
	})
	// 5. A shell pane below coder: an empty pane, filled by a typed command.
	chord("-")
	send("attach shell\r")
	await("shell-pane", "an empty pane below coder takes a typed live command: a shell", func(s string) bool {
		return strings.Contains(s, "shell $") && strings.Contains(s, "├") || strings.Contains(s, "┤")
	})
	// 6. Type into the focused shell; focus moves with the arrows.
	send("hello panes")
	await("typing", "keys reach only the focused shell", func(s string) bool { return strings.Contains(s, "shell $ hello panes") })
	chord("\x1b[D") // ←
	await("focus-left", "← moves focus to the space view (title and cursor follow)", func(string) bool {
		return m.tabs.Focused().Command == "yoloctl space view --space dev"
	})
	chord("\x1b[C") // →: the nearer of coder (top) and the shell (bottom)
	await("focus-right", "→ reaches coder, the top-right pane", func(string) bool {
		return strings.HasPrefix(m.tabs.Focused().Command, "yoloctl box attach coder")
	})
	chord("\x1b[B") // ↓
	await("focus-down", "↓ reaches the shell below coder", func(string) bool {
		return m.tabs.Focused().Command == "yoloctl attach shell"
	})
	chord("\x1b[A") // ↑
	await("focus-up", "↑ returns to coder", func(string) bool {
		return strings.HasPrefix(m.tabs.Focused().Command, "yoloctl box attach coder")
	})
	// 7. D1: the same session in a second pane; latest resize wins.
	chord("|")
	send("box attach coder --space dev\r")
	var lines strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&lines, "coder line %02d %s\r\n", i, strings.Repeat("·", 70))
	}
	agent.emit(t, lines.String())
	await("same-session", "a second view of coder (D1): the holder has the newest pane's size", func(s string) bool {
		return strings.Count(s, "coder line 39") >= 2
	})
	// 8. Zoom, unzoom, resize a border, equalize (on the focused D1 view).
	tab := m.tabs.Current()
	focused := func() string { return m.tabs.Focused().Command }
	chord("z")
	await("zoom", "z zooms the focused pane to the tab", func(s string) bool {
		return tab.Layout.Zoomed() != nil && !strings.Contains(s, "SPACE VIEW") && !m.focusPalette
	})
	chord("z")
	await("unzoom", "z again restores the layout", func(s string) bool { return tab.Layout.Zoomed() == nil && strings.Contains(s, "SPACE VIEW") })
	before := tab.Focused().Rect()
	chord("\x1b[1;5D") // Ctrl+←
	await("resize-border", "Ctrl+← moves the focused pane's left border one cell", func(string) bool {
		return tab.Focused().Rect().X == before.X-1
	})
	send("\x1b[1;3D") // Alt+←, inside the repeat window: no prefix needed
	await("resize-repeat", "within a second Alt+← moves it five more without the prefix", func(s string) bool {
		return tab.Focused().Rect().X == before.X-6
	})
	chord("=")
	await("equalize", "= equalizes the splits", func(string) bool { return tab.Focused().Rect() == before })
	chord("|")
	await("split-refused", "a split that would go below 20×5 is refused in the menu footer; the menu stays open", func(s string) bool {
		return strings.Contains(s, "not enough room to split") && tab.Layout.Empty() == nil && m.prefix != nil
	})
	send("\x1b")
	await("menu-closed", "Esc closes the menu", func(string) bool { return m.prefix == nil })
	cur := tab.Focused()
	chord("o")
	await("focus-next", "o focuses the next pane in tree order", func(string) bool { return tab.Focused() != cur })
	moved := tab.Focused()
	n, at := len(tab.Layout.Panes()), slices.Index(tab.Layout.Panes(), moved)
	chord("{")
	await("swap", "{ swaps the focused pane with the previous one; focus moves with it", func(string) bool {
		return tab.Focused() == moved && slices.Index(tab.Layout.Panes(), moved) == (at+n-1)%n
	})
	chord("}")
	await("swap-back", "} swaps it back", func(string) bool { return slices.Index(tab.Layout.Panes(), moved) == at })
	chord("?")
	await("menu-question", "? keeps the menu open (the key sheet is retired)", func(s string) bool {
		return strings.Contains(s, "Detach whole tab") && m.prefix != nil
	})
	send("\x1b")
	await("menu-question-closed", "Esc closes it", func(string) bool { return m.prefix == nil })
	_ = focused
	// 9. A new tab: a bell pane, then a shell split right of it takes focus;
	// the bell rings in the unfocused pane and marks it.
	palette("attach bell")
	await("bell-tab", "a bell pane in a new tab", func(s string) bool { return strings.Contains(s, "bell in 1s") })
	chord("|")
	send("attach shell\r")
	await("bell-mark", "the bell rang in the unfocused pane: ● on its border", func(s string) bool {
		return strings.Contains(s, "rang") && strings.Contains(s, "●")
	})
	chord("\x1b[D")
	await("bell-mark-cleared", "← focuses the bell pane and clears its mark", func(s string) bool {
		return !strings.Contains(s, "●") && m.tabs.Focused().Command == "yoloctl attach bell"
	})
	// 10. Mouse kinds in a new tab: a cell-mouse pane beside a pixel-mouse
	// pane. A click in the unfocused cell pane focuses it and arrives as
	// pane-local cells; the pixel pane gets pane-relative pixels once the
	// host has switched to pixel reports.
	palette("attach mouse-cell")
	chord("|")
	send("attach mouse-pixel\r")
	await("mouse-panes", "a cell-mouse pane and a pixel-mouse pane", func(s string) bool {
		return strings.Contains(s, "MOUSE 1006 ready") && strings.Contains(s, "MOUSE 1016 ready")
	})
	top := m.tabTop()
	right := m.tabs.Current().Layout.Panes()[1].Rect()
	// The pixel pane is focused, so the host reports pixels: the driver sends
	// window pixels (8×16 cells) for a click in the unfocused cell pane.
	await("pixel-mode", "the host switched to pixel reports for the focused pixel pane", func(string) bool {
		return m.hostMouseEncoding.pixels && !m.hostMouseEncoding.negotiating
	})
	pixelClick := func(x, y int) {
		px, py := x*8+3, y*16+7
		send(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", px, py, px, py))
	}
	pixelClick(5, top+2)
	await("cell-click", "a click in the unfocused cell pane focuses it and arrives as pane-local cells (6;3)", func(s string) bool {
		return strings.Contains(s, `<0;6;3M`) && m.tabs.Focused().Command == "yoloctl attach mouse-cell"
	})
	await("cell-mode", "with the cell pane focused the host is back to cell reports", func(string) bool {
		return !m.hostMouseEncoding.pixels && !m.hostMouseEncoding.negotiating
	})
	click(right.X+5, top+2)
	await("pixel-click-from-cells", "a cell click in the unfocused pixel pane focuses it; the child gets the cell's centre in pane pixels (44;40)", func(s string) bool {
		return strings.Contains(s, `<0;44;40M`) && m.tabs.Focused().Command == "yoloctl attach mouse-pixel"
	})
	await("pixel-mode-again", "focused again, the pixel pane gets host pixel reports", func(string) bool {
		return m.hostMouseEncoding.pixels && !m.hostMouseEncoding.negotiating
	})
	pixelClick(right.X+5, top+2)
	await("pixel-click", "a host pixel report arrives pane-relative (43;39)", func(s string) bool {
		return strings.Contains(s, `<0;43;39M`)
	})
	chord(prefixKey)
	await("double-prefix", "the prefix twice sends Ctrl+Q to the focused pane", func(s string) bool {
		return strings.Contains(s, `b'\x11'`)
	})
	chord("w")
	await("tab-picker", "w lists the tabs to switch", func(s string) bool { return strings.Contains(s, "switch to a tab") })
	send("1")
	await("picked", "a digit switches at once", func(string) bool { return m.picker == nil && m.tabs.Active == 0 })
	chord("p")
	await("previous-tab", "p: the previous tab (wraps to the last)", func(string) bool { return m.tabs.Active == len(m.tabs.Tabs)-1 })
	// 11. Host resize: every pane is laid out again.
	p.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	await("host-resize", "a host resize re-flows the panes to 100 columns", func(string) bool {
		r := m.tabs.Current().Layout.Panes()[1].Rect()
		return m.width == 100 && r.X+r.W == 100
	})
	// 12. Close every pane: a running command's close asks first (the popup
	// in the palette area, Keep by default); y closes; the last pane closes
	// its tab.
	chord("x")
	await("close-popup", "x on a running command opens the close popup", func(s string) bool {
		return strings.HasPrefix(s, "╭─ Close pane?") && strings.Contains(s, "[ Keep ]   [ Close ]")
	})
	send("y")
	chord("x")
	send("y")
	await("last-close", "x closes panes; the last pane closes its tab", func(string) bool { return len(m.tabs.Tabs) == 2 })
	// 13. A new tab by prefix c; prefix d detaches it, asking first.
	chord("c")
	await("new-tab-entry", "c opens the palette for a new tab", func(s string) bool { return strings.Contains(s, "new tab: type a command") })
	send("attach shell\r")
	await("new-tab", "the live command opens a new tab", func(s string) bool { return len(m.tabs.Tabs) == 3 && strings.Contains(s, "shell $") })
	chord("d")
	await("detach-tab-popup", "d on a running command asks for the whole tab", func(s string) bool { return strings.HasPrefix(s, "╭─ Close tab?") })
	send("y")
	await("detached-tab", "y detaches the tab", func(string) bool { return len(m.tabs.Tabs) == 2 })
	// 14. Moves. ! breaks the bell pane out into a new last tab; j brings
	// the shell tab back in below it.
	bell := m.tabs.Current()
	chord("!")
	await("break", "! breaks the focused bell pane out into a new last tab", func(string) bool {
		return len(m.tabs.Tabs) == 3 && m.tabs.Active == 2 && m.tabs.Focused().Command == "yoloctl attach bell" && len(bell.Layout.Panes()) == 1
	})
	chord("j")
	await("join-picker", "j lists the tabs to bring in below the focused pane; this tab is greyed", func(s string) bool {
		return strings.Contains(s, "bring a tab in below the focused pane") && strings.Contains(s, "that is this tab")
	})
	send("2")
	await("join", "2 brings the shell tab in below the bell pane, in one move", func(string) bool {
		ps := m.tabs.Current().Layout.Panes()
		return len(m.tabs.Tabs) == 2 && len(ps) == 2 && ps[0].Command == "yoloctl attach bell" && ps[1].Command == "yoloctl attach shell"
	})
	// 15. Dock drags: tab ❶'s four panes do not fit beside the bell pane
	// (hatched, the reason on the status line; Esc cancels); a new shell
	// tab docks right of the bell pane.
	strip := m.tabStripRow()
	bellRect := m.tabs.Current().Layout.Panes()[0].Rect()
	dropX, dropY := bellRect.X+bellRect.W-3, m.tabTop()+bellRect.Y+bellRect.H/2
	press := func(x, y int) { send(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1)) }
	drag := func(x, y int) { send(fmt.Sprintf("\x1b[<32;%d;%dM", x+1, y+1)) }
	release := func(x, y int) { send(fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+1)) }
	press(tabClickX(m, 0), strip)
	drag(dropX, dropY)
	await("dock-refused", "tab ❶ dragged over the bell pane's right half: hatched, refused with its reason", func(s string) bool {
		return strings.Contains(s, "╱╱╱") && strings.Contains(s, "too small for tab ❶'s panes")
	})
	send("\x1b")
	release(dropX, dropY)
	await("dock-cancelled", "Esc cancels the drag; nothing moved", func(s string) bool {
		return m.chrome == nil && len(m.tabs.Tabs) == 2 && !strings.Contains(s, "╱╱╱")
	})
	chord("c")
	send("attach shell\r")
	await("dock-source", "a new shell tab to dock", func(string) bool { return len(m.tabs.Tabs) == 3 })
	chord("2")
	await("dock-destination", "back to the bell tab", func(string) bool { return m.tabs.Active == 1 })
	press(tabClickX(m, 2), strip)
	drag(dropX, dropY)
	await("dock-drag", "the shell tab dragged over the bell pane's right half: tinted, the tab still visible, its label reversed", func(s string) bool {
		return m.chrome != nil && m.chrome.kind == chromeDock && m.tabs.Active == 1 && strings.Contains(s, dockStatus)
	})
	release(dropX, dropY)
	await("dock-drop", "the release docks the shell right of the bell pane; the tab left the strip", func(string) bool {
		ps := m.tabs.Current().Layout.Panes()
		return len(m.tabs.Tabs) == 2 && len(ps) == 3 && ps[1].Command == "yoloctl attach shell" && ps[1].Rect().X > ps[0].Rect().X
	})
	_ = os.WriteFile(filepath.Join(out, "steps.txt"), []byte(log.String()), 0o644)
	t.Log("\n" + log.String())
}
