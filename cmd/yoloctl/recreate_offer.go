package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// recreateOfferKind says which command asks, because each skips a different
// case: a running box's start returns as already running, an attach to a
// running box joins or decides in the worker, and the chat server also
// refuses restart drift.
type recreateOfferKind int

const (
	offerStart recreateOfferKind = iota
	offerRestart
	offerAttach
	offerChatServer
)

// recreateOfferInteractive is true only when a person can answer at the
// terminal; tests replace it.
var recreateOfferInteractive = func(cmd *cobra.Command) bool {
	return stdinIsTerminal(cmd.InOrStdin()) && term.IsTerminal(int(os.Stdout.Fd()))
}

const recreateOfferInspectTimeout = 30 * time.Second

// offerRecreate runs before the original request is sent, while the host
// still owns the terminal (no request, reader or view started yet). It reads
// the box through the read-only box.inspect (the same Diff as box diff) and,
// only for an affirmative recreate (or, for the chat server, restart)
// strategy, asks once. A yes sends `box recreate NAME --force --no-attach`
// through normal routing; the caller then sends its original request
// unchanged. A no, an unreadable inspect or any other strategy changes
// nothing: the original request is sent and the worker refuses drift before
// any image work.
func offerRecreate(cmd *cobra.Command, target controllerTarget, box string, kind recreateOfferKind, noAutoupdate bool) error {
	if !recreateOfferInteractive(cmd) {
		return nil
	}
	var report struct {
		Identity inspectIdentity `json:"identity"`
		Drift    inspectDrift    `json:"drift"`
	}
	raw := boundedBuffer{Limit: 4 << 20}
	ctx, cancel := context.WithTimeout(cmd.Context(), recreateOfferInspectTimeout)
	inspect := controller.Request{ID: rand.Text(), Method: "box.inspect", Space: target.Space, Args: []string{box}, Dash: -1, Flags: map[string][]string{"json": {"true"}}}
	err := target.Call(ctx, inspect, controller.ClientIO{Out: &raw, Err: &boundedBuffer{Limit: 64 << 10}})
	cancel()
	if err != nil || json.Unmarshal(raw.Bytes(), &report) != nil {
		return nil // not evidence: the worker's early refusal still applies
	}
	running := report.Identity.RuntimeState == string(boxstate.RuntimeRunning)
	if running && (kind == offerStart || kind == offerAttach) {
		return nil
	}
	strategy := report.Drift.Strategy
	if strategy != "recreate" && !(kind == offerChatServer && strategy == "restart") {
		return nil
	}
	extra := ""
	if running {
		extra += "; its running session ends"
	}
	if kind == offerChatServer {
		extra += "; recreating the chat server restarts the room for every connected chat client"
	}
	ok, err := promptYesNo(fmt.Sprintf("box %s requires recreation (%s).\nRecreate it now? Its container is replaced: the workdir and the account's native data are kept, other files in the container are lost%s. [y/N]: ",
		box, report.Drift.Reason, extra), cmd.InOrStdin(), cmd.ErrOrStderr())
	if err != nil || !ok {
		return err
	}
	flags := map[string][]string{"force": {"true"}, "no-attach": {"true"}}
	if noAutoupdate {
		flags["no-autoupdate"] = []string{"true"}
	}
	recreate := controller.Request{ID: rand.Text(), Method: "box.recreate", Space: target.Space, Args: []string{box}, Dash: -1, Flags: flags}
	// As on the regular path: interrupts are forwarded to the operation
	// instead of abandoning it, and an interrupted shared image build keeps
	// its explicit retry question.
	signals := make(chan int, 4)
	stopSignals := forwardCommandSignals(cmd.Context(), signals)
	defer stopSignals()
	streams := controller.ClientIO{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Signals: signals}
	return callWithImageRetry(cmd.Context(), cmd, recreate, streams, target.Call)
}

// boxRecreateOffer selects the offer for an outgoing box request, or none:
// non-attaching scripts, structured use (routed before this) and chat-guarded
// attaches (their room/member/box identity is checked under the native lock)
// never get one.
func boxRecreateOffer(req controller.Request) (box string, kind recreateOfferKind, ok bool) {
	if len(req.Args) == 0 || flagTrue(req.Flags, "no-attach") {
		return "", 0, false
	}
	switch req.Method {
	case "box.start":
		kind = offerStart
	case "box.restart":
		kind = offerRestart
	case "box.attach":
		for _, name := range append([]string{"chat-allow-start"}, chatAttachIdentityFlags...) {
			if len(req.Flags[name]) > 0 {
				return "", 0, false
			}
		}
		kind = offerAttach
	default:
		return "", 0, false
	}
	return req.Args[0], kind, true
}

func flagTrue(flags map[string][]string, name string) bool {
	values := flags[name]
	return len(values) > 0 && values[len(values)-1] == "true"
}
