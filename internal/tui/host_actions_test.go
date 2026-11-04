package tui

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/term"
	xterm "golang.org/x/term"
)

const testImagePath = "/tmp/yoloctl-image-0123456789ABCDEFG/clipboard.png"

// A real CLI-child process with an inherited private socket and ordinary PTY
// input. Deliberately independent wire constants exercise the protocol boundary.
func TestHostIntegrationChild(t *testing.T) {
	mode := os.Getenv("YOLOCTL_TEST_HOST_MODE")
	if mode == "" {
		return
	}
	f, err := termio.InheritedView()
	if err != nil || f == nil {
		os.Exit(71)
	}
	defer func() { _ = f.Close() }()
	if _, err := xterm.MakeRaw(0); err != nil {
		os.Exit(72)
	}
	send := func(kind byte, p []byte) {
		var header [3]byte
		header[0] = kind
		binary.BigEndian.PutUint16(header[1:], uint16(len(p)))
		if _, err := f.Write(append(header[:], p...)); err != nil {
			os.Exit(73)
		}
	}
	recv := func() (byte, []byte, error) {
		var header [3]byte
		if _, err := io.ReadFull(f, header[:]); err != nil {
			return 0, nil, err
		}
		p := make([]byte, int(binary.BigEndian.Uint16(header[1:])))
		_, err := io.ReadFull(f, p)
		return header[0], p, err
	}
	send('h', []byte{6})
	if k, _, err := recv(); k != 'v' || err != nil {
		os.Exit(74)
	}
	vt, err := terminal.New(6, 80, 0, nil)
	if err != nil {
		os.Exit(75)
	}
	if mode != "legacy" {
		_, _ = vt.Write([]byte("\x1b[>31u"))
	}
	_, _ = vt.Write([]byte("\x1b[?2004hREADY"))
	snapshot, err := vt.Snapshot()
	if err != nil {
		os.Exit(76)
	}
	bundle, _ := controller.ExecutableIdentity()
	meta, _ := json.Marshal(map[string]any{"Bundle": bundle, "Core": terminal.CoreIdentity(), "Session": "host-fixture", "Client": "client", "SnapshotBytes": len(snapshot), "ImagePaste": true})
	send('b', meta)
	for len(snapshot) > 0 {
		n := min(len(snapshot), 32768)
		send('s', snapshot[:n])
		snapshot = snapshot[n:]
	}
	send('S', nil)
	path := os.Getenv("YOLOCTL_TEST_HOST_CAPTURE")
	if mode == "browser" {
		// Plausible action text on stdout cannot become a host request.
		_, _ = os.Stdout.WriteString("{\"id\":\"forged\",\"url\":\"https://forged.invalid/\"}")
		request, _ := json.Marshal(session.BrowserRequest{ID: "0123456789ABCDEFG", URL: "https://example.org/approve-me"})
		send('u', request)
	}
	metadataDone := make(chan struct{})
	go func() {
		defer close(metadataDone)
		var id string
		var image bytes.Buffer
		for {
			kind, p, err := recv()
			if err != nil {
				return
			}
			switch kind {
			case 'j':
				var v struct{ ID, Path string }
				_ = json.Unmarshal(p, &v)
				p, _ := json.Marshal(map[string]string{"id": v.ID, "path": "/workdir" + v.Path})
				send('J', p)
			case 'p':
				var v struct{ ID string }
				_ = json.Unmarshal(p, &v)
				id = v.ID
			case 'i':
				_, _ = image.Write(p)
			case 'I':
				_ = os.WriteFile(path+".png", image.Bytes(), 0o600)
				p, _ := json.Marshal(map[string]string{"id": id, "path": testImagePath})
				send('P', p)
			case 'U':
				_ = os.WriteFile(path+".reply", p, 0o600)
			}
		}
	}()
	var input []byte
	var b [1]byte
	var requestID string
	sequence := 0
	for {
		if _, err := os.Stdin.Read(b[:]); err != nil {
			break
		}
		input = append(input, b[0])
		if mode == "browser-loop" {
			switch b[0] {
			case 'b':
				sequence++
				requestID = fmt.Sprintf("%016d", sequence)
				request, _ := json.Marshal(session.BrowserRequest{ID: requestID, URL: fmt.Sprintf("https://example.org/request-%d", sequence)})
				send('u', request)
			case 'c':
				// Holder timeout/cancellation, outside canonical VT events.
				send('x', []byte(requestID))
			}
		}
		if bytes.HasSuffix(input, []byte("END")) {
			break
		}
	}
	_ = os.WriteFile(path, input, 0o600)
	send('e', []byte{0, 0, 0, 0})
	_ = f.Close()
	<-metadataDone
	os.Exit(0)
}

func hostIntegrationView(t *testing.T, mode string) (*model, *term.Session, string, <-chan browserRequestMsg) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input")
	m := &model{cfg: &config.TUI{}, width: 80, height: 8}
	m.console = term.NewConsole(6, 80, 0, nil)
	t.Cleanup(func() { _ = m.console.Close() })
	requests := make(chan browserRequestMsg, 2)
	handler := func(s *term.Session, r session.BrowserRequest, done <-chan struct{}) {
		requests <- browserRequestMsg{s, r, done}
	}
	if mode == "browser-loop" {
		// No request is emitted until 'b', after the test starts the program.
		handler = m.receiveBrowserRequest
	}
	s, err := term.Start(term.Options{TrustedView: true, Rows: 6, Cols: 80,
		Argv:      []string{os.Args[0], "-test.run=^TestHostIntegrationChild$"},
		Env:       append(os.Environ(), "YOLOCTL_TEST_HOST_MODE="+mode, "YOLOCTL_TEST_HOST_CAPTURE="+path),
		OnBrowser: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	m.tabs.Add(testTab(s, "attach fixture", false))
	deadline := time.Now().Add(3 * time.Second)
	for !s.Host().CanPasteImage() || !s.BracketedPaste() || (mode != "legacy" && s.KeyboardFlags() != 31) {
		if time.Now().After(deadline) {
			t.Fatal("private view not ready")
		}
		time.Sleep(time.Millisecond)
	}
	return m, s, path, requests
}

// Use the real private-channel callback, event loop, host key reader and
// renderer. Direct model calls cannot detect a stuck watcher or a callback
// whose message never reaches a visible frame. The holder/reconnect boundary
// is covered separately in session tests and the isolated live smoke.
func TestBrowserPromptsSurviveRepeatedRequestsAndDisconnect(t *testing.T) {
	old := openHostURL
	t.Cleanup(func() { openHostURL = old })
	var opened atomic.Int32
	openHostURL = func(context.Context, string) error {
		opened.Add(1)
		return nil
	}
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprintf("standalone=%t", standalone), func(t *testing.T) {
			m, s, path, _ := hostIntegrationView(t, "browser-loop")
			m.standalone, m.focusPalette = standalone, false
			m.pending = &atomic.Bool{}
			m.cfg.Seams.Browser = "ask"
			pr, pw := io.Pipe()
			defer func() { _ = pr.Close(); _ = pw.Close() }()
			out := &observedOutput{ready: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(pr), tea.WithOutput(out),
				tea.WithWindowSize(90, 14), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
			m.program = p
			done := make(chan error, 1)
			go func() { _, err := p.Run(); done <- err }()
			defer func() {
				p.Quit()
				if err := <-done; err != nil {
					t.Errorf("frontend: %v", err)
				}
			}()
			vt, err := terminal.New(14, 90, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = vt.Close() }()
			offset := 0
			wait := func(description string, ready func() bool) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if ready() {
						return
					}
					time.Sleep(time.Millisecond)
				}
				t.Fatalf("timeout waiting for %s", description)
			}
			visible := func(text string, present bool) {
				t.Helper()
				wait(fmt.Sprintf("visible %q = %t", text, present), func() bool {
					out.mu.Lock()
					data := bytes.Clone(out.buf.Bytes()[offset:])
					offset = out.buf.Len()
					out.mu.Unlock()
					if _, err := vt.Write(data); err != nil {
						t.Fatal(err)
					}
					lines, err := vt.Lines(false, true, 0, 14)
					if err != nil {
						t.Fatal(err)
					}
					return strings.Contains(strings.Join(lines, "\n"), text) == present
				})
			}
			visible("READY", true)
			before := opened.Load()
			for index, key := range []string{"\x0f", "\x1b[27u", "\x0f"} {
				if err := s.SendInput([]byte("b")); err != nil {
					t.Fatal(err)
				}
				visible(fmt.Sprintf("https://example.org/request-%d", index+1), true)
				visible("Ctrl+O Open", true)
				if _, err := io.WriteString(pw, key); err != nil {
					t.Fatal(err)
				}
				wait("private browser reply", func() bool {
					var reply struct{ ID, Error string }
					data, _ := os.ReadFile(path + ".reply")
					return json.Unmarshal(data, &reply) == nil && reply.ID == fmt.Sprintf("%016d", index+1) &&
						(reply.Error != "") == (index == 1)
				})
				visible("Open link in your browser?", false)
			}
			for _, disconnect := range []bool{false, true} {
				_ = s.SendInput([]byte("b"))
				visible("Open link in your browser?", true)
				if disconnect {
					s.Close()
				} else {
					_ = s.SendInput([]byte("c"))
				}
				visible("Open link in your browser?", false)
			}
			if got := opened.Load() - before; got != 2 {
				t.Fatalf("browser opens = %d, want exactly the two approved requests", got)
			}
		})
	}
}

func TestImagePasteKeepsOrderedKeysAndConsumesOnlyImageGesture(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	old := readHostImage
	t.Cleanup(func() { readHostImage = old })
	for _, tc := range []struct {
		name            string
		present, legacy bool
		filePath        string
	}{
		{"PNG", true, false, ""}, {"text fallback", false, false, ""}, {"legacy PNG", true, true, ""}, {"legacy text", false, true, ""},
		{"workdir file", true, false, "/home/repo/already-here.png"},
		{"quoted workdir file", true, false, "/home/repo/it's an image $(id).png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := "image"
			if tc.legacy {
				mode = "legacy"
			}
			m, s, path, _ := hostIntegrationView(t, mode)
			reading, release := make(chan struct{}), make(chan struct{})
			readHostImage = func(ctx context.Context) (hostintegration.ClipboardImage, error) {
				close(reading)
				select {
				case <-release:
				case <-ctx.Done():
					return hostintegration.ClipboardImage{}, ctx.Err()
				}
				if tc.present {
					return hostintegration.ClipboardImage{Data: pngData.Bytes(), Path: tc.filePath}, nil
				}
				return hostintegration.ClipboardImage{}, hostintegration.ErrNoImage
			}
			key := tea.Key{Code: 'v', Mod: tea.ModCtrl}
			m.sendKey(s, key, false)
			<-reading
			repeated := key
			repeated.IsRepeat = true
			m.sendKey(s, repeated, false)
			_, _ = m.Update(tea.KeyReleaseMsg(key))
			if err := s.SendInput([]byte("laterEND")); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := s.Wait(4 * time.Second); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "\x1b[118;5u\x1b[118;5:2u\x1b[118;5:3ulaterEND"
			if tc.legacy {
				want = "\x16\x16laterEND"
			}
			if tc.present {
				imagePath := testImagePath
				if tc.filePath != "" {
					imagePath = hostintegration.ImagePathText("/workdir" + tc.filePath)
					if _, err := os.Stat(path + ".png"); !os.IsNotExist(err) {
						t.Fatal("reuse uploaded image bytes")
					}
				} else {
					data, err := os.ReadFile(path + ".png")
					if err != nil || !bytes.Equal(data, pngData.Bytes()) {
						t.Fatalf("image=%v", err)
					}
				}
				want = "\x1b[200~" + imagePath + "\x1b[201~laterEND"
			}
			if string(got) != want {
				t.Fatalf("input=%q want=%q", got, want)
			}
		})
	}
}

func TestBrowserAskOpensPromptAndRestoresFocus(t *testing.T) {
	m, s, path, requests := hostIntegrationView(t, "browser")
	m.cfg.Seams.Browser = "ask"
	old := openHostURL
	t.Cleanup(func() { openHostURL = old })
	opened := 0
	openHostURL = func(_ context.Context, url string) error {
		opened++
		if url != "https://example.org/approve-me" {
			t.Fatalf("unexpected URL %q", url)
		}
		return nil
	}
	var request browserRequestMsg
	select {
	case request = <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("no browser request")
	}
	// Deliver the callback as the actual frontend does. The watcher is omitted
	// here so the test controls completion and cannot leave a blocked command.
	_ = m.browserRequest(request)
	if !m.focusPalette || opened != 0 {
		t.Fatal("ask did not open the palette or launched without confirmation")
	}
	if view := m.View(); !strings.Contains(view.Content, "https://example.org/approve-me") || !strings.Contains(view.Content, "Ctrl+O") || !strings.Contains(view.Content, "Open link in your browser?") || view.Cursor != nil {
		t.Fatalf("prompt lost URL/choice: %q cursor=%+v", view.Content, view.Cursor)
	}
	m.pal.Insert("box ls")
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.PasteMsg{Content: "not approval"},
		tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: 7},
	} {
		_, _ = m.Update(msg)
	}
	if line, _ := m.pal.Line(); line != "box ls" || !m.focusPalette || opened != 0 {
		t.Fatalf("dialog input changed underlying state: line=%q palette=%t opened=%d", line, m.focusPalette, opened)
	}
	longRequest := browserPrompt(request)
	longRequest.request.URL += "?query=" + strings.Repeat("x", 1000)
	m.browserPrompts[s] = longRequest
	lines := m.browserPopup(5)
	if len(lines) != 5 || !strings.Contains(lines[3], "Ctrl+O Open     Esc Cancel") || !strings.HasSuffix(strings.TrimSuffix(ansi.Strip(lines[2]), " │"), "…") {
		t.Fatalf("long URL hid choices or truncation: %q", lines)
	}
	m.browserPrompts[s] = browserPrompt(request)
	cmd, handled := m.browserKey(tea.Key{Code: 'o', Mod: tea.ModCtrl})
	if !handled || cmd == nil {
		t.Fatal("confirmation unavailable")
	}
	msg := cmd().(browserFinishedMsg)
	if msg.err != nil || opened != 1 || m.focusPalette {
		t.Fatalf("open=%d palette=%t %v", opened, m.focusPalette, msg.err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path + ".reply"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("browser response not received")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.SendInput([]byte("END")); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	reply, err := os.ReadFile(path + ".reply")
	if err != nil || !bytes.Contains(reply, []byte(request.request.ID)) {
		t.Fatalf("browser result=%q %v", reply, err)
	}
	select {
	case <-requests:
		t.Fatal("stdout forged a host request")
	default:
	}
	// A retained prompt from an ended view must never launch a browser.
	msg = m.answerBrowser(browserPrompt(request), true)().(browserFinishedMsg)
	if msg.err == nil || opened != 1 {
		t.Fatal("expired browser request was executed")
	}
}

func TestBrowserDefaultOpensWithoutPrompt(t *testing.T) {
	m, s, _, requests := hostIntegrationView(t, "browser")
	old := openHostURL
	t.Cleanup(func() { openHostURL = old })
	opened := 0
	openHostURL = func(_ context.Context, url string) error {
		opened++
		if url != "https://example.org/approve-me" {
			t.Fatalf("unexpected URL %q", url)
		}
		return nil
	}
	select {
	case request := <-requests:
		cmd := m.browserRequest(request)
		if cmd == nil || m.focusPalette || m.currentBrowserPrompt() != nil {
			t.Fatal("default policy showed an approval prompt or changed focus")
		}
		if msg := cmd().(browserFinishedMsg); msg.err != nil || opened != 1 {
			t.Fatalf("default browser action: opened=%d err=%v", opened, msg.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no browser request")
	}
	if err := s.SendInput([]byte("END")); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(3 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserDialogRestoresDraftOnCancelAndExpiry(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		for _, expire := range []bool{false, true} {
			t.Run(fmt.Sprintf("standalone=%t/expire=%t", standalone, expire), func(t *testing.T) {
				m, s, _, requests := hostIntegrationView(t, "browser")
				m.standalone = standalone
				m.focusPalette = !standalone
				m.cfg.Seams.Browser = "ask"
				m.cfg.PaletteBg = "#262626"
				m.pal.Insert("box inspect unfinished")
				var request browserRequestMsg
				select {
				case request = <-requests:
				case <-time.After(3 * time.Second):
					t.Fatal("no browser request")
				}
				_ = m.browserRequest(request)
				if view := m.View(); !strings.Contains(view.Content, "Open link in your browser?") || !strings.Contains(view.Content, request.request.URL) || view.Cursor != nil {
					t.Fatalf("dialog not visible: %q", view.Content)
				} else {
					assertBrowserPopupSurface(t, m, view.Content)
				}
				if expire {
					_, _ = m.Update(browserFinishedMsg{surface: s, id: request.request.ID})
				} else {
					cmd, handled := m.browserKey(tea.Key{Code: tea.KeyEscape})
					if !handled || cmd == nil {
						t.Fatal("Escape did not cancel")
					}
					_ = cmd()
				}
				if line, _ := m.pal.Line(); line != "box inspect unfinished" || m.focusPalette != !standalone || m.browserDialog != nil {
					t.Fatalf("dialog did not restore prior view/draft: %q palette=%t", line, m.focusPalette)
				}
				if err := s.SendInput([]byte("END")); err != nil {
					t.Fatal(err)
				}
				if err := s.Wait(3 * time.Second); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestBrowserPolicyOptOut(t *testing.T) {
	m := &model{cfg: &config.TUI{Seams: config.TUISeams{Browser: "off"}}}
	if m.browserHandler() != nil {
		t.Fatal("disabled host advertised browser handling")
	}
	if err := hostintegration.ValidateURL("file:///tmp/no"); err == nil {
		t.Fatal("file opener exposed")
	}
}

func TestBrowserDialogQueuesBackgroundRequestsOverBusyConsole(t *testing.T) {
	m := newTestModel(t, 120, 14)
	m.cfg.Seams.Browser = "ask"
	a := frameTestTab(t, m)
	b := frameTestTab(t, m)
	m.tabs.Select(1)
	selected := m.tabs.Current()
	m.focusPalette = true
	m.pal.Insert("box ls")
	if started, err := m.console.Run(nil, "$ waiting", "sh", "-c", "read answer"); err != nil || !started {
		t.Fatalf("start console: %t %v", started, err)
	}
	done := make(chan struct{})
	for _, request := range []browserRequestMsg{
		{surface: a, request: session.BrowserRequest{ID: "first", URL: "https://example.org/first"}, done: done},
		{surface: b, request: session.BrowserRequest{ID: "second", URL: "https://example.org/second"}, done: done},
	} {
		_ = m.browserRequest(request)
	}
	if v := m.View(); !strings.Contains(v.Content, "https://example.org/first") || strings.Contains(v.Content, "https://example.org/second") || v.Cursor != nil {
		t.Fatalf("busy console hid first prompt: %q", v.Content)
	}
	if m.tabs.Current() != selected || !m.console.Busy() {
		t.Fatal("background prompt switched tabs or stopped the command")
	}
	m.finishBrowserPrompt(a, "first")
	if v := m.View(); !strings.Contains(v.Content, "https://example.org/second") {
		t.Fatalf("queued prompt not displayed: %q", v.Content)
	}
	if cmd, handled := m.browserKey(tea.Key{Code: 'o', Mod: tea.ModCtrl, IsRepeat: true}); !handled || cmd != nil {
		t.Fatal("held confirmation key accepted another request")
	}
	m.finishBrowserPrompt(b, "second")
	if line, _ := m.pal.Line(); line != "box ls" || !m.focusPalette || m.browserDialog != nil || !m.console.Busy() {
		t.Fatalf("original palette/command not restored: line=%q focus=%t", line, m.focusPalette)
	}
}

// Adjacent writes through the real host reader: ESC+i denotes Alt+i, whereas
// an explicit Escape report exits the palette and preserves the following i.
func TestBrowserSmokePaletteEscapePreservesFollowingInput(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		palette           bool
	}{
		{"legacy Alt+i", "\x1bi", "", true},
		{"explicit Escape then i", "\x1b[27ui", "i", false},
	} {
		for _, fragmented := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fragmented=%t", tc.name, fragmented), func(t *testing.T) {
				m := newTestModel(t, 80, 24)
				_, capture := compatibilityCapture(t, m, "")
				m.focusPalette = true
				runCompatibilityInput(t, m, tc.input, fragmented)
				if got := capture(); got != tc.want || m.focusPalette != tc.palette {
					t.Fatalf("input %q: child=%q palette=%t; want %q, %t", tc.input, got, m.focusPalette, tc.want, tc.palette)
				}
			})
		}
	}
}

// Terminal paste is literal text even when it names an existing host PNG and
// the managed view supports image transfer. Exercise the actual host reader,
// private view and child PTY; no clipboard read or path translation is allowed.
func TestHostPasteRemainsLiteralWithImageIntegration(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	hostPath := filepath.Join(t.TempDir(), "image with spaces.png")
	if err := os.WriteFile(hostPath, pngData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	old := readHostImage
	t.Cleanup(func() { readHostImage = old })
	readHostImage = func(context.Context) (hostintegration.ClipboardImage, error) {
		reads.Add(1)
		return hostintegration.ClipboardImage{Data: pngData.Bytes(), Path: hostPath}, nil
	}
	for _, tc := range []struct{ name, input string }{
		{"host PNG path", "\x1b[200~" + hostPath + "\x1b[201~"},
		{"multiline text", "\x1b[200~inspect this image:\n" + hostPath + "\x1b[201~"},
		{"Ctrl+Shift+V key", "\x1b[118;7u\x1b[118;7:3u"},
	} {
		for _, fragmented := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fragmented=%t", tc.name, fragmented), func(t *testing.T) {
				m, s, capture, _ := hostIntegrationView(t, "image")
				runCompatibilityInput(t, m, "\x1b[?31u"+tc.input, fragmented)
				if err := s.SendInput([]byte("END")); err != nil {
					t.Fatal(err)
				}
				if err := s.Wait(4 * time.Second); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(capture)
				if err != nil || string(got) != tc.input+"END" {
					t.Fatalf("literal input = %q (%v), want %q", got, err, tc.input+"END")
				}
				if reads.Load() != 0 {
					t.Fatal("ordinary paste invoked the host image reader")
				}
				if _, err := os.Stat(capture + ".png"); !os.IsNotExist(err) {
					t.Fatal("ordinary paste uploaded an image")
				}
			})
		}
	}
}

func TestClipboardSwitchDisablesHostImageReads(t *testing.T) {
	m, surface, _, _ := hostIntegrationView(t, "legacy")
	disabled := false
	m.cfg.Seams.Clipboard = &disabled
	old := readHostImage
	t.Cleanup(func() { readHostImage = old })
	readHostImage = func(context.Context) (hostintegration.ClipboardImage, error) {
		t.Fatal("disabled clipboard was read")
		return hostintegration.ClipboardImage{}, nil
	}
	if target := m.imageKey(surface, tea.Key{Code: 'v', Mod: tea.ModCtrl}, false); target != surface {
		t.Fatal("disabled clipboard started an image paste")
	}
}
