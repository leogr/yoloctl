package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
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
		Identity inspectIdentity  `json:"identity"`
		Manifest *inspectManifest `json:"manifest"`
		Drift    inspectDrift     `json:"drift"`
	}
	raw := boundedBuffer{Limit: 4 << 20}
	ctx, cancel := context.WithTimeout(cmd.Context(), recreateOfferInspectTimeout)
	inspect := controller.Request{ID: rand.Text(), Method: "box.inspect", Space: target.Space, Args: []string{box}, Dash: -1, Flags: map[string][]string{"json": {"true"}}}
	err := target.Call(ctx, inspect, controller.ClientIO{Out: &raw, Err: &boundedBuffer{Limit: 64 << 10}})
	cancel()
	if err != nil || json.Unmarshal(raw.Bytes(), &report) != nil {
		return nil // not evidence: the worker's early refusal still applies
	}
	if report.Identity.LegacyRuntime {
		// A pre-native box is never started or restarted: the worker refuses
		// (stopping a running one first), and offerLegacyRecreate asks then.
		return nil
	}
	running := report.Identity.RuntimeState == string(boxstate.RuntimeRunning)
	strategy := report.Drift.Strategy
	affirmative := strategy == "recreate" || (kind == offerChatServer && strategy == "restart")
	if !affirmative || running && (kind == offerStart || kind == offerAttach) {
		// Directive 56: a box created before the session launcher is offered its
		// recreate when this request starts a new managed session. Its own
		// rule, not the drift offer's running-box shortcut.
		if !launcherRecreateOffered(report.Identity, report.Manifest, report.Drift, kind) {
			return nil
		}
		ok, err := promptYesNo(agentbox.RecreateWarning(box)+".\nRecreate now? [y/N]: ", cmd.InOrStdin(), cmd.ErrOrStderr())
		if err != nil || !ok {
			return err
		}
		return sendOfferedRecreate(cmd, target, box, noAutoupdate)
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
	return sendOfferedRecreate(cmd, target, box, noAutoupdate)
}

// boxLegacyRecreateOffer selects a request whose refusal for a pre-native box
// is offered recreation: one that needs a new run (start, restart, attach,
// whatever its attach flags) or, with apply, a policy apply (set, reconcile)
// that restarts a running box. A chat-guarded attach is never offered.
func boxLegacyRecreateOffer(req controller.Request) (box string, apply, ok bool) {
	if len(req.Args) == 0 {
		return "", false, false
	}
	switch req.Method {
	case "box.start", "box.restart":
	case "box.attach":
		for _, name := range append([]string{"chat-allow-start"}, chatAttachIdentityFlags...) {
			if len(req.Flags[name]) > 0 {
				return "", false, false
			}
		}
	case "box.set", "box.reconcile":
		if flagTrue(req.Flags, "dry-run") {
			return "", false, false
		}
		apply = true
	default:
		return "", false, false
	}
	return req.Args[0], apply, true
}

// readLegacyIdentity reads the box's identity through the read-only
// box.inspect; ok is false when it cannot be read.
func readLegacyIdentity(cmd *cobra.Command, target controllerTarget, box string) (inspectIdentity, bool) {
	var report struct {
		Identity inspectIdentity `json:"identity"`
	}
	raw := boundedBuffer{Limit: 4 << 20}
	ctx, cancel := context.WithTimeout(cmd.Context(), recreateOfferInspectTimeout)
	defer cancel()
	inspect := controller.Request{ID: rand.Text(), Method: "box.inspect", Space: target.Space, Args: []string{box}, Dash: -1, Flags: map[string][]string{"json": {"true"}}}
	if err := target.Call(ctx, inspect, controller.ClientIO{Out: &raw, Err: &boundedBuffer{Limit: 64 << 10}}); err != nil || json.Unmarshal(raw.Bytes(), &report) != nil {
		return inspectIdentity{}, false
	}
	return report.Identity, true
}

// legacyRuntimeRunning reports a read showing a running pre-native box.
func legacyRuntimeRunning(cmd *cobra.Command, target controllerTarget, box string) bool {
	id, ok := readLegacyIdentity(cmd, target, box)
	return ok && id.LegacyRuntime && id.RuntimeState == string(boxstate.RuntimeRunning)
}

// offerLegacyRecreate runs after a selected request for a pre-native box
// failed and its refusal was printed. It asks only a person at the terminal,
// only when a fresh read shows the box pre-native and stopped (the refusal
// stopped it, or found it stopped); a read that fails or shows it running (an
// unconfirmed stop) asks nothing. A yes sends one ordinary `box recreate NAME
// --force --no-attach`; the original request is never sent again. A no, EOF
// or any other case keeps the refusal.
func offerLegacyRecreate(cmd *cobra.Command, target controllerTarget, box string, noAutoupdate bool, refusal error) error {
	if refusal == nil || !recreateOfferInteractive(cmd) || cmd.Context().Err() != nil {
		return refusal
	}
	id, ok := readLegacyIdentity(cmd, target, box)
	if !ok || !id.LegacyRuntime || id.RuntimeState != string(boxstate.RuntimeStopped) {
		return refusal
	}
	yes, err := promptYesNo(legacyRecreatePrompt(box), cmd.InOrStdin(), cmd.ErrOrStderr())
	if err != nil {
		return errors.Join(refusal, err)
	}
	if !yes {
		return refusal
	}
	return sendOfferedRecreate(cmd, target, box, noAutoupdate)
}

// legacyRecreatePrompt is the one confirmation for a pre-native box.
func legacyRecreatePrompt(box string) string {
	return "Recreation deletes files stored only in the container. Workdir, declared cache and account state are preserved. Recreate " + box + " now? [y/N]: "
}

// launcherRecreateOffered: the box carries the launcher_recreate reason
// (created before the session launcher, or the old upstream runtime), its session is
// managed (an agent or a declared session command) and this request starts a
// new one: a restart always does; a start or an attach does unless a session
// already runs (joining asks nothing). The chat server is not asked here.
func launcherRecreateOffered(id inspectIdentity, manifest *inspectManifest, drift inspectDrift, kind recreateOfferKind) bool {
	if kind == offerChatServer {
		return false
	}
	needs := false
	for _, reason := range drift.Reasons {
		needs = needs || strings.HasPrefix(reason, string(boxstate.ReasonLauncherRecreate)+":")
	}
	managed := id.Agent != "" && id.Agent != "none" || manifest != nil && len(manifest.SessionCommand) > 0
	newSession := kind == offerRestart || id.SessionState != string(boxstate.SessionRunning)
	return needs && managed && newSession
}

// sendOfferedRecreate sends the one ordinary recreate an affirmative answer
// approved: `box recreate NAME --force --no-attach`; the caller then sends
// its original request unchanged, only if this one succeeded.
func sendOfferedRecreate(cmd *cobra.Command, target controllerTarget, box string, noAutoupdate bool) error {
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
