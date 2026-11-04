package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

// lockedWriter lets the test poll a child's output while it is written.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// This opt-in live check runs a Participants click's lookup through the chat
// view's own path (resolveAttach, callChatAttachTarget, the real
// attachLookupTimeout) against the host controller of a disposable running
// space while another box mutation holds its exclusive native space lock. The
// holder is `box recreate` without --force waiting at its confirmation
// prompt, which runs inside that lock; it is declined afterwards, so nothing
// changes, and it is never killed. The caller supplies an isolated
// XDG_CONFIG_HOME holding only that space. The test binary must carry the
// space's controller bundle ID, linked as for a coordinated build of the same
// checkout as YOLOCTL, and its VM transport re-executes it (TestMain):
//
//	BUNDLE=$(sed -n 's/^bundle: //p' "$XDG_CONFIG_HOME/yoloctl/spaces/$YOLOCTL_TEST_LIVE_CHAT_SPACE/space.yaml")
//	go test -count=1 -v -run TestLiveChatAttachTargetAnswersWhileSpaceLockIsHeld \
//	  -ldflags "-X github.com/leogr/yoloctl/internal/controller.BuildID=$BUNDLE" ./cmd/yoloctl
func TestLiveChatAttachTargetAnswersWhileSpaceLockIsHeld(t *testing.T) {
	space, member, holderBox, yoloctl := os.Getenv("YOLOCTL_TEST_LIVE_CHAT_SPACE"), os.Getenv("YOLOCTL_TEST_LIVE_CHAT_MEMBER"),
		os.Getenv("YOLOCTL_TEST_LIVE_CHAT_HOLDER"), os.Getenv("YOLOCTL")
	if space == "" || member == "" || holderBox == "" || yoloctl == "" {
		t.Skip("set YOLOCTL, YOLOCTL_TEST_LIVE_CHAT_SPACE, _MEMBER (an enrolled participant whose box runs) and _HOLDER (another box) for a disposable space")
	}
	if os.Getenv("XDG_CONFIG_HOME") == "" || member == holderBox {
		t.Fatal("use an isolated XDG_CONFIG_HOME and a holder box other than the member")
	}
	// Never the space hosting this session: an operator client would select
	// its own space's endpoint.
	if operator, err := loadOperatorClient(); err != nil || operator != nil {
		t.Fatalf("run this from the disposable space's host, not inside a box (%v)", err)
	}
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		t.Fatal(err)
	}
	if identity, err := controller.ExecutableIdentity(); err != nil || identity != boot.Bundle {
		t.Fatalf("this test binary is %s (%v) but space %s selects bundle %s; link BuildID=%s from the checkout YOLOCTL was built from", identity, err, space, boot.Bundle, boot.Bundle)
	}
	if attachLookupTimeout != 10*time.Second {
		t.Fatalf("the view's lookup deadline is %v, not the shipped 10s", attachLookupTimeout)
	}
	t.Setenv(liveReexecEnv, "1")
	cmd := &cobra.Command{Use: "chat"}
	cmd.Flags().String("space", "", "")
	if err := cmd.Flags().Set("space", space); err != nil {
		t.Fatal(err)
	}
	cmd.SetErr(io.Discard)
	ctx := t.Context()

	// Resolve the immutable member ID before the lock is held.
	status, err := callChatController(ctx, cmd, "chat.status", chatRuntimeRequest{})
	if err != nil {
		t.Fatal("chat status:", err)
	}
	memberID, boxID := "", ""
	for _, m := range status.Members {
		if m.Name == member && !m.Revoked {
			memberID, boxID = m.ID, m.BoxID
		}
	}
	if status.Room == nil || memberID == "" || boxID == "" {
		t.Fatalf("no enrolled participant %q in %+v", member, status.Members)
	}
	type receipt struct{ ID, Method, State string }
	receipts := func() []receipt {
		t.Helper()
		out, err := exec.Command(yoloctl, "space", "operation", space).Output()
		if err != nil {
			t.Fatal("space operation:", err)
		}
		var list []receipt
		if err := json.Unmarshal(out, &list); err != nil {
			t.Fatal("space operation output:", err)
		}
		return list
	}
	before := map[string]bool{}
	for _, r := range receipts() {
		if r.State == "pending" || r.State == "uncertain" {
			t.Fatalf("receipt %s (%s) is %s before the test; the fixture is not idle", r.ID, r.Method, r.State)
		}
		before[r.ID] = true
	}
	// holderPending reports whether a box.recreate this test started is the
	// pending receipt, and fails on any uncertain receipt.
	holderPending := func() bool {
		t.Helper()
		pending := false
		for _, r := range receipts() {
			if r.State == "uncertain" || r.State == "command-uncertain" {
				t.Errorf("uncertain receipt %s (%s)", r.ID, r.Method)
			}
			switch {
			case before[r.ID]:
			case r.Method == "box.recreate" && r.State == "pending":
				pending = true
			case r.Method == "chat.attach-target":
				t.Errorf("the lookup left receipt %s (%s)", r.ID, r.State)
			default:
				t.Logf("unrelated new receipt %s %s %s", r.ID, r.Method, r.State)
			}
		}
		return pending
	}

	holder := exec.Command(yoloctl, "box", "recreate", holderBox, "--space", space, "--no-attach", "--no-autoupdate")
	answer, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var holderOut lockedWriter
	holder.Stdout, holder.Stderr = &holderOut, &holderOut
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	holderDone := make(chan error, 1)
	go func() { holderDone <- holder.Wait() }()
	// Closing its input declines the prompt; the holder is never killed
	// (recreate is not effect-gated) and never left holding the lock.
	declined := false
	t.Cleanup(func() {
		if !declined {
			_ = answer.Close()
		}
		select {
		case err := <-holderDone:
			holderDone <- err
		case <-time.After(30 * time.Second):
			t.Errorf("the lock holder still runs; inspect space %s before destroying it", space)
		}
	})
	for deadline := time.Now().Add(2 * time.Minute); !strings.Contains(holderOut.String(), "continue? [y/N]"); time.Sleep(100 * time.Millisecond) {
		select {
		case err := <-holderDone:
			holderDone <- err
			t.Fatalf("the lock holder ended before its prompt: %v\n%s", err, holderOut.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lock holder never reached its prompt:\n%s", holderOut.String())
		}
	}
	if !holderPending() {
		t.Fatalf("the prompting holder has no pending box.recreate receipt:\n%s", holderOut.String())
	}

	// The click's lookup exactly as the chat view runs it.
	view := &chatSession{ctx: ctx, members: map[string]string{member: memberID},
		header: chatstream.Header{Bootstrap: managedchat.Bootstrap{RoomID: status.Room.ID}}}
	started := time.Now()
	msg := view.resolveAttach(cmd, member)
	elapsed := time.Since(started)
	got, ok := msg.(chatui.AttachTargetMsg)
	t.Logf("chat.attach-target answered in %v while box.recreate %s held the space lock: %+v", elapsed, holderBox, msg)
	if !ok || got.Err != nil {
		t.Fatalf("the lookup failed after %v: %+v", elapsed, msg)
	}
	// Correct, not only fast: this room's participant and its declared,
	// running box.
	want := chatui.AttachTarget{Account: member, Box: member, BoxID: boxID, State: "running", RoomID: status.Room.ID, MemberID: memberID}
	if elapsed >= attachLookupTimeout || got.Target != want {
		t.Fatalf("the lookup answered %+v after %v, want %+v", got.Target, elapsed, want)
	}
	// The holder was still pending, so it held the lock throughout.
	if !holderPending() {
		t.Fatal("the lock holder released before the lookup ended")
	}

	declined = true
	if _, err := io.WriteString(answer, "n\n"); err != nil {
		t.Fatal(err)
	}
	_ = answer.Close()
	select {
	case err := <-holderDone:
		holderDone <- err
		// A declined recreate changes nothing and exits nonzero.
		if err == nil || !strings.Contains(holderOut.String(), "recreate not confirmed") {
			t.Fatalf("the holder did not decline cleanly: %v\n%s", err, holderOut.String())
		}
	case <-time.After(time.Minute):
		t.Fatal("the declined holder did not exit")
	}
	// Nothing this test started is left pending or uncertain.
	for _, r := range receipts() {
		if !before[r.ID] && (r.State == "pending" || r.State == "uncertain" || r.State == "command-uncertain") {
			t.Errorf("receipt %s (%s) left %s", r.ID, r.Method, r.State)
		}
	}
}
