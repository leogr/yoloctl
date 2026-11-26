package tui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/tui/term"
)

// The sweep's rule: a single wait that nothing else ends stops at 2 minutes.
func TestHostFailSafeIsTwoMinutes(t *testing.T) {
	if hostFailSafe != 2*time.Minute {
		t.Fatalf("fail-safe %s", hostFailSafe)
	}
}

func withHostFailSafe(t *testing.T, d time.Duration) {
	t.Helper()
	old := hostFailSafe
	hostFailSafe = d
	t.Cleanup(func() { hostFailSafe = old })
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// waitInputError waits for the pane's next input notice.
func waitInputError(t *testing.T, s *term.Session) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := s.TakeInputError(); err != nil {
			return err.Error()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no input notice")
	return ""
}

// Ctrl+C on a pane whose image paste waits (keys typed meanwhile queue behind
// it) stops the paste and is used up by it; the next Ctrl+C reaches the program.
func TestCtrlCStopsAPendingPasteAndIsUsedUp(t *testing.T) {
	old := readHostImage
	t.Cleanup(func() { readHostImage = old })
	for _, mode := range []string{"image", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			m, s, path, _ := hostIntegrationView(t, mode)
			reading := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			readHostImage = func(ctx context.Context) (hostintegration.ClipboardImage, error) {
				close(reading)
				<-ctx.Done()
				close(canceled)
				<-release // the tool takes time to unwind and reports no image
				return hostintegration.ClipboardImage{}, hostintegration.ErrNoImage
			}
			paste := tea.Key{Code: 'v', Mod: tea.ModCtrl}
			m.sendKey(s, paste, false)
			_, _ = m.Update(tea.KeyReleaseMsg(paste))
			select {
			case <-reading:
			case <-time.After(5 * time.Second):
				t.Fatal("clipboard read did not begin")
			}
			if err := s.SendInput([]byte("x")); err != nil {
				t.Fatal(err)
			}
			interrupt := tea.Key{Code: 'c', Mod: tea.ModCtrl}
			_, _ = m.Update(tea.KeyPressMsg(interrupt))
			_, _ = m.Update(tea.KeyReleaseMsg(interrupt))
			select {
			case <-canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("first interrupt did not cancel the clipboard read")
			}
			_, _ = m.Update(tea.KeyPressMsg(interrupt))
			_, _ = m.Update(tea.KeyReleaseMsg(interrupt))
			close(release)
			if got := waitInputError(t, s); got != "image paste: canceled" {
				t.Fatalf("notice %q", got)
			}
			if len(m.pendingPastes(s)) != 0 {
				t.Fatal("a stopped paste is still pending")
			}
			if err := s.SendInput([]byte("END")); err != nil {
				t.Fatal(err)
			}
			if err := s.Wait(4 * time.Second); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "x\x1b[99;5u\x1b[99;5:3uEND"
			if mode == "legacy" {
				want = "x\x03END"
			}
			if string(got) != want {
				t.Fatalf("input=%q want=%q (only the second Ctrl+C reaches the program)", got, want)
			}
		})
	}
}

func TestCanceledQueuedPasteDoesNotReadClipboard(t *testing.T) {
	old := readHostImage
	t.Cleanup(func() { readHostImage = old })
	read := make(chan struct{}, 1)
	readHostImage = func(context.Context) (hostintegration.ClipboardImage, error) {
		read <- struct{}{}
		return hostintegration.ClipboardImage{}, hostintegration.ErrNoImage
	}

	m, s, path, _ := hostIntegrationView(t, "legacy")
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if err := s.PrepareInput(func(ctx context.Context) ([]byte, error) {
		select {
		case <-release:
			return []byte("x"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	m.sendKey(s, tea.Key{Code: 'v', Mod: tea.ModCtrl}, false)
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	close(release)
	if got := waitInputError(t, s); got != "image paste: canceled" {
		t.Fatalf("notice %q", got)
	}
	if err := s.SendInput([]byte("END")); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(4 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "xEND" {
		t.Fatalf("queued paste changed input: %q, %v", got, err)
	}
	select {
	case <-read:
		t.Fatal("a paste canceled in the queue read the clipboard")
	default:
	}
}

func TestCanceledPasteCannotCommitAnUploadPath(t *testing.T) {
	k := newImagePasteKey(nil)
	if !k.cancel() || k.cancel() {
		t.Fatal("only the first interrupt may cancel a paste")
	}
	path, err := k.finish([]byte(testImagePath), nil)
	if path != nil || err == nil || !k.consumed {
		t.Fatalf("canceled paste committed a late path: %q, %v", path, err)
	}
}

// Without a pending paste Ctrl+C goes to the program as before.
func TestCtrlCWithoutAPasteReachesTheProgram(t *testing.T) {
	m, s, path, _ := hostIntegrationView(t, "legacy")
	interrupt := tea.Key{Code: 'c', Mod: tea.ModCtrl}
	_, _ = m.Update(tea.KeyPressMsg(interrupt))
	if err := s.SendInput([]byte("END")); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(4 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "\x03END" {
		t.Fatalf("input=%q", got)
	}
}

// The upload's reply had 20 s; it now waits up to the fail-safe. A paste that
// ends without its reply closes only this view's attachment, as it did at 20 s
// (a partial upload cannot be resumed): by the fail-safe or by Ctrl+C.
func TestPasteUploadWaitsUntilTheFailSafe(t *testing.T) {
	old := readHostImage
	t.Cleanup(func() { readHostImage = old })
	data := testPNG(t)
	readHostImage = func(context.Context) (hostintegration.ClipboardImage, error) {
		return hostintegration.ClipboardImage{Data: data}, nil
	}
	t.Run("slow reply", func(t *testing.T) {
		withHostFailSafe(t, 10*time.Second)
		t.Setenv("YOLOCTL_TEST_UPLOAD_DELAY", "1s")
		m, s, path, _ := hostIntegrationView(t, "legacy")
		m.sendKey(s, tea.Key{Code: 'v', Mod: tea.ModCtrl}, false)
		if err := s.SendInput([]byte("END")); err != nil {
			t.Fatal(err)
		}
		if err := s.Wait(6 * time.Second); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != "\x1b[200~"+testImagePath+"\x1b[201~END" {
			t.Fatalf("input=%q", got)
		}
	})
	for _, tc := range []struct {
		name     string
		failSafe time.Duration
		ctrlC    bool
		want     string
	}{
		{"no reply", 300 * time.Millisecond, false, "image paste: context deadline exceeded"},
		{"Ctrl+C", time.Minute, true, "image paste: canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHostFailSafe(t, tc.failSafe)
			t.Setenv("YOLOCTL_TEST_UPLOAD_DELAY", "never")
			m, s, path, _ := hostIntegrationView(t, "legacy")
			m.sendKey(s, tea.Key{Code: 'v', Mod: tea.ModCtrl}, false)
			if tc.ctrlC {
				// The holder has the whole image and withholds its reply.
				deadline := time.Now().Add(5 * time.Second)
				for _, err := os.Stat(path + ".png"); err != nil; _, err = os.Stat(path + ".png") {
					if time.Now().After(deadline) {
						t.Fatal("upload did not reach the holder")
					}
					time.Sleep(5 * time.Millisecond)
				}
				_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
			}
			if got := waitInputError(t, s); got != tc.want {
				t.Fatalf("notice %q, want %q", got, tc.want)
			}
			if err := s.Wait(6 * time.Second); err == nil {
				t.Fatal("the attachment survived an unfinished upload")
			}
		})
	}
}

// The chat image job had 30 s for its fetch and decode; it now has the fail-safe.
func TestImageJobHasTheFailSafe(t *testing.T) {
	deadline := make(chan time.Duration, 1)
	m, s := supportedBroker(t, func(ctx context.Context, _ string, _ chatimage.Frame) ([]byte, error) {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now()
		}
		deadline <- time.Until(d)
		return nil, context.Canceled
	})
	m.imageHandler()(s, previewFrame(7))
	_ = m.syncImages()
	select {
	case left := <-deadline:
		if left < hostFailSafe-10*time.Second || left > hostFailSafe {
			t.Fatalf("image job deadline in %v, want the %v fail-safe", left, hostFailSafe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job did not start")
	}
}

// A host terminal that answers the encoding change late is waited for up to
// the fail-safe (it was 2 s), and its answer is still taken.
func TestMouseEncodingAnswerIsAwaitedUntilTheFailSafe(t *testing.T) {
	withHostFailSafe(t, 300*time.Millisecond)
	m := newTestModel(t, 80, 24)
	_, _ = mouseCapture(t, m, "\x1b[?1000h\x1b[?1016h")
	_, _ = m.Update(uv.CellSizeEvent{Width: 10, Height: 20})
	_, _ = m.Update(m.initHostMouse()())
	_, cmd := m.Update(tea.ModeReportMsg{Mode: ansi.ModeMouseExtSgrPixel, Value: ansi.ModeReset})
	if cmd == nil || !m.hostMouseEncoding.negotiating {
		t.Fatal("no negotiation")
	}
	start := time.Now()
	var timeout tea.Msg
	for _, c := range sequenceCmds(cmd) {
		if msg := c(); reflect.TypeOf(msg) == reflect.TypeOf(mouseEncodingTimeout(0)) {
			timeout = msg
		}
	}
	if took := time.Since(start); timeout == nil || took < 250*time.Millisecond || took > 1500*time.Millisecond {
		t.Fatalf("encoding wait ended after %v (%v), want the injected fail-safe", took, timeout)
	}
	_, _ = m.Update(tea.ModeReportMsg{Mode: ansi.ModeMouseExtSgrPixel, Value: ansi.ModeSet})
	if !m.hostMouseEncoding.pixels || m.hostMouseEncoding.negotiating || m.hostMouseEncoding.failed {
		t.Fatal("an answer within the fail-safe was not taken")
	}
	_, _ = m.Update(timeout)
	if m.hostMouseEncoding.failed {
		t.Fatal("a stale fail-safe ended a confirmed negotiation")
	}
}

// sequenceCmds returns the commands a tea.Sequence or tea.Batch carries.
func sequenceCmds(cmd tea.Cmd) []tea.Cmd {
	msg := cmd()
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeOf(tea.Cmd(nil)) {
		return []tea.Cmd{func() tea.Msg { return msg }}
	}
	var out []tea.Cmd
	for i := 0; i < v.Len(); i++ {
		if c := v.Index(i).Interface().(tea.Cmd); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// The TUI adds no bound of its own to an approved browser open: OpenURL has
// the fail-safe, and the holder withdrawing the request still cancels it.
func TestBrowserAnswerLeavesTheBoundToTheOpener(t *testing.T) {
	old := openHostURL
	t.Cleanup(func() { openHostURL = old })
	var bounded bool
	openHostURL = func(ctx context.Context, _ string) error {
		_, bounded = ctx.Deadline()
		return nil
	}
	m, s, _, _ := hostIntegrationView(t, "legacy")
	prompt := browserPrompt{surface: s, request: session.BrowserRequest{ID: "0123456789ABCDEFG", URL: "https://example.org/"}, done: make(chan struct{})}
	if msg, ok := m.answerBrowser(prompt, true)().(browserFinishedMsg); !ok || bounded || msg.err != nil && !strings.Contains(msg.err.Error(), "browser") {
		t.Fatalf("browser answer: bounded=%v %+v", bounded, msg)
	}
}
