package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// chatArtifactReadTimeout bounds the controller side of one read; the relay
// keeps its own shorter deadline inside the chat runtime.
const chatArtifactReadTimeout = 20 * time.Second

// chatArtifactReadRequest names one artifact of an authenticated message by
// its validated tuple and the identities it was seen under. It carries no
// filename, URL, path or credential.
type chatArtifactReadRequest struct {
	RoomID    string `json:"room_id"`
	DiskID    string `json:"disk_id"`
	HumanID   string `json:"human_id"`
	ChannelID string `json:"channel_id"`
	Channel   string `json:"channel"`
	MessageID string `json:"message_id"`
	Artifact  string `json:"artifact"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
}

func (r chatArtifactReadRequest) validate() error {
	for _, id := range []string{r.RoomID, r.DiskID, r.HumanID, r.ChannelID} {
		if layout.ValidateIdentity(id) != nil {
			return errors.New("invalid artifact read identity")
		}
	}
	if err := managedchat.ValidateChannelName(r.Channel); err != nil {
		return err
	}
	if !chatstream.ValidArtifactID(r.MessageID) || !chatstream.ValidArtifactID(r.Artifact) ||
		!chatstream.ValidArtifactHash(r.SHA256) || r.Bytes < 1 || r.Bytes > chatimage.MaxArtifactBytes {
		return errors.New("artifact read needs a valid message, artifact, SHA256 and a size of at most 4 MiB")
	}
	return nil
}

func decodeChatArtifactReadRequest(req controller.Request) (chatArtifactReadRequest, error) {
	var input chatArtifactReadRequest
	if req.Method != "chat.artifact.read" || len(req.Args) != 0 || len(req.Flags) != 0 || req.Terminal || req.View || req.Session != nil ||
		req.Cwd != "" || req.Term != "" || req.Rows != 0 || req.Cols != 0 || len(req.Payload) > 2048 {
		return input, errors.New("artifact read accepts only typed metadata")
	}
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &input, 2048); err != nil {
		return input, errors.New("invalid artifact read metadata")
	}
	return input, input.validate()
}

// chatArtifactReadPayload is the request payload for a validated read.
func chatArtifactReadPayload(r chatArtifactReadRequest) []byte {
	raw, _ := json.Marshal(r)
	return raw
}

// matchesRoom requires the room, disk and human the message was seen under,
// a prepared and running room, and the human's current membership of that
// channel. AIRC remains authoritative for the read itself.
func (r chatArtifactReadRequest) matchesRoom(room *managedchat.Room) error {
	if room == nil || room.ID != r.RoomID || room.DiskID != r.DiskID || room.Human.ID != r.HumanID || !room.Prepared || !room.RuntimeEnabled {
		return errors.New("chat room, disk or human identity changed; reopen the chat")
	}
	for _, channel := range room.ChannelCatalog() {
		if channel.ID == r.ChannelID && channel.Name == r.Channel && slices.Contains(channel.Members, r.HumanID) {
			return nil
		}
	}
	return errors.New("chat channel identity or access changed")
}

// runControllerChatArtifactRead is the host-only read-only operation: it
// verifies the managed chat peer and the request's identities, runs the
// fixed relay once, verifies its frame against the request and writes the
// bytes to out. It never stages, uploads, joins or retries.
func runControllerChatArtifactRead(ctx context.Context, req controller.Request, out io.Writer) error {
	if !currentControllerPrincipal(ctx).Admin() {
		return errors.New("artifact read requires the human host endpoint")
	}
	input, err := decodeChatArtifactReadRequest(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, chatArtifactReadTimeout)
	defer cancel()
	store, err := controller.NativeChatStore()
	if err != nil {
		return err
	}
	conn, err := openManagedChatConnection(ctx, req.Space, store)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	data, err := executeChatArtifactRead(ctx, input, conn.Room, conn, func() error {
		current, err := store.ReadManifestSnapshot()
		if err != nil {
			return err
		}
		return input.matchesRoom(current)
	})
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

// executeChatArtifactRead is everything after the connection: identity and
// feature checks, one relay run with a cancel-only input pipe, and strict
// verification of its single frame.
func executeChatArtifactRead(ctx context.Context, input chatArtifactReadRequest, room *managedchat.Room, conn chatImageConnection, authorize func() error) ([]byte, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	if err := input.matchesRoom(room); err != nil {
		return nil, err
	}
	var probe bytes.Buffer
	if err := conn.Run(ctx, []string{"/usr/local/bin/airc", "features"}, nil, &boundedChatWriter{Out: &probe, Remaining: managedchat.ControlLimit}, &boundedChatWriter{Out: io.Discard, Remaining: chatImageReplyLimit}); err != nil {
		return nil, errors.New("could not inspect the confined artifact client")
	}
	var features managedchat.Features
	if json.Unmarshal(probe.Bytes(), &features) != nil {
		return nil, errors.New("invalid artifact client feature response")
	}
	if err := features.Require("artifact-cli"); err != nil {
		return nil, err
	}
	if err := conn.Verify(ctx); err != nil {
		return nil, err
	}
	if err := authorize(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, notice, err := runChatArtifactRelay(ctx, conn, input)
	if err != nil {
		if line := relayFailure(notice); line != "" {
			return nil, errors.New("artifact read failed: " + line)
		}
		return nil, errors.New("artifact read failed")
	}
	return verifyChatArtifactFrame(raw, input)
}

// runChatArtifactRelay runs the relay with a dedicated pipe as its input: the
// write end stays open until the run returns, and cancellation closes it,
// which the relay treats as cancel. Its output and notices are bounded.
func runChatArtifactRelay(ctx context.Context, conn chatImageConnection, input chatArtifactReadRequest) ([]byte, []byte, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = writer.Close() })
	defer stop()
	argv := []string{"python3", "-c", chatArtifactRelay, managedchat.HumanMount + "/client.toml",
		input.Artifact, strconv.FormatInt(input.Bytes, 10), input.SHA256}
	var out, notice bytes.Buffer
	err = conn.Run(ctx, argv, reader,
		&boundedChatWriter{Out: &out, Remaining: 4 + int(input.Bytes)},
		&boundedChatWriter{Out: &notice, Remaining: chatImageReplyLimit})
	return out.Bytes(), notice.Bytes(), err
}

// verifyChatArtifactFrame accepts exactly one frame of the requested size
// and hash and nothing after it.
func verifyChatArtifactFrame(raw []byte, input chatArtifactReadRequest) ([]byte, error) {
	if len(raw) < 4 || int64(binary.BigEndian.Uint32(raw)) != input.Bytes || int64(len(raw)-4) != input.Bytes {
		return nil, errors.New("artifact read returned an incomplete or oversized frame")
	}
	data := raw[4:]
	if chatImageHash(data) != input.SHA256 {
		return nil, errors.New("artifact read bytes do not match the message")
	}
	return data, nil
}

// relayFailure is the relay's one-line reason, printable and bounded.
func relayFailure(notice []byte) string {
	line, _, _ := strings.Cut(string(notice), "\n")
	line = strings.TrimPrefix(line, "artifact relay: ")
	var b strings.Builder
	for _, r := range line {
		if b.Len() >= 200 {
			break
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
