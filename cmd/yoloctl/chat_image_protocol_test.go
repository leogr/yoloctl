package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func chatImageFixture(t *testing.T) ([]byte, chatImageRequest, *managedchat.Room) {
	t.Helper()
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	pixels.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, pixels); err != nil {
		t.Fatal(err)
	}
	room := &managedchat.Room{Schema: 1, ID: managedchat.NewID(), DiskID: managedchat.NewID(), Generation: 1, Channel: "#space", Prepared: true, RuntimeEnabled: true,
		Human: managedchat.Member{ID: managedchat.NewID(), Name: "human"}, Server: managedchat.Box{ID: managedchat.NewID(), Name: "chat"}}
	input := chatImageRequest{RoomID: room.ID, DiskID: room.DiskID, HumanID: room.Human.ID, ChannelID: managedchat.LegacyChannelID(room.ID),
		Channel: "#space", PublicationToken: managedchat.NewID(), Bytes: int64(b.Len()), SHA256: chatImageHash(b.Bytes())}
	return b.Bytes(), input, room
}

func chatUploadReply(input chatImageRequest) []byte {
	raw, _ := json.Marshal(map[string]any{"id": "616fd98a-70e3-470a-b182-bd6e44f99e83",
		"url":   "http://airc.local/v1/artifacts/616fd98a-70e3-470a-b182-bd6e44f99e83",
		"bytes": input.Bytes, "sha256": input.SHA256, "msgid": "66eb4b77-cfbd-4b90-aec6-538014c4108d"})
	return raw
}

func TestChatImageTypedHostOnlyRequest(t *testing.T) {
	_, input, _ := chatImageFixture(t)
	request := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "chat.artifact.upload", Space: "work", Dash: -1, Payload: chatImagePayload(input)}
	if decoded, err := decodeChatImageRequest(request); err != nil || decoded != input {
		t.Fatal(decoded, err)
	}
	op := controllerOperations[request.Method]
	if !op.Mutates || !op.EffectGated || op.BoxAllowed || operatorChatMethod(request.Method) {
		t.Fatal("artifact operation must be an admitted host-only mutation", op)
	}
	if err := controller.Authorize(controller.Principal{Box: "caller", Incarnation: "live"}, request, "work", controllerOperations); err == nil {
		t.Fatal("operator upload accepted")
	}
	for _, mutate := range []func(*controller.Request){
		func(r *controller.Request) { r.Args = []string{"file.png"} },
		func(r *controller.Request) { r.Flags = map[string][]string{"url": {"https://example.com"}} },
		func(r *controller.Request) { r.Terminal = true },
		func(r *controller.Request) { r.View = true },
		func(r *controller.Request) { r.Payload = append(r.Payload, []byte("{}")...) },
		func(r *controller.Request) { r.Payload = []byte(`{"filename":"/secret.png"}`) },
	} {
		changed := request
		mutate(&changed)
		if _, err := decodeChatImageRequest(changed); err == nil {
			t.Fatal("extraneous request surface accepted")
		}
	}
	for _, mutate := range []func(*chatImageRequest){
		func(r *chatImageRequest) { r.RoomID = "" },
		func(r *chatImageRequest) { r.DiskID = "other" },
		func(r *chatImageRequest) { r.ChannelID = "" },
		func(r *chatImageRequest) { r.Channel = "#Space" },
		func(r *chatImageRequest) { r.PublicationToken = "../private" },
		func(r *chatImageRequest) { r.Bytes = chatImageLimit + 1 },
		func(r *chatImageRequest) { r.SHA256 = strings.Repeat("A", 64) },
	} {
		changed := input
		mutate(&changed)
		if err := changed.validate(); err == nil {
			t.Fatal("invalid request metadata accepted", changed)
		}
	}
}

func TestChatImagePNGAndFramingBeforeEffects(t *testing.T) {
	p, input, _ := chatImageFixture(t)
	got, err := readChatImage(chatImageFrame(p), input)
	if err != nil || !bytes.Equal(got, p) {
		t.Fatal(err)
	}
	badCRC := bytes.Clone(p)
	badCRC[len(badCRC)-1] ^= 1
	hugePixels := bytes.Clone(p)
	binary.BigEndian.PutUint32(hugePixels[16:20], hostintegration.MaxImagePixels+1)
	binary.BigEndian.PutUint32(hugePixels[29:33], crc32.ChecksumIEEE(hugePixels[12:29]))
	for _, bad := range [][]byte{nil, []byte("/secret.png"), []byte("file:///secret.png"), p[:len(p)-4], badCRC, hugePixels, make([]byte, chatImageLimit+1)} {
		if validateChatImage(bad) == nil {
			t.Fatal("invalid PNG accepted", len(bad))
		}
	}
	frame, _ := io.ReadAll(chatImageFrame(p))
	for _, bad := range [][]byte{frame[:3], frame[:len(frame)-1], append(bytes.Clone(frame), 0)} {
		if _, err := readChatImage(bytes.NewReader(bad), input); err == nil {
			t.Fatal("incomplete or extra frame data accepted")
		}
	}
	oversized := bytes.Clone(frame)
	binary.BigEndian.PutUint32(oversized, chatImageLimit+1)
	reader := bytes.NewReader(oversized)
	if _, err := readChatImage(reader, input); err == nil || reader.Len() != len(p) {
		t.Fatal("oversized frame read its body", err, reader.Len())
	}
	input.SHA256 = strings.Repeat("0", 64)
	if _, err := readChatImage(bytes.NewReader(frame), input); err == nil {
		t.Fatal("mismatched hash accepted")
	}
}

func TestChatImageImmutableIdentityAndNewerGeneration(t *testing.T) {
	_, input, room := chatImageFixture(t)
	room.Generation++
	if err := input.matchesRoom(room); err != nil {
		t.Fatal("a newer generation with unchanged identity invalidated draft", err)
	}
	for _, mutate := range []func(*managedchat.Room){
		func(r *managedchat.Room) { r.ID = managedchat.NewID() },
		func(r *managedchat.Room) { r.DiskID = managedchat.NewID() },
		func(r *managedchat.Room) { r.Human.ID = managedchat.NewID() },
		func(r *managedchat.Room) { r.RuntimeEnabled = false },
		func(r *managedchat.Room) { r.Prepared = false },
		func(r *managedchat.Room) {
			r.Schema = 2
			r.Channels = []managedchat.Channel{{ID: managedchat.NewID(), Name: input.Channel, Members: []string{input.HumanID}}}
		},
	} {
		changed := *room
		mutate(&changed)
		if input.matchesRoom(&changed) == nil {
			t.Fatal("stale image identity accepted", changed)
		}
	}
}

func TestChatImageReplyValidation(t *testing.T) {
	_, input, _ := chatImageFixture(t)
	reply := chatUploadReply(input)
	good, err := decodeChatImageUploadReply(reply, input, "request1")
	if err != nil || good.PublicationToken != input.PublicationToken || good.Bytes != input.Bytes {
		t.Fatal(good, err)
	}
	for _, replacement := range []struct{ from, to string }{
		{"http://airc.local", "https://evil.example"},
		{input.SHA256, strings.Repeat("0", 64)},
		{"616fd98a-70e3-470a-b182-bd6e44f99e83", "../../secret"},
		{"66eb4b77-cfbd-4b90-aec6-538014c4108d", "missing"},
	} {
		bad := bytes.ReplaceAll(reply, []byte(replacement.from), []byte(replacement.to))
		if _, err := decodeChatImageUploadReply(bad, input, "request1"); err == nil {
			t.Fatal("forged reply accepted")
		}
	}
	for _, bad := range [][]byte{append(bytes.Clone(reply), []byte("{}")...), append(reply, bytes.Repeat([]byte(" "), chatImageReplyLimit)...), []byte(`{"id":"fake"}`)} {
		if _, err := decodeChatImageUploadReply(bad, input, "request1"); err == nil {
			t.Fatal("invalid/oversized reply accepted")
		}
	}
	raw, _ := json.Marshal(good)
	if _, err := decodeChatImageResult(raw, input, "request1"); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeChatImageResult(raw, input, "request2"); err == nil {
		t.Fatal("reply from another request accepted")
	}
	input.PublicationToken = managedchat.NewID()
	if _, err := decodeChatImageResult(raw, input, "request1"); err == nil {
		t.Fatal("reply from another publication accepted")
	}
}
