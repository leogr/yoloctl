package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/tui"
)

// chatArtifactHostTimeout bounds the host side of one preview read; the
// controller and the relay keep their own shorter bounds.
const chatArtifactHostTimeout = 25 * time.Second

func init() { tui.ReadChatArtifact = readChatArtifactForPreview }

// readChatArtifactForPreview is the TUI broker's artifact reader. It routes to
// the space's host controller (space "" resolves like the CLI) and accepts
// only exactly the requested size and hash.
func readChatArtifactForPreview(ctx context.Context, space string, f chatimage.Frame) ([]byte, error) {
	req := chatArtifactReadRequest{RoomID: f.RoomID, DiskID: f.DiskID, HumanID: f.HumanID, ChannelID: f.ChannelID,
		Channel: f.Channel, MessageID: f.MessageID, Artifact: f.Artifact, Bytes: f.Bytes, SHA256: f.SHA256}
	if err := req.validate(); err != nil {
		return nil, err
	}
	resolved, _, err := config.ResolveSpace(space, space != "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, chatArtifactHostTimeout)
	defer cancel()
	boot, err := checkControllerSpace(ctx, resolved)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = callHostController(ctx, boot, controller.Request{ID: rand.Text(), Method: "chat.artifact.read", Space: resolved, Dash: -1,
		Payload: chatArtifactReadPayload(req)}, controller.ClientIO{
		In:  strings.NewReader(""),
		Out: &boundedChatWriter{Out: &out, Remaining: int(req.Bytes)},
		Err: &boundedChatWriter{Out: io.Discard, Remaining: chatImageReplyLimit},
	})
	if err != nil {
		return nil, err
	}
	if int64(out.Len()) != req.Bytes || chatImageHash(out.Bytes()) != req.SHA256 {
		return nil, errors.New("artifact read returned other bytes than the message names")
	}
	return out.Bytes(), nil
}
