package main

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func readFixture(t *testing.T) ([]byte, chatArtifactReadRequest, *managedchat.Room) {
	t.Helper()
	p, img, room := chatImageFixture(t)
	return p, chatArtifactReadRequest{RoomID: img.RoomID, DiskID: img.DiskID, HumanID: img.HumanID, ChannelID: img.ChannelID,
		Channel: img.Channel, MessageID: "66eb4b77-cfbd-4b90-aec6-538014c4108d", Artifact: relayArtifact,
		Bytes: img.Bytes, SHA256: img.SHA256}, room
}

// localRelay runs what the controller would run in the chat runtime, here
// with a fake AIRC client: argv must be the fixed relay invocation.
func localRelay(t *testing.T, fakeAirc string, runs *int) func([]string, *os.File, io.Writer, io.Writer) error {
	return func(argv []string, in *os.File, out, notice io.Writer) error {
		*runs++
		if len(argv) != 7 || argv[0] != "python3" || argv[1] != "-c" || argv[2] != chatArtifactRelay ||
			argv[3] != managedchat.HumanMount+"/client.toml" || argv[4] != relayArtifact {
			t.Fatalf("unexpected relay argv %q", argv[3:])
		}
		cmd := exec.Command("python3", append([]string{"-c", relayUnderTest(t, fakeAirc, "5.0")}, argv[3:]...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, notice
		return cmd.Run()
	}
}

func TestChatArtifactReadIsATypedHostOnlyReadOnlyRequest(t *testing.T) {
	_, input, _ := readFixture(t)
	request := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "chat.artifact.read", Space: "work", Dash: -1, Payload: chatArtifactReadPayload(input)}
	if decoded, err := decodeChatArtifactReadRequest(request); err != nil || decoded != input {
		t.Fatal(decoded, err)
	}
	op := controllerOperations[request.Method]
	if op.Mutates || op.EffectGated || op.BoxAllowed || operatorChatMethod(request.Method) {
		t.Fatal("artifact read must be a host-only read", op)
	}
	for name, edit := range map[string]func(*controller.Request){
		"args":     func(r *controller.Request) { r.Args = []string{"x"} },
		"terminal": func(r *controller.Request) { r.Terminal = true },
		"payload":  func(r *controller.Request) { r.Payload = []byte(`{"artifact":"https://x"}`) },
		"size":     func(r *controller.Request) { r.Payload = []byte(strings.Repeat(" ", 3000)) },
	} {
		bad := request
		edit(&bad)
		if _, err := decodeChatArtifactReadRequest(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestChatArtifactReadRunsTheRelayOnceAndVerifiesItsFrame(t *testing.T) {
	data, input, room := readFixture(t)
	runs := 0
	conn := &imageTestConnection{features: `"artifact-cli"`, run: localRelay(t, fakeDownload(string(data), ""), &runs)}
	got, err := executeChatArtifactRead(t.Context(), input, room, conn, func() error { return nil })
	if err != nil || string(got) != string(data) || runs != 1 {
		t.Fatalf("read %d bytes, runs %d, err %v", len(got), runs, err)
	}
	// The relay's own failure reason reaches the caller.
	runs = 0
	conn.run = localRelay(t, fakeDownload("other", ""), &runs)
	if _, err := executeChatArtifactRead(t.Context(), input, room, conn, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err %v", err)
	}
}

func TestChatArtifactReadRefusesBeforeRunningAnything(t *testing.T) {
	_, input, room := readFixture(t)
	runs := 0
	conn := &imageTestConnection{features: `"artifact-cli"`, run: func([]string, *os.File, io.Writer, io.Writer) error { runs++; return nil }}
	changed := *room
	changed.Human.ID = managedchat.NewID()
	if _, err := executeChatArtifactRead(t.Context(), input, &changed, conn, func() error { return nil }); err == nil {
		t.Fatal("changed human accepted")
	}
	if _, err := executeChatArtifactRead(t.Context(), input, room, conn, func() error { return io.ErrUnexpectedEOF }); err == nil {
		t.Fatal("failed authorization accepted")
	}
	conn.features = `"artifact-metadata"`
	if _, err := executeChatArtifactRead(t.Context(), input, room, conn, func() error { return nil }); err == nil {
		t.Fatal("client without artifact-cli accepted")
	}
	if runs != 0 {
		t.Fatalf("relay ran %d times", runs)
	}
}

func TestChatArtifactReadRejectsMalformedFrames(t *testing.T) {
	data, input, room := readFixture(t)
	frame := func(n uint32, body []byte) []byte {
		b := make([]byte, 4, 4+len(body))
		binary.BigEndian.PutUint32(b, n)
		return append(b, body...)
	}
	evil := append([]byte(nil), data...)
	evil[0] ^= 1
	for name, raw := range map[string][]byte{
		"short":       data[:2],
		"wrong size":  frame(uint32(len(data))+1, data),
		"trailing":    append(frame(uint32(len(data)), data), 'x'),
		"wrong bytes": frame(uint32(len(data)), evil),
	} {
		conn := &imageTestConnection{features: `"artifact-cli"`, run: func(_ []string, _ *os.File, out, _ io.Writer) error {
			_, err := out.Write(raw)
			if name == "trailing" && err != nil {
				return err // the bounded writer refuses the extra byte
			}
			return nil
		}}
		if _, err := executeChatArtifactRead(t.Context(), input, room, conn, func() error { return nil }); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestChatArtifactReadCancellationStopsTheRelay(t *testing.T) {
	_, input, room := readFixture(t)
	runs := 0
	conn := &imageTestConnection{features: `"artifact-cli"`, run: localRelay(t, "sleep 30\n", &runs)}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	if _, err := executeChatArtifactRead(ctx, input, room, conn, func() error { return nil }); err == nil {
		t.Fatal("canceled read succeeded")
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("cancel took %s", took)
	}
}
