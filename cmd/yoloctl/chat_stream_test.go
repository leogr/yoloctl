package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"golang.org/x/sys/unix"
)

func TestChatClientFeaturesRequirePresence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe string
		want  string
	}{
		{"supported", `{"protocol":1,"features":["client-unix","jsonl-v2","client-presence","client-topic","nick-regain"]}`, ""},
		{"missing-regain", `{"protocol":1,"features":["client-unix","jsonl-v2","client-presence","client-topic"]}`, "nick-regain"},
		{"missing-topic", `{"protocol":1,"features":["client-unix","jsonl-v2","client-presence"]}`, "client-topic"},
		{"older-client", `{"protocol":1,"features":["client-unix","jsonl-v2"]}`, "client-presence"},
		{"server-feature-is-not-client", `{"protocol":1,"features":["client-unix","jsonl-v2","server-presence"]}`, "client-presence"},
		{"missing-unix", `{"protocol":1,"features":["jsonl-v2","client-presence"]}`, "client-unix"},
		{"missing-jsonl-v2", `{"protocol":1,"features":["client-unix","client-presence"]}`, "jsonl-v2"},
		{"unsupported-protocol", `{"protocol":2,"features":["client-unix","jsonl-v2","client-presence"]}`, "unsupported AIRC managed protocol"},
		{"malformed", `{"protocol":1`, "invalid AIRC client feature probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateManagedChatClientFeatures([]byte(tc.probe))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want diagnostic containing %q", err, tc.want)
			}
		})
	}
}

// In a controller worker stdin is an inherited blocking pipe, unlike os.Pipe's
// pollable Go file. Client exit must return its real result while the host
// keeps stdin open, including when an incomplete request was buffered.
func TestChatRelayExitDoesNotWaitForStdin(t *testing.T) {
	clientErr := errors.New("client launcher failed")
	for _, tc := range []struct {
		name, partial string
		err           error
	}{
		{"failure", "", clientErr},
		{"failure-partial", `{"id":"unfinished"`, clientErr},
		{"success", "", nil},
		{"success-partial", `{"id":"unfinished"`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			fd, err := unix.FcntlInt(read.Fd(), unix.F_DUPFD_CLOEXEC, 0)
			_ = read.Close()
			if err != nil {
				_ = write.Close()
				t.Fatal(err)
			}
			input := os.NewFile(uintptr(fd), "inherited-chat-stdin")
			defer func() { _ = write.Close(); _ = input.Close() }()
			if _, err := io.WriteString(write, tc.partial); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- relayManagedChatWithExec(t.Context(), nil, input, io.Discard, io.Discard,
					managedchat.Store{}, &managedchat.Room{}, "human", false, false,
					func(context.Context, *agentbox.Config, []string, *os.File, io.Writer, io.Writer) error {
						// Let the filter enter its read before the launcher fails.
						time.Sleep(30 * time.Millisecond)
						return tc.err
					})
			}()
			select {
			case err := <-done:
				if !errors.Is(err, tc.err) {
					t.Fatalf("client result = %v, want %v", err, tc.err)
				}
			case <-time.After(time.Second):
				_ = write.Close() // release the blocked reader before reporting
				<-done
				t.Fatal("client exit waited for host stdin to close")
			}
		})
	}
}

func TestChatRelayPreservesFilterFailure(t *testing.T) {
	input, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = write.Close(); _ = input.Close() }()
	if _, err := io.WriteString(write, "{\"id\":\"x\",\"op\":\"unsupported\"}\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = relayManagedChatWithExec(ctx, nil, input, io.Discard, io.Discard,
		managedchat.Store{}, &managedchat.Room{}, "human", false, false,
		func(ctx context.Context, _ *agentbox.Config, _ []string, _ *os.File, _, _ io.Writer) error {
			<-ctx.Done()
			return nil // a clean client exit must not hide the filter's refusal
		})
	if err == nil || !strings.Contains(err.Error(), "invalid or unsupported managed chat stream request") {
		t.Fatalf("filter failure = %v", err)
	}
}

// A client may exit successfully on the filtered pipe's EOF rather than on
// cancellation. The refusal that produced that EOF must still be the result.
func TestChatRelayReportsRefusalBeforeClientEOF(t *testing.T) {
	for range 50 {
		input, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(write, "{\"id\":\"x\",\"op\":\"unsupported\"}\n"); err != nil {
			t.Fatal(err)
		}
		err = relayManagedChatWithExec(t.Context(), nil, input, io.Discard, io.Discard,
			managedchat.Store{}, &managedchat.Room{}, "human", false, false,
			func(_ context.Context, _ *agentbox.Config, _ []string, filtered *os.File, _, _ io.Writer) error {
				_, err := io.Copy(io.Discard, filtered)
				return err // nil at EOF: a clean exit caused by the refusal
			})
		_ = write.Close()
		_ = input.Close()
		if err == nil || !strings.Contains(err.Error(), "invalid or unsupported managed chat stream request") {
			t.Fatalf("EOF-first client hid the refusal: %v", err)
		}
	}
}

// A stream registers the saved nickname itself through the fixed confined
// launcher. While status shows another live session of the human account it
// sends only the typed nick-held frame, before any effect or launch; the
// explicit take-over flag launches the same nickname with AIRC's regain. The
// SSH peer echoes the exec command.
func TestChatStreamUsesTheSavedNickAndRefusesAHeldOneBeforeEffects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newChatConnectionFixture(t)
	oldStore, oldOpen := nativeChatStoreFn, openManagedChatConnectionFn
	t.Cleanup(func() { nativeChatStoreFn, openManagedChatConnectionFn = oldStore, oldOpen })
	nativeChatStoreFn = func() (managedchat.Store, error) { return f.store, nil }
	openManagedChatConnectionFn = func(ctx context.Context, space string, store managedchat.Store) (*managedChatConnection, error) {
		return openManagedChatConnectionWith(ctx, space, store, f.ops)
	}
	nicks, err := managedchat.RoomNicknames(f.room)
	if err != nil {
		t.Fatal(err)
	}
	saved := nicks[f.room.Human.ID]
	stream := func(flags map[string][]string) (out string, effects int, err error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		ctx = controller.WithEffectGate(ctx, func(context.Context, controller.EffectIntent) error { effects++; return nil })
		ctx = context.WithValue(ctx, controllerReleaseKey{}, func() error { return nil })
		input, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = input.Close() }()
		var buf bytes.Buffer
		err = runControllerChatStream(ctx, controller.Request{Method: "chat.stream", Space: "work", Flags: flags}, input, &buf, io.Discard)
		return buf.String(), effects, err
	}
	launches := func(out string, takeOver bool) {
		t.Helper()
		var header chatstream.Header
		rest := strings.NewReader(out)
		dec := json.NewDecoder(rest)
		if err := dec.Decode(&header); err != nil {
			t.Fatal(err)
		}
		if header.Event != "managed-room" || header.Nick != saved {
			t.Fatalf("the stream must register the saved nickname %q: %+v", saved, header)
		}
		for _, identity := range header.Identities {
			if identity.Role == "human" && identity.Nick != saved {
				t.Fatalf("the identity map must keep the saved nickname: %+v", identity)
			}
		}
		launched, _ := io.ReadAll(io.MultiReader(dec.Buffered(), rest))
		want := remoteCommandString(chatstream.ClientArgv(managedChatPython, "/usr/local/bin/airc", managedchat.HumanMount+"/client.toml", saved, takeOver))
		if got := strings.TrimPrefix(string(launched), "\n"); got != want+"\n" {
			t.Fatalf("the stream did not launch the fixed client launcher (take-over=%v):\n%s", takeOver, got)
		}
	}

	out, effects, err := stream(nil)
	if err != nil || effects != 1 {
		t.Fatalf("an unheld nickname must connect: effects=%d %v", effects, err)
	}
	launches(out, false)

	f.humanLive.Store(1)
	commands := f.commands.Load()
	out, effects, err = stream(nil)
	if !errors.Is(err, chatstream.ErrNickHeld) || effects != 0 || out != `{"event":"nick-held"}`+"\n" {
		t.Fatalf("a held nickname must be refused before any effect: effects=%d out=%q %v", effects, out, err)
	}
	if ran := f.commands.Load() - commands; ran != 1 {
		t.Fatalf("a refused stream may only probe features, not launch a client: %d commands", ran)
	}

	out, effects, err = stream(map[string][]string{chatstream.TakeOverFlag: {"true"}})
	if err != nil || effects != 1 {
		t.Fatalf("an explicit take-over must connect: effects=%d %v", effects, err)
	}
	launches(out, true)

	for _, flags := range []map[string][]string{{chatstream.TakeOverFlag: {"yes"}}, {"other": {"true"}}, {chatstream.TakeOverFlag: {"true"}, "other": nil}} {
		if _, effects, err := stream(flags); err == nil || effects != 0 {
			t.Fatalf("flags %v must be refused before any effect: effects=%d %v", flags, effects, err)
		}
	}
}
