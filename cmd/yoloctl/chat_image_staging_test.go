package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func chatStageReply(input chatImageRequest, extra map[string]any) []byte {
	reply := map[string]any{"id": "616fd98a-70e3-470a-b182-bd6e44f99e83", "bytes": input.Bytes, "sha256": input.SHA256, "staged": true}
	for k, v := range extra {
		reply[k] = v
	}
	raw, _ := json.Marshal(reply)
	return raw
}

func TestChatImageStagingNeedsTheFeatureAndTheStagingFlag(t *testing.T) {
	p, input, room := chatImageFixture(t)
	input.Stage = true
	gates := 0
	ctx := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error { gates++; return nil })
	old := &imageTestConnection{run: func([]string, *os.File, io.Writer, io.Writer) error {
		t.Fatal("a client without message-attachments must not run")
		return nil
	}}
	if _, err := executeChatImageUpload(ctx, "request1", input, p, room, old, func() error { return nil }); err == nil || old.uploads != 0 || gates != 0 {
		t.Fatal("staging needs the feature before any effect", err, old.uploads, gates)
	}
	conn := &imageTestConnection{features: `"artifact-cli","artifact-metadata","` + chatstream.AttachmentsFeature + `"`,
		run: func(argv []string, in *os.File, out, _ io.Writer) error {
			want := []string{"/usr/local/bin/airc", "artifact", "upload", "--config", managedchat.HumanMount + "/client.toml", "--channel", "#space", "--name", input.filename(), "--stdin", "--stage"}
			if !reflect.DeepEqual(argv, want) {
				t.Fatal(argv)
			}
			_, _ = io.ReadAll(in)
			_, err := out.Write(chatStageReply(input, nil))
			return err
		}}
	result, err := executeChatImageUpload(ctx, "request1", input, p, room, conn, func() error { return nil })
	if err != nil || !result.Staged || result.MsgID != "" || result.ID == "" || gates != 1 {
		t.Fatal(result, err, gates)
	}
	if _, err := decodeChatImageResult(mustJSON(t, result), input, "request1"); err != nil {
		t.Fatal("the host accepts a staged result for a staging request", err)
	}
	announced := input
	announced.Stage = false
	if _, err := decodeChatImageResult(mustJSON(t, result), announced, "request1"); err == nil {
		t.Fatal("a staged result never answers an ordinary upload")
	}
}

func TestChatImageStagingRepliesThatAreNotStagesAreUncertain(t *testing.T) {
	p, input, room := chatImageFixture(t)
	input.Stage = true
	for name, reply := range map[string][]byte{
		"announced":   chatUploadReply(input),
		"not staged":  chatStageReply(input, map[string]any{"staged": false}),
		"with msgid":  chatStageReply(input, map[string]any{"msgid": "66eb4b77-cfbd-4b90-aec6-538014c4108d"}),
		"other bytes": chatStageReply(input, map[string]any{"bytes": input.Bytes + 1}),
	} {
		conn := &imageTestConnection{features: `"artifact-cli","artifact-metadata","` + chatstream.AttachmentsFeature + `"`,
			run: func(_ []string, in *os.File, out, _ io.Writer) error {
				_, _ = io.ReadAll(in)
				_, err := out.Write(reply)
				return err
			}}
		ctx := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error { return nil })
		_, err := executeChatImageUpload(ctx, "request1", input, p, room, conn, func() error { return nil })
		var uncertain *controller.EffectsUncertain
		if !errors.As(err, &uncertain) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestStreamHeaderAdvertisesAttachmentsOnlyFromTheProbe(t *testing.T) {
	for probe, want := range map[string]bool{
		`{"protocol":1,"features":["client-unix","message-attachments"]}`: true,
		`{"protocol":1,"features":["client-unix"]}`:                       false,
		`{"protocol":2,"features":["message-attachments"]}`:               false,
		`not json`: false,
	} {
		if got := managedChatAttachments([]byte(probe)); got != want {
			t.Errorf("%s: %v", probe, got)
		}
	}
}

func TestChatViewCarriesAttachmentsAndRefusals(t *testing.T) {
	connected := chatViewEvent(chatstream.Event{Kind: "connection", Connected: true, Attachments: true}).(chatui.ConnectionMsg)
	if !connected.Attachments {
		t.Fatal("the view learns that the connection sends one message")
	}
	refusal := &chatstream.AttachmentRefusal{Reason: "consumed", Index: 1, MessageID: "m1"}
	d := chatViewEvent(chatstream.Event{Kind: "delivery", ID: "x", State: "rejected", Refusal: refusal}).(chatui.DeliveryMsg)
	if d.Refusal == nil || *d.Refusal != (chatui.AttachmentRefusal{Reason: "consumed", Index: 1, MessageID: "m1"}) {
		t.Fatal(d)
	}
	m := chatViewEvent(chatstream.Event{Kind: "message", Message: chatstream.Message{ID: "p", Text: "[Image #1]",
		Attachments: []chatstream.Attachment{{Artifact: chatstream.Artifact{ID: "a", Name: "n.png", SHA256: "s", Bytes: 3}, At: 0}}}}).(chatui.IncomingMsg)
	if len(m.Message.Attachments) != 1 || m.Message.Attachments[0].ID != "a" || m.Message.Attachments[0].At != 0 {
		t.Fatal(m)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
