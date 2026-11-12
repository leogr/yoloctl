package term

import (
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatimage"
)

// imageChild sends one preview frame (or garbage), then prints each frame the
// host sends.
const imageChild = `
import json, os, struct, sys, time
fd = int(os.environ["YOLOCTL_TUI_IMAGE_FD"])
os.set_blocking(fd, True)
f = os.fdopen(fd, "r+b", buffering=0)
if sys.argv[1] == "garbage":
    f.write(b"\x00\x00\x00\x05hello")
else:
    body = json.dumps({"v":1,"op":"preview","generation":1,"request":7,
        "room_id":"0123456789abcdef0123456789abcdef","disk_id":"0123456789abcdef0123456789abcdef",
        "human_id":"0123456789abcdef0123456789abcdef","channel_id":"0123456789abcdef0123456789abcdef","channel":"#space",
        "message_id":"01234567-89ab-cdef-0123-456789abcdef","artifact":"01234567-89ab-cdef-0123-456789abcdef",
        "bytes":1024,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","chip":"[file a.png]"}).encode()
    f.write(struct.pack(">I", len(body)) + body)
print("SENT", flush=True)
while True:
    hdr = f.read(4)
    if len(hdr) < 4:
        print("CLOSED", flush=True)
        time.sleep(30)
    n = struct.unpack(">I", hdr)[0]
    print("GOT", f.read(n).decode(), flush=True)
`

func waitText(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(s.GridText(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q in %q", want, s.GridText())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestImageChannelCarriesValidatedFramesBothWays(t *testing.T) {
	got := make(chan chatimage.Frame, 1)
	s, err := Start(Options{Rows: 10, Cols: 200, Argv: []string{"python3", "-c", imageChild, "preview"},
		OnImage: func(_ *Session, f chatimage.Frame) { got <- f }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case f := <-got:
		if f.Op != chatimage.OpPreview || f.Request != 7 || f.Bytes != 1024 {
			t.Fatalf("frame %+v", f)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no preview frame")
	}
	if err := s.SendImage(chatimage.Frame{Op: chatimage.OpCapability, Generation: 2, Supported: true, MaxCols: 60, MaxRows: 12}); err != nil {
		t.Fatal(err)
	}
	waitText(t, s, `"op":"capability"`)
	if s.SendImage(chatimage.Frame{Op: chatimage.OpPreview}) == nil {
		t.Fatal("host sent a child operation")
	}
}

func TestImageChannelClosesOnGarbageAndOnlyForImageSessions(t *testing.T) {
	called := make(chan struct{}, 1)
	s, err := Start(Options{Rows: 10, Cols: 120, Argv: []string{"python3", "-c", imageChild, "garbage"},
		OnImage: func(*Session, chatimage.Frame) { called <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitText(t, s, "CLOSED")
	select {
	case <-called:
		t.Fatal("garbage reached the handler")
	default:
	}
	// The session itself keeps running.
	if strings.Contains(s.GridText(), "Traceback") {
		t.Fatalf("child failed: %q", s.GridText())
	}
	// A session without OnImage gets no channel and no variable, even when
	// the TUI's own environment names one.
	t.Setenv(chatimage.ChannelFDEnv, "9")
	plain, err := Start(Options{Rows: 4, Cols: 120, Argv: []string{"sh", "-c", "echo VAR=${" + chatimage.ChannelFDEnv + ":-none}; sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	waitText(t, plain, "VAR=none")
	if plain.SendImage(chatimage.Frame{Op: chatimage.OpCapability, Generation: 1}) == nil {
		t.Fatal("plain session has an image channel")
	}
}
