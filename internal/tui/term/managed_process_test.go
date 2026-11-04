package term

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
	"golang.org/x/term"
)

// This fixture speaks the public wire shape through the REAL inherited socket.
// PTY output containing plausible controls must not become authoritative state.
func TestManagedViewChild(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_VIEW_CHILD") != "1" {
		return
	}
	f, err := termio.InheritedView()
	if err != nil || f == nil {
		os.Exit(80)
	}
	defer func() { _ = f.Close() }()
	if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
		os.Exit(79)
	}
	send := func(kind byte, p []byte) {
		var header [3]byte
		header[0] = kind
		binary.BigEndian.PutUint16(header[1:], uint16(len(p)))
		if _, err := f.Write(append(header[:], p...)); err != nil {
			os.Exit(81)
		}
	}
	recv := func() (byte, []byte, error) {
		var header [3]byte
		if _, err := io.ReadFull(f, header[:]); err != nil {
			return 0, nil, err
		}
		p := make([]byte, int(binary.BigEndian.Uint16(header[1:])))
		if _, err := io.ReadFull(f, p); err != nil {
			return 0, nil, err
		}
		return header[0], p, nil
	}
	_, _ = os.Stdout.WriteString("untrusted progress\x1b]52;c;")
	if gate := os.Getenv("YOLOCTL_TEST_VIEW_GATE"); gate != "" {
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if os.Getenv("YOLOCTL_TEST_VIEW_BAD_PROTOCOL") == "1" {
		send('h', []byte{1})
		time.Sleep(time.Minute)
		os.Exit(90)
	}
	send('h', []byte{6})
	kind, _, err := recv()
	if err != nil {
		os.Exit(82)
	}
	if kind != 'v' {
		os.Exit(84)
	}
	vt, err := terminal.New(8, 40, 100, nil)
	if err != nil {
		os.Exit(85)
	}
	_, _ = vt.Write([]byte("PRIVATE_SCREEN\x1b[>3u\x1b[?2004h"))
	fullScreen := os.Getenv("YOLOCTL_TEST_VIEW_FULL_SCREEN")
	if fullScreen != "" {
		if fullScreen == "alt" {
			_, _ = vt.Write([]byte("\x1b[?1049hPRIVATE_SCREEN"))
		}
		_, _ = vt.Write([]byte("\x1b[8;1HBOTTOM"))
	}
	snapshot, err := vt.Snapshot()
	if err != nil {
		os.Exit(86)
	}
	bundle, _ := controller.ExecutableIdentity()
	meta, _ := json.Marshal(map[string]any{"Bundle": bundle, "Core": terminal.CoreIdentity(), "Session": "fixture", "Client": "client", "Sequence": 0, "SnapshotBytes": len(snapshot), "Colors": terminal.Colors{}})
	send('b', meta)
	for len(snapshot) > 0 {
		n := min(len(snapshot), 32768)
		send('s', snapshot[:n])
		snapshot = snapshot[n:]
	}
	send('S', nil)
	metadataDone := make(chan struct{})
	go func() {
		defer close(metadataDone)
		for { // geometry and color metadata use the private channel
			// The frontend closes this channel after the final status. That
			// normal EOF must not race the fixture's successful process exit.
			if _, _, err := recv(); err != nil {
				return
			}
		}
	}()
	want := "\x1b[13;2u"
	if os.Getenv("YOLOCTL_TEST_VIEW_GATE") != "" {
		want = "early-late"
	}
	input := make([]byte, len(want))
	if _, err := io.ReadFull(os.Stdin, input); err != nil || string(input) != want {
		fmt.Fprintln(os.Stderr, "unexpected child input", string(input), err)
		os.Exit(87)
	}
	data := append(make([]byte, 8), []byte("\r\nLIVE_INPUT\x1b[6n")...)
	binary.BigEndian.PutUint64(data, 1)
	send('d', data)
	_, _ = os.Stdout.WriteString("\x1bcFORGED_SCREEN\a")
	time.Sleep(20 * time.Millisecond)
	code := byte(0)
	if fullScreen != "" {
		code = 7
	}
	send('e', []byte{0, 0, 0, code})
	_ = f.Close()
	<-metadataDone
	os.Exit(int(code))
}

func TestManagedExitFitsLocalViewport(t *testing.T) {
	for _, screen := range []string{"main", "alt"} {
		t.Run(screen, func(t *testing.T) {
			s, err := Start(Options{
				Argv:        []string{os.Args[0], "-test.run=^TestManagedViewChild$"},
				Env:         append(os.Environ(), "YOLOCTL_TEST_VIEW_CHILD=1", "YOLOCTL_TEST_VIEW_FULL_SCREEN="+screen),
				TrustedView: true, Rows: 6, Cols: 60,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			awaitInput(t, "canonical screen", s.managed.active.Load)
			// The local tab was resized, but a different viewer determines the
			// larger canonical geometry. Live replicas must retain that geometry.
			if err := s.Resize(5, 52); err != nil {
				t.Fatal(err)
			}
			if f := s.Frame(); f.Rows != 8 || f.Cols != 40 {
				t.Fatalf("local resize changed live canonical geometry: %+v", f)
			}
			if err := s.SendInput([]byte("\x1b[13;2u")); err != nil {
				t.Fatal(err)
			}
			if err := s.Wait(5 * time.Second); err == nil || !strings.Contains(err.Error(), "exit status 7") {
				t.Fatalf("lost final status: %v", err)
			}
			f := s.Frame()
			// This is the same top-of-grid clipping used by the TUI. An exit
			// marker below these five rows is invisible even if stored in the core.
			visible := strings.Join(f.Lines[:min(5, len(f.Lines))], "\n")
			assertBannerClean(t, visible, "exit 7")
			if f.Rows != 5 || f.Cols != 52 || s.IsAlt() != (screen == "alt") {
				t.Fatalf("completed view has wrong geometry or screen: %+v, alt=%v", f, s.IsAlt())
			}
			// A completed tab has no remote geometry owner or PTY to resize.
			if err := s.Resize(10, 65); err != nil {
				t.Fatal(err)
			}
			if f := s.Frame(); f.Rows != 10 || f.Cols != 65 {
				t.Fatalf("completed tab ignored local resize: %+v", f)
			}
			assertBannerClean(t, s.GridText(), "exit 7")
		})
	}
}

func TestManagedPrivateDescriptorRejectsPTYForgery(t *testing.T) {
	s, err := Start(Options{Argv: []string{os.Args[0], "-test.run=^TestManagedViewChild$"}, Env: append(os.Environ(), "YOLOCTL_TEST_VIEW_CHILD=1"), TrustedView: true, Rows: 8, Cols: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	awaitInput(t, "native snapshot", func() bool { return strings.Contains(s.GridText(), "PRIVATE_SCREEN") && s.managed.active.Load() })
	if s.KeyboardFlags() != 3 {
		t.Fatal("private state did not restore keyboard mode")
	}
	if err := s.SendInput([]byte("\x1b[13;2u")); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	got := s.GridText()
	if !strings.Contains(got, "LIVE_INPUT") || strings.Contains(got, "FORGED_SCREEN") {
		t.Fatalf("managed screen=%q", got)
	}
	if s.managed.err != nil {
		t.Fatal(s.managed.err)
	}
}

func TestManagedPublicationPreservesTypeaheadOrder(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "publish")
	s, err := Start(Options{Argv: []string{os.Args[0], "-test.run=^TestManagedViewChild$"}, Env: append(os.Environ(), "YOLOCTL_TEST_VIEW_CHILD=1", "YOLOCTL_TEST_VIEW_GATE="+gate), TrustedView: true, Rows: 8, Cols: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	awaitInput(t, "setup before publication", func() bool { return strings.Contains(s.GridText(), "untrusted progress") })
	if err := s.SendInput([]byte("early-")); err != nil {
		t.Fatal(err)
	}
	awaitInput(t, "early input in PTY", func() bool { s.writer.mu.Lock(); defer s.writer.mu.Unlock(); return s.writer.count == 0 })
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitInput(t, "managed publication", s.managed.active.Load)
	if err := s.SendInput([]byte("late")); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(3 * time.Second); err != nil {
		t.Fatalf("typeahead and live input did not arrive in order: %v", err)
	}
}

func TestUnusedPrivateDescriptorDoesNotFailOrdinaryCommand(t *testing.T) {
	s, err := Start(Options{Argv: []string{"sh", "-c", "printf ordinary"}, TrustedView: true, Rows: 8, Cols: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.GridText(), "ordinary") || s.managed.active.Load() {
		t.Fatal("ordinary command acquired managed state")
	}
}

func TestPrivateHandshakeFailureDoesNotLeaveCLIHanging(t *testing.T) {
	s, err := Start(Options{Argv: []string{os.Args[0], "-test.run=^TestManagedViewChild$"}, Env: append(os.Environ(), "YOLOCTL_TEST_VIEW_CHILD=1", "YOLOCTL_TEST_VIEW_BAD_PROTOCOL=1"), TrustedView: true, Rows: 8, Cols: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("incompatible private channel left worker running")
	}
	err = s.Wait(time.Second)
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("missing actionable mismatch: %v", err)
	}
}
