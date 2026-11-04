package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
)

type chatImageConnection interface {
	Verify(context.Context) error
	Run(context.Context, []string, *os.File, io.Writer, io.Writer) error
}

// This host-only operation never stages a file or opens an IRC connection.
// The existing human stream must already be joined and allowed to speak;
// AIRC enforces that permission at the artifact POST commit.
func runControllerChatImage(ctx context.Context, req controller.Request, in *os.File, out io.Writer) error {
	if !currentControllerPrincipal(ctx).Admin() {
		return errors.New("image publication requires the human host endpoint")
	}
	input, err := decodeChatImageRequest(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = in.Close() })
	defer stop()
	png, err := readChatImage(in, input)
	if err != nil {
		return err
	}
	store, err := controller.NativeChatStore()
	if err != nil {
		return err
	}
	conn, err := openManagedChatConnection(ctx, req.Space, store)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	result, err := executeChatImageUpload(ctx, req.ID, input, png, conn.Room, conn, func() error {
		current, err := store.ReadManifestSnapshot()
		if err != nil {
			return err
		}
		return input.matchesRoom(current)
	})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return &controller.EffectsUncertain{Cause: err}
	}
	return nil
}

// Everything before runChatImageCommand is read-only. A failed command or
// unverified reply after that boundary cannot prove that no POST committed.
func executeChatImageUpload(ctx context.Context, requestID string, input chatImageRequest, png []byte, room *managedchat.Room, conn chatImageConnection, authorize func() error) (chatImageResult, error) {
	var result chatImageResult
	if err := input.validate(); err != nil {
		return result, err
	}
	if err := validateChatImage(png); err != nil {
		return result, err
	}
	if int64(len(png)) != input.Bytes || chatImageHash(png) != input.SHA256 {
		return result, errors.New("image publication bytes changed before launch")
	}
	if err := input.matchesRoom(room); err != nil {
		return result, err
	}
	var probe bytes.Buffer
	if err := conn.Run(ctx, []string{"/usr/local/bin/airc", "features"}, nil, &boundedChatWriter{Out: &probe, Remaining: managedchat.ControlLimit}, &boundedChatWriter{Out: io.Discard, Remaining: chatImageReplyLimit}); err != nil {
		return result, errors.New("could not inspect the confined artifact client")
	}
	var features managedchat.Features
	if json.Unmarshal(probe.Bytes(), &features) != nil {
		return result, errors.New("invalid artifact client feature response")
	}
	required := []string{"artifact-cli", "artifact-metadata"}
	if input.Stage {
		required = append(required, chatstream.AttachmentsFeature)
	}
	if err := features.Require(required...); err != nil {
		return result, err
	}
	if err := conn.Verify(ctx); err != nil {
		return result, err
	}
	if err := authorize(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := controller.BeginScopedEffects(ctx, chatIntent("")); err != nil {
		return result, err
	}
	raw, err := runChatImageCommand(ctx, conn, input, png)
	switch {
	case err != nil:
	case input.Stage:
		result, err = decodeChatImageStageReply(raw, input, requestID)
	default:
		result, err = decodeChatImageUploadReply(raw, input, requestID)
	}
	if err != nil {
		return chatImageResult{}, &controller.EffectsUncertain{Cause: errors.New("image publication did not return a verified reply; inspect the request receipt and authenticated channel history before any new attempt")}
	}
	return result, nil
}

func runChatImageCommand(ctx context.Context, conn chatImageConnection, input chatImageRequest, png []byte) ([]byte, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = reader.Close(); _ = writer.Close() })
	defer stop()
	written := make(chan error, 1)
	go func() {
		_, err := writer.Write(png)
		_ = writer.Close()
		written <- err
	}()
	var raw bytes.Buffer
	argv := []string{"/usr/local/bin/airc", "artifact", "upload", "--config", managedchat.HumanMount + "/client.toml",
		"--channel", input.Channel, "--name", input.filename(), "--stdin"}
	if input.Stage {
		argv = append(argv, "--stage")
	}
	err = conn.Run(ctx, argv, reader, &boundedChatWriter{Out: &raw, Remaining: chatImageReplyLimit}, &boundedChatWriter{Out: io.Discard, Remaining: chatImageReplyLimit})
	_ = reader.Close() // unblock a writer if the confined process refused early
	writeErr := <-written
	if err != nil {
		return nil, err
	}
	return raw.Bytes(), writeErr
}
