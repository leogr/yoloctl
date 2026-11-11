package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// sessionRestartOffer is what offerSessionRestarts needs from the host: the
// updated controller's box inventory and managed room, its conditional box
// restart and chat ensure, and whether a person can answer a prompt.
type sessionRestartOffer struct {
	inventory func(context.Context) ([]boxstate.Box, error)
	// chatServer names the managed room's server box ("" without a room).
	chatServer func(context.Context) (string, error)
	// restart restarts name only while it still runs the listed holder;
	// startWorkload also starts its declared session_command again.
	restart func(ctx context.Context, name, holder string, startWorkload bool) error
	// drift resolves the full drift of only the named boxes (box.drift).
	drift func(ctx context.Context, names []string) ([]boxDriftView, error)
	// ensureChat is the ordinary `chat ensure`.
	ensureChat  func(context.Context) error
	interactive bool
	in          io.Reader
	errw        io.Writer
}

func hostSessionRestartOffer(cmd *cobra.Command, boot *spaceconfig.Bootstrap) sessionRestartOffer {
	return sessionRestartOffer{
		inventory: func(ctx context.Context) ([]boxstate.Box, error) {
			var view []controllerBoxView
			if err := controllerJSON(ctx, boot, "box.inventory", nil, nil, &view, cmd.ErrOrStderr()); err != nil {
				return nil, err
			}
			boxes := make([]boxstate.Box, 0, len(view))
			for _, b := range view {
				// The declaration says whether the session is a managed workload.
				b.State.Config = b.Config
				boxes = append(boxes, b.State)
			}
			return boxes, nil
		},
		chatServer: func(ctx context.Context) (string, error) {
			var status managedchat.Status
			if err := controllerJSON(ctx, boot, "chat.status", nil, nil, &status, cmd.ErrOrStderr()); err != nil {
				return "", err
			}
			if status.Room == nil {
				return "", nil
			}
			return status.Room.Server.Name, nil
		},
		restart: func(ctx context.Context, name, holder string, startWorkload bool) error {
			return callHostController(ctx, boot, sessionRestartRequest(boot.Name, name, holder, startWorkload),
				controller.ClientIO{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()})
		},
		drift: func(ctx context.Context, names []string) ([]boxDriftView, error) {
			var view []boxDriftView
			if err := controllerJSON(ctx, boot, boxDriftMethod, names, nil, &view, cmd.ErrOrStderr()); err != nil {
				return nil, err
			}
			return view, nil
		},
		ensureChat: func(ctx context.Context) error {
			req, err := chatEnsureRequest(boot.Name)
			if err != nil {
				return err
			}
			// A successful exit can still report an unready server: only its
			// typed, bounded status decides.
			raw := boundedBuffer{Limit: chatEnsureReplyLimit}
			if err := callHostController(ctx, boot, req, controller.ClientIO{Out: &raw, Err: cmd.ErrOrStderr()}); err != nil {
				return err
			}
			var status managedchat.Status
			if err := decodeBoundedJSON(&raw.Buffer, &status, raw.Limit); err != nil {
				return fmt.Errorf("invalid chat ensure status: %w", err)
			}
			return chatEnsureOutcome(status, cmd.ErrOrStderr())
		},
		// The prompt needs a person on both ends: a terminal to read the
		// answer from and one showing the question.
		interactive: isInteractive(cmd.InOrStdin()) && isTerminalWriter(cmd.ErrOrStderr()),
		in:          cmd.InOrStdin(),
		errw:        cmd.ErrOrStderr(),
	}
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// sessionRestartRequest is the ordinary `box restart NAME --no-attach`, bound
// to the listed holder: the shared session-aware stop and start, recorded
// under its own receipt, refused before teardown if that holder changed. A
// managed workload also gets its session started again.
func sessionRestartRequest(space, name, holder string, startWorkload bool) controller.Request {
	flags := map[string][]string{"no-attach": {"true"}, staleSessionFlag: {holder}}
	if startWorkload {
		flags[startWorkloadFlag] = []string{"true"}
	}
	return controller.Request{ID: rand.Text(), Method: "box.restart", Space: space, Args: []string{name}, Dash: -1, Flags: flags}
}

func sessionRestartCommand(space, name string) string {
	return fmt.Sprintf("yoloctl box restart %s --space %s --no-attach", name, space)
}

// chatEnsureRequest is exactly what a plain `yoloctl chat ensure` sends.
func chatEnsureRequest(space string) (controller.Request, error) {
	payload, err := json.Marshal(chatRuntimeRequest{DefaultHuman: defaultChatHuman()})
	if err != nil {
		return controller.Request{}, err
	}
	return controller.Request{ID: rand.Text(), Method: "chat.ensure", Space: space, Dash: -1, Payload: payload}, nil
}

// chatEnsureReplyLimit bounds the ensure status, as for chat commands.
const chatEnsureReplyLimit = 2 << 20

// chatEnsureOutcome accepts an ensure only when its final observation shows
// the room's server ready, keeping the reported pending reasons otherwise.
// Participant changes still pending behind a ready server are shown, not
// hidden.
func chatEnsureOutcome(status managedchat.Status, errw io.Writer) error {
	switch {
	case status.Room == nil:
		return errors.New("chat ensure reported no managed room")
	case !status.ServerReady:
		if len(status.Pending) > 0 {
			return fmt.Errorf("chat ensure reported the server not ready: %s", strings.Join(status.Pending, "; "))
		}
		return errors.New("chat ensure reported the server not ready")
	}
	if len(status.Pending) > 0 {
		cliNotice(logging.Host, "The chat server is ready; still pending: %s", strings.Join(status.Pending, "; "))
	}
	return nil
}

// sessionKind says what a restart must bring back.
type sessionKind int

const (
	// agentSession has no session until someone attaches or resumes it.
	agentSession sessionKind = iota
	// workloadSession is a declared session_command, started again by the
	// restart itself.
	workloadSession
	// chatServerSession is the managed room's server, started again through
	// chat ensure, which also confirms readiness and membership.
	chatServerSession
)

// staleSession is a running session whose holder answered as another build,
// with the incarnation its restart is bound to.
type staleSession struct {
	name, holder string
	kind         sessionKind
}

// recovery is the manual step that brings a restarted session's workload
// back, "" for an agent box.
func (s staleSession) recovery(space string) string {
	switch s.kind {
	case chatServerSession:
		return fmt.Sprintf("yoloctl chat ensure --space %s", space)
	case workloadSession:
		return fmt.Sprintf("yoloctl box attach %s --space %s", s.name, space)
	}
	return ""
}

// staleSessions splits running sessions into those of another build that
// can be bound to a holder incarnation, and those that are left running
// because they could not be asked or identified. chatServer is the room's
// server box; when the room could not be read (!chatKnown), a declared
// session_command cannot be classified, so that box is left running too.
func staleSessions(boxes []boxstate.Box, chatServer string, chatKnown bool) (stale []staleSession, unknown []string) {
	for _, b := range boxes {
		if b.Session.State != boxstate.SessionRunning {
			continue
		}
		workload := b.Config != nil && len(b.Config.SessionCommand) > 0
		switch {
		case b.Session.Build == boxstate.SessionBuildCurrent:
		case b.Session.Build != boxstate.SessionBuildOther || b.Session.Holder == "" || workload && !chatKnown:
			unknown = append(unknown, b.Name)
		case chatServer != "" && b.Name == chatServer:
			stale = append(stale, staleSession{b.Name, b.Session.Holder, chatServerSession})
		case workload:
			stale = append(stale, staleSession{b.Name, b.Session.Holder, workloadSession})
		default:
			stale = append(stale, staleSession{b.Name, b.Session.Holder, agentSession})
		}
	}
	return stale, unknown
}

// offerSessionRestarts runs after a successful controller update. Sessions
// started by the previous build keep running, but this build cannot attach
// to them. It lists the boxes whose holder answered as another build and
// restarts them only on an explicit yes at a terminal or with explicit set
// (--restart-sessions). Anything else, scripts included, only gets the
// commands. Sessions it could not ask or identify are named and never
// restarted, and a failed listing restarts nothing. Before each restart it
// lists again and skips a box whose session changed; the controller checks
// the same binding again before teardown.
func offerSessionRestarts(ctx context.Context, space string, o sessionRestartOffer, explicit bool) error {
	o.errw = cliTerminalWriter(o.errw)
	boxes, err := o.inventory(ctx)
	if err != nil {
		// The update itself succeeded; only a requested restart fails with it.
		if explicit {
			return fmt.Errorf("controller updated, but its agent sessions could not be listed, so none was restarted: %w", err)
		}
		cliWarn(logging.Host, "controller updated, but its agent sessions could not be listed (%v); check them with yoloctl box list --space %s", err, space)
		return nil
	}
	chatServer, chatErr := o.chatServer(ctx)
	if chatErr != nil {
		cliWarn(logging.Host, "the managed chat room could not be read (%v); boxes with a managed workload are left running", chatErr)
	}
	stale, unknown := staleSessions(boxes, chatServer, chatErr == nil)
	if len(unknown) > 0 {
		cliWarn(logging.Host, "Could not check or identify the agent sessions of %s; they are left running.", strings.Join(unknown, ", "))
	}
	conversations := map[string]*agentsession.Status{}
	for _, b := range boxes {
		conversations[b.Name] = b.Conversation
	}
	if len(stale) > 0 {
		stale = setAsideRecreations(ctx, space, o, stale, conversations)
	}
	if len(stale) == 0 {
		return nil
	}
	names := make([]string, len(stale))
	for i, s := range stale {
		names[i] = s.name
	}
	cliNotice(logging.Host, "These boxes still run agent sessions of the previous build, which this build cannot attach to: %s.", strings.Join(names, ", "))
	cliWarn(logging.Host, "Restarting a box ends its agent's current process.")
	for _, s := range stale {
		if s.kind == agentSession {
			_, _ = fmt.Fprintf(o.errw, "%s: %s\n", s.name, restartConversationNote(conversations[s.name]))
		}
		switch s.kind {
		case chatServerSession:
			cliNotice(logging.Host, "%s is the managed chat server: restarting it restarts the room on this build, and every connected chat client must reconnect.", s.name)
		case workloadSession:
			cliNotice(logging.Host, "%s runs a managed workload, which is started again after its restart.", s.name)
		}
	}
	commands := func(lead string) {
		_, _ = fmt.Fprintln(o.errw, lead)
		for _, s := range stale {
			_, _ = fmt.Fprintf(o.errw, "  %s\n", sessionRestartCommand(space, s.name))
			switch s.kind {
			case chatServerSession:
				_, _ = fmt.Fprintf(o.errw, "  %s   # then start the chat room again on this build\n", s.recovery(space))
			case workloadSession:
				_, _ = fmt.Fprintf(o.errw, "  %s   # then start its managed workload again, and detach\n", s.recovery(space))
			}
		}
	}
	if !explicit {
		if !o.interactive {
			commands("Nothing was restarted. To restart them:")
			return nil
		}
		ok, err := promptYesNo(fmt.Sprintf("Restart %s now? [y/N]: ", boxCount(len(stale))), o.in, o.errw)
		if err != nil {
			return err
		}
		if !ok {
			commands("Left running. To restart them later:")
			return nil
		}
	}
	restarted := 0
	for i, s := range stale {
		notAttempted := func(err error) error {
			if rest := names[i+1:]; len(rest) > 0 {
				return fmt.Errorf("restart box %s (not attempted: %s): %w", s.name, strings.Join(rest, ", "), err)
			}
			return fmt.Errorf("restart box %s: %w", s.name, err)
		}
		// The answer may have taken a while: list again and skip a session
		// that another restart replaced in the meantime.
		now, err := o.inventory(ctx)
		if err != nil {
			return notAttempted(fmt.Errorf("list agent sessions again: %w", err))
		}
		current, _ := staleSessions(now, chatServer, chatErr == nil)
		if !containsStale(current, s) {
			cliWarn(logging.Host, "Skipped box %s: its session changed since it was listed; left running.", s.name)
			continue
		}
		cliNotice(logging.Host, "Restarting box %s", s.name)
		// A failed restart may already have stopped a workload: say how to
		// bring it back.
		down := func(what string) {
			if step := s.recovery(space); step != "" {
				cliWarn(logging.Host, "%s may be down. Recover it with:\n%s", what, step)
			}
		}
		if err := o.restart(ctx, s.name, s.holder, s.kind == workloadSession); err != nil {
			down(map[sessionKind]string{chatServerSession: "The chat room", workloadSession: "The managed workload of " + s.name}[s.kind])
			return notAttempted(err)
		}
		restarted++
		if s.kind == chatServerSession {
			if err := o.ensureChat(ctx); err != nil {
				down("The chat room")
				return notAttempted(fmt.Errorf("start the chat room again: %w", err))
			}
			cliNotice(logging.Host, "The chat room was restarted on this build; every connected chat client must reconnect.")
		}
	}
	if restarted > 0 {
		cliNotice(logging.Host, "Restarted %s on this build. A tracked conversation is requested on the box's next attach and stays unverified until its agent reports it; resume an untracked one yourself.", boxCount(restarted))
	}
	return nil
}

// setAsideRecreations leaves running, with their explicit remedy, the stale
// boxes whose declarations also require recreation, which a restart refuses;
// the rest stay in the offer. It never recreates anything. When drift cannot
// be read every box stays: each restart still refuses recreation drift
// before its teardown.
func setAsideRecreations(ctx context.Context, space string, o sessionRestartOffer, stale []staleSession, conversations map[string]*agentsession.Status) []staleSession {
	names := make([]string, len(stale))
	for i, s := range stale {
		names[i] = s.name
	}
	view, err := o.drift(ctx, names)
	if err != nil {
		cliWarn(logging.Host, "could not check whether %s also require recreation (%v); a restart refuses a box that does", strings.Join(names, ", "), err)
		return stale
	}
	drift := make(map[string]boxDriftView, len(view))
	for _, v := range view {
		drift[v.Name] = v
	}
	var keep []staleSession
	for _, s := range stale {
		remedy, reason := remedyUnknown, "not reported"
		if v, ok := drift[s.name]; ok {
			remedy, reason = v.remedy()
		}
		switch remedy {
		case remedyRestart:
			keep = append(keep, s)
			continue
		case remedyUnknown:
			cliWarn(logging.Host, "Could not check whether box %s also requires recreation (%s); its restart refuses it if so.", s.name, reason)
			keep = append(keep, s)
			continue
		}
		cliWarn(logging.Host, "Box %s runs an agent session of the previous build and also requires recreation (%s), which a restart refuses, so it is left running. Recreation ends its agent's current process and replaces its container: the workdir and the account's native state are kept, other files in the container are lost. Inspect it, then recreate it explicitly if replacement is intended:", s.name, reason)
		_, _ = fmt.Fprintf(o.errw, "  yoloctl box inspect %s --space %s\n  yoloctl box recreate %s --space %s --no-attach\n", s.name, space, s.name, space)
		switch s.kind {
		case chatServerSession:
			_, _ = fmt.Fprintf(o.errw, "  %s   # then start the chat room again on this build\n", s.recovery(space))
		case workloadSession:
			_, _ = fmt.Fprintf(o.errw, "  %s   # then start its managed workload again, and detach\n", s.recovery(space))
		default:
			_, _ = fmt.Fprintf(o.errw, "%s: %s\n", s.name, restartConversationNote(conversations[s.name]))
		}
	}
	return keep
}

// restartConversationNote says what the next session of a restarted agent
// box does with its conversation. Nothing claims a resume succeeded.
func restartConversationNote(c *agentsession.Status) string {
	switch {
	case c == nil:
		return "its conversation is not known here; resume it yourself after the restart"
	case c.Tracked && c.Switched() && c.Note == "":
		return fmt.Sprintf("the next attach requests conversation %s, the one the agent last reported (policy %s)", c.Current.ID, c.Policy)
	case c.Tracked && c.ID != "" && c.Note == "":
		return fmt.Sprintf("the next attach requests conversation %s again (policy %s)", c.ID, c.Policy)
	case c.Note == agentsession.Untracked:
		return "its conversation started before tracking, so the next attach starts a new tracked one; resume the old one yourself if needed"
	case c.Note != "":
		return "its conversation is not resumed automatically (" + c.Note + ")"
	}
	return "its next attach starts a new tracked conversation"
}

func containsStale(list []staleSession, s staleSession) bool {
	for _, c := range list {
		if c == s {
			return true
		}
	}
	return false
}

func boxCount(n int) string {
	if n == 1 {
		return "1 box"
	}
	return fmt.Sprintf("%d boxes", n)
}

// staleSessionFlag is box restart's hidden precondition for the
// post-update offer: the listed holder incarnation. startWorkloadFlag also
// starts a declared session_command again after that restart.
const (
	staleSessionFlag  = "if-stale-session"
	startWorkloadFlag = "start-workload"
)

// errStaleSessionChanged refuses a conditional restart before teardown.
var errStaleSessionChanged = errors.New("its session changed since it was listed; nothing was restarted")

// checkStaleSession decides the precondition from one probe of the box's
// session: it must still run the listed holder, and that holder must still
// answer as another build.
func checkStaleSession(name, listed string, running bool, build boxstate.SessionBuild, holder string) error {
	switch {
	case !running:
		return asUserError(fmt.Errorf("box %s: its listed session is no longer running; %w", name, errStaleSessionChanged))
	case holder != listed:
		return asUserError(fmt.Errorf("box %s: its holder was replaced; %w", name, errStaleSessionChanged))
	case build != boxstate.SessionBuildOther:
		return asUserError(fmt.Errorf("box %s: its holder now answers as %s build; %w", name, build, errStaleSessionChanged))
	}
	return nil
}

// requireStaleSession runs the precondition inside the restart command,
// under the space operation lock that excludes every other managed
// mutation, so no session can be started or replaced between this probe
// and the teardown that follows it.
func requireStaleSession(ctx context.Context, c *container.Client, name, listed string) error {
	if !spaceenv.Native() {
		return asUserError(fmt.Errorf("--%s is checked only by the space controller", staleSessionFlag))
	}
	running := session.HolderUnitActive(ctx, c, name)
	build, holder := boxstate.SessionBuildUnknown, ""
	if running {
		build, holder = boxstate.ProbeHolder(ctx, c, name)
	}
	return checkStaleSession(name, listed, running, build, holder)
}

// requireRestartableWorkload refuses --start-workload before any teardown
// unless the box declares a session_command workload other than the managed
// chat server, which only chat ensure may start: it also confirms readiness
// and membership.
func requireRestartableWorkload(space, name string) error {
	cfg, err := loadManagedAgentbox(space, name)
	if err != nil {
		return err
	}
	return restartableWorkload(cfg, space)
}

func restartableWorkload(cfg *agentbox.Config, space string) error {
	switch {
	case len(cfg.SessionCommand) == 0:
		return asUserError(fmt.Errorf("box %s declares no session_command workload to start again", cfg.Name))
	case slices.Equal(cfg.SessionCommand, agentbox.ChatServerCommand()):
		return asUserError(fmt.Errorf("box %s is the managed chat server; start it again with yoloctl chat ensure --space %s", cfg.Name, space))
	}
	return nil
}

// startWorkloadSession is the shared session start that attach also uses.
var startWorkloadSession = ensureAgentSession

// startRestartedWorkload starts a restarted box's session_command again
// through the shared session start, without attaching.
func startRestartedWorkload(ctx context.Context, c *container.Client, space, name string, errw io.Writer) error {
	updated := false
	if _, err := startWorkloadSession(ctx, c, space, name, errw, true, &updated, sessionStartOptions{}); err != nil {
		return fmt.Errorf("box %s restarted, but its managed workload did not start again (recover with yoloctl box attach %s --space %s): %w", name, name, space, err)
	}
	cliNotice(logging.Host, "Started the managed workload of box %s again on this build.", name)
	return nil
}
