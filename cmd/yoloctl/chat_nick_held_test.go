package main

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

// The prompt follows the typed pre-effect refusal and, when another session
// connected meanwhile, the confined client's NICK_HELD registration failure as
// it reaches the view (stderr tail on the transport error). AIRC's 433 is the
// hard error that no take-over can resolve.
func TestChatViewMapsNicknameRefusals(t *testing.T) {
	failed := func(stderr string) error {
		tail := &stderrTail{}
		_, _ = io.WriteString(tail, stderr)
		return fmt.Errorf("managed chat stream failed: %w", tail.annotate(errors.New("controller exit status 2")))
	}
	if !chatNickHeld(chatstream.ErrNickHeld) || !chatNickHeld(fmt.Errorf("stream: %w", chatstream.ErrNickHeld)) {
		t.Fatal("the typed refusal must ask")
	}
	if !chatNickHeld(failed("Error: IRC registration failed (NICK_HELD)\n")) {
		t.Fatal("a raced NICK_HELD must ask")
	}
	for _, err := range []error{nil, failed("Error: IRC registration failed (433)\n"), chatstream.ErrTakenOver} {
		if chatNickHeld(err) {
			t.Fatalf("%v must not ask to take over", err)
		}
	}
	msg := chatViewEvent(chatstream.Event{Kind: "connection", Err: failed("Error: IRC registration failed (433)\n")}).(chatui.ConnectionMsg)
	if !errors.Is(msg.Err, errChatNickTaken) {
		t.Fatalf("433 must explain another account holds the nickname: %v", msg.Err)
	}
}
