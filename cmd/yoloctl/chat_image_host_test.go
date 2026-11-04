package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func chatImageHostFixture(t *testing.T) (*chatSession, []byte) {
	t.Helper()
	p, _, room := chatImageFixture(t)
	header, err := chatstream.RoomHeader(room)
	if err != nil {
		t.Fatal(err)
	}
	return &chatSession{ctx: t.Context(), header: header, link: &chatLink{}, target: controllerTarget{Space: "work", DiskID: header.DiskID}}, p
}

func TestChatImageClipboardOffAndOperatorNeverRead(t *testing.T) {
	for _, operator := range []bool{false, true} {
		s, _ := chatImageHostFixture(t)
		s.target.Operator = operator
		var opts chatui.Options
		configureChatImages(s, &opts, operator, func(context.Context) (hostintegration.ClipboardImage, error) {
			t.Fatal("desktop read without host clipboard authority")
			return hostintegration.ClipboardImage{}, nil
		})
		if opts.ReadImage != nil || opts.UploadImage != nil {
			t.Fatal("clipboard-off/operator callback installed")
		}
	}
}

func TestChatImagePreparationDoesNotPublishOrLeakPath(t *testing.T) {
	s, p := chatImageHostFixture(t)
	calls := 0
	s.target.Call = func(context.Context, controller.Request, controller.ClientIO) error {
		t.Fatal("paste published")
		return nil
	}
	var opts chatui.Options
	configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
		calls++
		return hostintegration.ClipboardImage{Data: p, Path: "/private/secret-source.png"}, nil
	})
	cmd := opts.ReadImage(chatui.ImageRequest{ID: "draft1", Channel: "#space"})
	if calls != 0 {
		t.Fatal("callback construction read desktop")
	}
	prepared := cmd().(chatui.ImagePreparedMsg)
	if calls != 1 || prepared.Err != nil || prepared.ID != "draft1" || prepared.Width != 2 || prepared.Height != 2 || !bytes.Equal(prepared.Data, p) || strings.Contains(prepared.Name, "secret") || prepared.Name != "paste-"+prepared.Token+".png" {
		t.Fatal(prepared, calls)
	}
}

func TestChatImagePreparationPreservesNoImageAndRejectsInvalidOrLarge(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		err  error
	}{
		{"no image", nil, hostintegration.ErrNoImage},
		{"invalid", []byte("file:///private/secret.png"), nil},
		{"oversized", make([]byte, chatImageLimit+1), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := chatImageHostFixture(t)
			var opts chatui.Options
			configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
				return hostintegration.ClipboardImage{Data: test.data}, test.err
			})
			prepared := opts.ReadImage(chatui.ImageRequest{ID: "draft1", Channel: "#space"})().(chatui.ImagePreparedMsg)
			if prepared.Err == nil || len(prepared.Data) != 0 || prepared.Token != "" || test.err != nil && !errors.Is(prepared.Err, test.err) {
				t.Fatal(prepared)
			}
		})
	}
}

func TestChatImageHostConfirmedAndUncertainAttemptsCannotReplay(t *testing.T) {
	for _, outcome := range []string{"confirmed", "lost reply", "forged reply"} {
		t.Run(outcome, func(t *testing.T) {
			s, p := chatImageHostFixture(t)
			calls := 0
			s.target.Call = func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
				calls++
				input, err := decodeChatImageRequest(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := readChatImage(streams.In, input)
				if err != nil || !bytes.Equal(data, p) {
					t.Fatal(err)
				}
				if outcome == "lost reply" {
					return &controller.OutcomeUnknown{ID: req.ID, Cause: io.ErrUnexpectedEOF}
				}
				result, err := decodeChatImageUploadReply(chatUploadReply(input), input, req.ID)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "forged reply" {
					result.PublicationToken = managedchat.NewID()
				}
				return json.NewEncoder(streams.Out).Encode(result)
			}
			var opts chatui.Options
			configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
				return hostintegration.ClipboardImage{Data: p}, nil
			})
			prepared := opts.ReadImage(chatui.ImageRequest{ID: "draft1", Channel: "#space"})().(chatui.ImagePreparedMsg)
			upload := chatui.ImageUpload{Channel: prepared.Channel, Token: prepared.Token, Data: prepared.Data}
			got := opts.UploadImage(upload)().(chatui.ImageUploadedMsg)
			want := chatui.Uncertain
			if outcome == "confirmed" {
				want = chatui.Confirmed
			}
			if got.State != want || got.RequestID == "" || got.Token != prepared.Token || calls != 1 {
				t.Fatal(got, calls)
			}
			again := opts.UploadImage(upload)().(chatui.ImageUploadedMsg)
			if again.State != chatui.Rejected || calls != 1 {
				t.Fatal("attempt replayed", again, calls)
			}
		})
	}
}

func TestChatImagePreparationAndUploadRejectReconnectedStream(t *testing.T) {
	s, p := chatImageHostFixture(t)
	var opts chatui.Options
	configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
		s.mu.Lock()
		s.link = &chatLink{}
		s.mu.Unlock()
		return hostintegration.ClipboardImage{Data: p}, nil
	})
	got := opts.ReadImage(chatui.ImageRequest{ID: "draft1", Channel: "#space"})().(chatui.ImagePreparedMsg)
	if got.Err == nil || got.Token != "" {
		t.Fatal("stale read prepared", got)
	}
	configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
		return hostintegration.ClipboardImage{Data: p}, nil
	})
	got = opts.ReadImage(chatui.ImageRequest{ID: "draft2", Channel: "#space"})().(chatui.ImagePreparedMsg)
	s.link = &chatLink{}
	s.target.Call = func(context.Context, controller.Request, controller.ClientIO) error {
		t.Fatal("stale image published")
		return nil
	}
	uploaded := opts.UploadImage(chatui.ImageUpload{Channel: got.Channel, Token: got.Token, Data: got.Data})().(chatui.ImageUploadedMsg)
	if uploaded.State != chatui.Rejected || uploaded.Err == nil {
		t.Fatal(uploaded)
	}
}

func TestChatImagePreparedMetadataBound(t *testing.T) {
	s, p := chatImageHostFixture(t)
	var opts chatui.Options
	configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
		return hostintegration.ClipboardImage{Data: p}, nil
	})
	first := opts.ReadImage(chatui.ImageRequest{ID: "first", Channel: "#space"})().(chatui.ImagePreparedMsg)
	for range chatImageDraftLimit {
		if got := opts.ReadImage(chatui.ImageRequest{ID: "next", Channel: "#space"})().(chatui.ImagePreparedMsg); got.Err != nil {
			t.Fatal(got.Err)
		}
	}
	s.target.Call = func(context.Context, controller.Request, controller.ClientIO) error {
		t.Fatal("evicted draft published")
		return nil
	}
	got := opts.UploadImage(chatui.ImageUpload{Channel: first.Channel, Token: first.Token, Data: first.Data})().(chatui.ImageUploadedMsg)
	if got.State != chatui.Rejected {
		t.Fatal(got)
	}
}

func TestChatImageSingleClipboardReadAndPathlessFailure(t *testing.T) {
	s, _ := chatImageHostFixture(t)
	var opts chatui.Options
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
		calls.Add(1)
		close(started)
		<-finish
		return hostintegration.ClipboardImage{}, errors.New("open /private/copied-secret.png: denied")
	})
	done := make(chan chatui.ImagePreparedMsg, 1)
	first := opts.ReadImage(chatui.ImageRequest{ID: "first", Channel: "#space"})
	go func() { done <- first().(chatui.ImagePreparedMsg) }()
	<-started
	second := opts.ReadImage(chatui.ImageRequest{ID: "second", Channel: "#space"})().(chatui.ImagePreparedMsg)
	if second.Err == nil || calls.Load() != 1 {
		t.Fatal("concurrent desktop read admitted", second, calls.Load())
	}
	close(finish)
	result := <-done
	if result.Err == nil || strings.Contains(result.Err.Error(), "/private") || result.Token != "" {
		t.Fatal("desktop path disclosed or failed read retained", result)
	}
}

func TestChatImageMissingChannelDoesNotReadClipboard(t *testing.T) {
	s, _ := chatImageHostFixture(t)
	var opts chatui.Options
	configureChatImages(s, &opts, true, func(context.Context) (hostintegration.ClipboardImage, error) {
		t.Fatal("invalid channel invoked desktop")
		return hostintegration.ClipboardImage{}, nil
	})
	result := opts.ReadImage(chatui.ImageRequest{ID: "draft", Channel: "#unknown"})().(chatui.ImagePreparedMsg)
	if result.Err == nil || result.Token != "" {
		t.Fatal(result)
	}
}
