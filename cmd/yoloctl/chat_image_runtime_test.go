package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
)

type imageTestConnection struct {
	verifyErr error
	run       func([]string, *os.File, io.Writer, io.Writer) error
	uploads   int
	probes    int
	// features overrides the probe's default feature list.
	features string
}

func (c *imageTestConnection) Verify(context.Context) error { return c.verifyErr }
func (c *imageTestConnection) Run(_ context.Context, argv []string, in *os.File, out, notice io.Writer) error {
	if len(argv) == 2 && argv[1] == "features" {
		c.probes++
		features := c.features
		if features == "" {
			features = `"artifact-cli","artifact-metadata"`
		}
		_, err := io.WriteString(out, `{"protocol":1,"features":[`+features+`]}`)
		return err
	}
	c.uploads++
	return c.run(argv, in, out, notice)
}

func TestChatImageUploadUsesFixedConfinedStdin(t *testing.T) {
	p, input, room := chatImageFixture(t)
	conn := &imageTestConnection{run: func(argv []string, in *os.File, out, _ io.Writer) error {
		want := []string{"/usr/local/bin/airc", "artifact", "upload", "--config", managedchat.HumanMount + "/client.toml", "--channel", "#space", "--name", input.filename(), "--stdin"}
		if !reflect.DeepEqual(argv, want) {
			t.Fatal(argv)
		}
		got, err := io.ReadAll(in)
		if err != nil || !bytes.Equal(got, p) {
			t.Fatal("stdin changed image bytes", err)
		}
		_, err = out.Write(chatUploadReply(input))
		return err
	}}
	authorized := 0
	gates := 0
	ctx := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error { gates++; return nil })
	result, err := executeChatImageUpload(ctx, "request1", input, p, room, conn, func() error { authorized++; return nil })
	if err != nil || result.RequestID != "request1" || conn.uploads != 1 || conn.probes != 1 || authorized != 1 || gates != 1 {
		t.Fatal(result, err, conn, authorized, gates)
	}
}

func TestChatImageFailsBeforeLaunchForInvalidOrStaleAuthority(t *testing.T) {
	p, input, room := chatImageFixture(t)
	for _, test := range []struct {
		name      string
		change    func(*chatImageRequest, **managedchat.Room, *[]byte)
		verifyErr error
		authErr   error
	}{
		{name: "PNG", change: func(_ *chatImageRequest, _ **managedchat.Room, p *[]byte) { *p = []byte("not PNG") }},
		{name: "room", change: func(r *chatImageRequest, _ **managedchat.Room, _ *[]byte) { r.RoomID = managedchat.NewID() }},
		{name: "channel", change: func(r *chatImageRequest, _ **managedchat.Room, _ *[]byte) { r.ChannelID = managedchat.NewID() }},
		{name: "disk", change: func(r *chatImageRequest, _ **managedchat.Room, _ *[]byte) { r.DiskID = managedchat.NewID() }},
		{name: "server replaced", verifyErr: errors.New("container incarnation changed")},
		{name: "private binds changed", verifyErr: errors.New("private bind verification failed")},
		{name: "access revoked", authErr: errors.New("native channel access changed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, r, data := input, room, p
			if test.change != nil {
				test.change(&next, &r, &data)
			}
			conn := &imageTestConnection{verifyErr: test.verifyErr, run: func([]string, *os.File, io.Writer, io.Writer) error {
				t.Fatal("invalid publication launched")
				return nil
			}}
			_, err := executeChatImageUpload(t.Context(), "request1", next, data, r, conn, func() error { return test.authErr })
			var uncertain *controller.EffectsUncertain
			if err == nil || errors.As(err, &uncertain) || conn.uploads != 0 {
				t.Fatal("pre-launch error must have no publication effect", err, conn.uploads)
			}
		})
	}
}

func TestChatImagePostLaunchFailureAlwaysUncertainWithoutReplay(t *testing.T) {
	p, input, room := chatImageFixture(t)
	for _, test := range []struct {
		name string
		run  func(io.Writer, io.Writer) error
	}{
		{"lost HTTP reply", func(io.Writer, io.Writer) error { return io.ErrUnexpectedEOF }},
		{"503 after possible commit", func(io.Writer, io.Writer) error { return errors.New("503 response unavailable") }},
		{"timeout", func(io.Writer, io.Writer) error { return context.DeadlineExceeded }},
		{"forged reply", func(out, _ io.Writer) error { _, err := io.WriteString(out, `{}`); return err }},
		{"oversized reply", func(out, _ io.Writer) error { _, err := out.Write(make([]byte, chatImageReplyLimit+1)); return err }},
		{"oversized stderr", func(_, notice io.Writer) error {
			_, err := notice.Write(make([]byte, chatImageReplyLimit+1))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := &imageTestConnection{run: func(_ []string, in *os.File, out, notice io.Writer) error {
				if _, err := io.Copy(io.Discard, in); err != nil {
					t.Fatal(err)
				}
				return test.run(out, notice)
			}}
			ctx := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error { return nil })
			_, err := executeChatImageUpload(ctx, "request1", input, p, room, conn, func() error { return nil })
			var uncertain *controller.EffectsUncertain
			if !errors.As(err, &uncertain) || conn.uploads != 1 {
				t.Fatal("possible publication was retried or marked safe", err, conn.uploads)
			}
		})
	}
}

func TestChatImageCancelledPipeReleasesWriter(t *testing.T) {
	p, input, _ := chatImageFixture(t)
	p = append(p, make([]byte, chatImageLimit-len(p))...)
	ctx, cancel := context.WithCancel(t.Context())
	conn := &imageTestConnection{run: func(_ []string, _ *os.File, _, _ io.Writer) error {
		cancel() // before reading the full pipe; writer must be joined
		return ctx.Err()
	}}
	if _, err := runChatImageCommand(ctx, conn, input, p); err == nil {
		t.Fatal("cancelled upload succeeded")
	}
}

func TestChatImageControllerCancellationClosesIncompleteInput(t *testing.T) {
	_, input, _ := chatImageFixture(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := controller.Request{ID: "request1", Method: "chat.artifact.upload", Space: "work", Dash: -1, Payload: chatImagePayload(input)}
	// No bytes or EOF arrive. Cancellation must close the owned descriptor
	// and return before attempting any native store/runtime access.
	if err := runControllerChatImage(ctx, request, reader, io.Discard); err == nil {
		t.Fatal("incomplete cancelled stream succeeded")
	}
}

func TestChatImageEffectGateDenialAndPreGateCancellationNeverPublish(t *testing.T) {
	p, input, room := chatImageFixture(t)
	for _, mode := range []string{"missing", "denied", "cancelled before gate"} {
		t.Run(mode, func(t *testing.T) {
			conn := &imageTestConnection{run: func([]string, *os.File, io.Writer, io.Writer) error {
				t.Fatal("publication bypassed the effect gate")
				return nil
			}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			gates := 0
			if mode != "missing" {
				ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error {
					gates++
					return errors.New("receipt write refused")
				})
			}
			authorize := func() error {
				if mode == "cancelled before gate" {
					cancel()
				}
				return nil
			}
			_, err := executeChatImageUpload(ctx, "request1", input, p, room, conn, authorize)
			var uncertain *controller.EffectsUncertain
			if err == nil || errors.As(err, &uncertain) || conn.uploads != 0 || mode == "cancelled before gate" && gates != 0 || mode == "denied" && gates != 1 {
				t.Fatal("pre-effect rejection crossed the publication boundary", err, conn.uploads, gates)
			}
		})
	}
}
