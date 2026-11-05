package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/vmservice"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var ensureVMBinaryFn = session.EnsureVMBinary
var holderUnitActiveFn = session.HolderUnitActive
var dialSessionVMFn = session.DialVM
var nativeSessionTransportFn = spaceenv.Native
var holderStartupTimeout = 10 * time.Second

type preparedSessionBinaryKey struct{}
type controllerViewKey struct{}

func ensureSessionBinary(ctx context.Context, c *container.Client) error {
	if space, ok := ctx.Value(preparedSessionBinaryKey{}).(string); ok && space == c.Env() {
		return nil
	}
	return ensureVMBinaryFn(ctx, c)
}

// Preflight only operations that will open a managed session, before changing
// the box. The context memo lasts for this command's space-locked operation;
// it is never reused across commands or VM recreation.
func prepareAutoAttach(cmd *cobra.Command, space, name string, noAttach, replacing bool) error {
	if noAttach || !stdinIsTerminalFn(cmd.InOrStdin()) {
		return nil
	}
	protected, err := operatorManagedServerTargetFn(cmd.Context(), space, name)
	if err != nil {
		return err
	}
	if protected {
		return nil // finite lifecycle remains available without server attachment
	}
	c := container.New(space)
	if !replacing && holderUnitActiveFn(cmd.Context(), c, name) {
		return nil
	}
	if err := ensureSessionBinary(cmd.Context(), c); err != nil {
		return err
	}
	cmd.SetContext(context.WithValue(cmd.Context(), preparedSessionBinaryKey{}, space))
	return nil
}

// Host side of the session layer: dialing an agentbox's
// session holder, and the ensure ladder that makes `box attach`
// kubectl-attach-shaped (replay + live, lazily reviving the session
// when the box has none).

func selectSessionTransport(ctx context.Context, space, name string) (io.ReadWriteCloser, error) {
	if err := guardOperatorChatExecution(ctx, space, name); err != nil {
		return nil, err
	}
	dialCtx := ctx
	cancel := func() {}
	if nativeSessionTransportFn() {
		// Every native selection uses a short-lived dial context, including
		// guarded human attachments that hold the space lock. The host bridge
		// subprocess needs the parent context for its entire stream.
		dialCtx, cancel = context.WithTimeout(ctx, operatorTargetSetupTimeout)
	}
	transport, err := dialSessionVMFn(dialCtx, space, name)
	cancel()
	if err != nil {
		return nil, err
	}
	// The native target socket is now selected. Releasing before DialVM could
	// let a delayed attachment follow a name replaced by the managed server.
	if err := releaseSpaceOperation(ctx); err != nil {
		_ = transport.Close()
		return nil, err
	}
	return transport, nil
}

// attachToSession attaches the local terminal to the agentbox's agent
// session: raw mode, SIGWINCH→RESIZE forwarding, replay-then-live
// stream. Returns nil when the agent exits 0, *session.ExitError for a
// nonzero exit (classifyError propagates the status as yoloctl's own
// exit code), and session.ErrSessionClosed when the holder goes away
// mid-attach.
func attachToSession(ctx context.Context, space, name string) error {
	transport, err := selectSessionTransport(ctx, space, name)
	if err != nil {
		return err
	}
	if view, ok := ctx.Value(controllerViewKey{}).(*os.File); ok {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			state, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				_ = transport.Close()
				return err
			}
			defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
		}
		return staleSessionGuidance(ctx, space, name, session.Relay(ctx, transport, view, os.Stdin))
	}
	aio := session.AttachIO{Stdin: os.Stdin, Stdout: os.Stdout}
	// A direct attach owns the real host terminal, so it is the OSC 52
	// enforcement point. A child launched inside the embedded TUI has only a
	// nested PTY (YOLOCTL_TUI=1): leave OSC 52 intact there so the outer TUI
	// can apply the same policy and emit an allowed sequence to the actual
	// host terminal rather than letting midterm consume it.
	embedded := os.Getenv("YOLOCTL_TUI") == "1"
	var filter *termio.OSC52Filter
	if !embedded {
		tuiCfg, cfgErr := config.LoadTUI()
		if cfgErr != nil {
			_ = transport.Close()
			return cfgErr
		}
		var disabledNotice sync.Once
		filter = termio.NewOSC52Filter(os.Stdout, func(event termio.OSC52) {
			sequence, notice := termio.EvaluateOSC52(event, tuiCfg.ClipboardEnabled())
			if sequence != "" {
				_, _ = io.WriteString(os.Stdout, sequence)
				return
			}
			if notice == "" {
				return
			}
			writeNotice := func() {
				_, _ = fmt.Fprintf(os.Stderr, "\r\n⚠ %s\r\n", notice)
			}
			if notice == termio.OSC52DisabledNotice {
				disabledNotice.Do(writeNotice)
			} else {
				writeNotice()
			}
		})
		aio.Stdout = filter
		defer func() { _ = filter.Flush() }()
	}
	// Track whether the agent painted anything: the post-attach terminal
	// reset below is pointless (and would jump the cursor to the bottom of
	// a clean screen) when the stream died before a single output byte.
	painted := &paintedWriter{w: aio.Stdout}
	aio.Stdout = painted
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		oldState, rawErr := term.MakeRaw(fd)
		if rawErr == nil {
			defer func() { _ = term.Restore(fd, oldState) }()
		}
		resize := make(chan terminal.Geometry, 1)
		aio.Resize = resize
		push := func() {
			cols, rows, sizeErr := term.GetSize(fd)
			if sizeErr != nil {
				return
			}
			// Latest geometry wins: drop a queued update, never block.
			select {
			case <-resize:
			default:
			}
			select {
			case resize <- terminal.Geometry{Cols: cols, Rows: rows}:
			default:
			}
		}
		push() // the holder's PTY starts at default size; correct it now
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				push()
			}
		}()
	}
	err = session.Attach(ctx, transport, aio)
	if reset := postAttachReset(err, term.IsTerminal(int(os.Stdout.Fd())), painted.painted(), embedded); reset != "" {
		// Drain any partial OSC 52 first: an unterminated escape trailing
		// the reset would swallow the error/notice printed after it. The
		// deferred Flush then finds nothing left.
		if filter != nil {
			_ = filter.Flush()
		}
		_, _ = io.WriteString(os.Stdout, reset)
	}
	return staleSessionGuidance(ctx, space, name, err)
}

// postAttachReset returns the terminal-normalization bytes to emit after
// session.Attach returned err, or "" when none are needed. An agent that
// ends without restoring the screen — holder killed by box stop/restart
// (ErrSessionClosed), agent killed inside the box (ExitError), or a detach
// (context.Canceled) — leaves the terminal mid-frame with the cursor parked
// inside its dead UI; every message yoloctl prints next (Cobra's "Error:",
// finishAttach's detach notice) would otherwise splice into leftover frame
// rows. The reset re-parks on the bottom row and the trailing newlines put
// a blank separator between the corpse and the message. Embedded in a TUI
// tab, the reset stays on whichever screen the agent died on so the outer
// tab keeps showing the final frame above the message; standalone it also
// returns the host terminal to the main screen so the shell resumes
// normally. Skipped when the agent exited cleanly (it restored the screen
// itself; err == nil prints nothing), when stdout is not a terminal
// (redirected output must not collect escapes), and when the agent never
// painted (pre-HELLO failures on a clean screen).
func postAttachReset(err error, stdoutIsTerminal, agentPainted, embedded bool) string {
	if err == nil || !stdoutIsTerminal || !agentPainted {
		return ""
	}
	if embedded {
		return termio.SessionEndResetInPlace + "\r\n\r\n"
	}
	return termio.SessionEndResetHost + "\r\n\r\n"
}

// paintedWriter passes bytes through and remembers that at least one write
// happened. Atomic because the attach reader goroutine may still be writing
// when Attach returns on a detach (ctx cancellation).
type paintedWriter struct {
	w   io.Writer
	set atomic.Bool
}

func (p *paintedWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		p.set.Store(true)
	}
	return p.w.Write(b)
}

func (p *paintedWriter) painted() bool { return p.set.Load() }

// finishAttach translates an interactive attach's outcome: context
// cancellation (SIGINT/SIGTERM — closing the terminal, `kill`, a
// wrapping `timeout`) is a DETACH, the normal way to leave a session,
// not an error. Say what detaching means and exit 0. Everything else
// (session exit status, holder loss) passes through. Runs after
// attachToSession restored the terminal, so the notice renders sanely.
// The reattach hint is always space-qualified so it works verbatim
// regardless of the caller's pinned/default space.
func finishAttach(err error, errw io.Writer, space, name string) error {
	if errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(errw, "\ndetached — the session keeps running; reattach: yoloctl box attach %s --space %s\n", name, space)
		return nil
	}
	return err
}

// shouldWarnPostAutoupdate reports whether an attach outcome right after an
// autoupdate deserves warnPostAutoupdateFailure: the update actually ran AND
// the session ended with a nonzero exit (a fresh agent version rejecting
// on-disk state an older one wrote is the usual culprit). A detach
// (finishAttach → nil) or a clean exit warns nothing. ExitError implements
// error on the pointer receiver, so the errors.As target must be **ExitError.
func shouldWarnPostAutoupdate(updated bool, attachErr error) bool {
	if !updated {
		return false
	}
	var exitErr *session.ExitError
	return errors.As(attachErr, &exitErr)
}

// ensureAndAttachSession is the ensure-then-attach tail shared by `box
// attach` and the lifecycle auto-attach
// (create/copy/start/restart/recreate): revive the session as needed, attach
// this terminal, translate a detach into exit 0, and surface the
// post-autoupdate failure correlation. alreadyUpdated seeds that correlation
// for callers whose own pipeline autoupdated the box before attaching
// (ensureAgentSession only updates when IT revives the container).
func ensureAndAttachSession(ctx context.Context, c *container.Client, space, name string, errw io.Writer, noAutoupdate, alreadyUpdated bool, opts sessionStartOptions) error {
	if err := guardOperatorChatExecution(ctx, space, name); err != nil {
		return err
	}
	agentUpdated := alreadyUpdated
	agentName, err := ensureAgentSession(ctx, c, space, name, errw, noAutoupdate, &agentUpdated, opts)
	if err != nil {
		return err
	}
	attachErr := finishAttach(attachToSession(ctx, space, name), errw, space, name)
	if shouldWarnPostAutoupdate(agentUpdated, attachErr) {
		var se *session.ExitError
		if errors.As(attachErr, &se) {
			warnPostAutoupdateFailure(errw, name, agentName, se.Status)
		}
	}
	return attachErr
}

// ensureAgentSession makes the named agentbox have a live session,
// reviving box and session as needed (the deliberate divergence from
// strict kubectl-attach: the session IS the box's main process, so
// ensure-semantics keep day-2 UX one command):
//
//  1. Holder unit active → done.
//  2. Tombstone present → report the detached exit once, clear it.
//  3. Marker without a live unit while the container runs → the holder
//     died and the session is an unrecoverable orphan (spike F2): error,
//     pointing at recreate. yoloctl cannot adopt it and must not start
//     a second session next to it.
//  4. Container stopped → start it (same steps as `box start`,
//     autoupdate included) and clear now-stale session state: a fresh
//     sandbox means the previous session is definitively gone.
//  5. Start the holder unit (argv resolved from the agent definition plus
//     the box's persistent extra args — or the built-in login shell for a
//     generic-shell box) and wait for its socket.
func ensureAgentSession(ctx context.Context, c *container.Client, space, name string, errw io.Writer, noAutoupdate bool, updated *bool, opts sessionStartOptions) (agent string, err error) {
	if !opts.NewSession && holderUnitActiveFn(ctx, c, name) {
		if err := gateWithoutImageBuild(ctx); err != nil {
			return "", err
		}
		bindLiveLaunch(ctx, c, space, name)
		foldLiveLaunch(ctx, space, name)
		return "", nil
	}

	resolved, err := newBoxStateResolver().ResolveOne(ctx, space, name, boxstate.RuntimeSummary)
	if err != nil {
		return "", err
	}
	if resolved.Declaration == boxstate.DeclarationMissing {
		return "", asUserError(fmt.Errorf(
			"no box %q in space %q — create one with `yoloctl box create %s --agent AGENT`",
			name, space, name))
	}
	if resolved.Declaration == boxstate.DeclarationInvalid {
		return "", asUserError(fmt.Errorf("box %q declaration is invalid: %s", name, resolved.ConfigError))
	}
	cfg := resolved.Config
	running := resolved.Runtime == boxstate.RuntimeRunning

	// A tracked box's conversation record changes only under its box lock,
	// taken inside the command's space-operation lock and held until the
	// holder is bound; agentless, unsupported and manual boxes take no lock.
	tracking, _, err := sessionTrackingFor(cfg)
	if err != nil {
		return "", err
	}
	if opts.NewSession && tracking == nil {
		return "", asUserError(fmt.Errorf("box %s does not track its agent conversation (resume policy manual or unsupported); --new-session has nothing to replace", name))
	}
	// A stopped box refuses recreate-only drift before any image work; the
	// running branch begins effects only after its holder decisions and the
	// fresh-session image guard below.
	if !running {
		if err := refuseRecreateBeforeImageWork(ctx, space, cfg); err != nil {
			return "", err
		}
		if err := warmBoxBeforeEffects(ctx, c, cfg, true); err != nil {
			return "", err
		}
	}
	// Identity decisions need definite holder evidence: an undecidable unit
	// or marker probe refuses before any record change or launch, and
	// --new-session refuses a live holder (only restart replaces one).
	var tracked *trackedLaunch
	var trackedMarker session.Marker
	trackedMarkerPresent := false
	if tracking != nil {
		active, err := holderUnitStateFn(ctx, c, name)
		if err != nil {
			return "", asUserError(fmt.Errorf("cannot tell whether a session runs in box %s (%v); nothing was changed", name, err))
		}
		if active {
			if opts.NewSession {
				return "", asUserError(fmt.Errorf("a session is running in box %s; replace its conversation with yoloctl box restart %s --new-session", name, name))
			}
			if running {
				if err := gateWithoutImageBuild(ctx); err != nil {
					return "", err
				}
			}
			bindLiveLaunch(ctx, c, space, name)
			foldLiveLaunch(ctx, space, name)
			return "", nil
		}
		if running {
			m, present, err := probeVMMarkerFn(ctx, c, name)
			if err != nil {
				return "", asUserError(fmt.Errorf("cannot tell whether a session of box %s survives (%v); nothing was changed — inspect it, or recover with yoloctl box recreate %s", name, err, name))
			}
			trackedMarker, trackedMarkerPresent = m, present
		}
	}
	// A living holder was handled above. Before any fresh launch, record change,
	// stopped-container start or npm maintenance, classify recipe-image drift
	// through the shared full resolver. A proven tracked marker still takes the
	// existing lost-holder path rather than being treated as a fresh launch.
	if !trackedMarkerPresent && (resolved.Runtime == boxstate.RuntimeRunning || resolved.Runtime == boxstate.RuntimeStopped) {
		if err := guardFreshSessionImage(ctx, space, name); err != nil {
			return "", err
		}
	}
	if running {
		if err := gateWithoutImageBuild(ctx); err != nil {
			return "", err
		}
	}
	var prepared recordedNativeStart
	defer func() { closeNativeStart(prepared) }()
	stoppedNative := false
	if resolved.Runtime == boxstate.RuntimeStopped {
		prepared, stoppedNative, err = prepareNativeExistingStart(ctx, c, cfg, true)
		if err != nil {
			return "", err
		}
	}
	if tracking != nil {
		if tracked, err = beginTrackedSession(ctx, tracking, space, name); err != nil {
			return "", err
		}
		defer tracked.Release()
	}

	if stoppedNative {
		if err := withdrawNativeStartForwards(ctx, c, name); err != nil {
			return "", err
		}
	}

	// Report the previous session's exit exactly once, then clear the
	// tombstone. The holder writes it on every real exit — also when a
	// client observed the exit live — so the wording must not claim
	// the user was detached. A tracked launch closes first, so a crash
	// before the removal only repeats the close.
	if ts, ok := readVMTombstoneFn(ctx, c, name); ok {
		_, _ = fmt.Fprintf(errw, "notice: previous session exited with status %d at %s; starting a fresh one\n",
			ts.ExitCode, ts.ExitedAt.Format(time.RFC3339))
		if tracked != nil {
			_, _ = fmt.Fprint(errw, tracked.exitHint(ts))
			if err := tracked.noteExit(ts); err != nil {
				return "", err
			}
		}
		_ = c.Shell(ctx, "sudo", "rm", "-f", session.TombstonePath(name))
	}

	markerPresent := trackedMarkerPresent
	if tracked == nil {
		markerPresent = c.Shell(ctx, "sudo", "test", "-f", session.MarkerPath(name)) == nil
	}
	if running && markerPresent {
		// Box up, marker on disk, no live unit: the holder died (spike
		// F2). The session command may still be running headless, its
		// output drained and its exit status lost; openshell-sandbox has
		// no reattach API, so it cannot be adopted — and starting a
		// second session next to it would be worse.
		if tracked != nil {
			if err := tracked.noteHolderLost(trackedMarker); err != nil {
				return "", err
			}
		}
		return "", asUserError(fmt.Errorf(
			"the session holder for %q died and its session cannot be re-attached (no reattach API in openshell-sandbox). "+
				"Recreate the box to recover — workdir and account state survive: yoloctl box recreate %s",
			name, name))
	}

	// A missing/mismatched bundle or incompatible Linux loader must fail
	// before starting/rebuilding the container or running npm maintenance.
	if err := ensureSessionBinary(ctx, c); err != nil {
		return "", err
	}

	if !running {
		fresh := false
		switch resolved.Runtime {
		case boxstate.RuntimeMissing:
			if err := preflightAgentboxRuntime(ctx, c, cfg, true); err != nil {
				return "", err
			}
			inspector := agentbox.LiveInspector{Client: c}
			applier := newManagedLiveApplier(c, errw)
			if _, err := agentbox.Reconcile(ctx, cfg, inspector, applier); err != nil {
				return "", err
			}
			fresh = true
		case boxstate.RuntimeStopped:
			if err := preflightAgentboxRuntimeQualified(ctx, c, cfg, true, func(check context.Context, declared *agentbox.Config) (bool, error) {
				if prepared != nil {
					return true, prepared.Check(check, declared)
				}
				return false, nil
			}); err != nil {
				return "", err
			}
			if err := startExistingAgentboxContainerPrepared(ctx, c, cfg, prepared); err != nil {
				return "", err
			}
			fresh = true
		case boxstate.RuntimeUnavailable:
			return "", asUserError(fmt.Errorf("box %q runtime is unavailable in space %q: %s",
				name, space, boxStateReasonField(resolved)))
		default:
			if err := startExistingAgentboxContainer(ctx, c, cfg); err != nil {
				return "", err
			}
		}
		if fresh && tracked != nil {
			if err := tracked.noteSandboxReplaced("the box was started from a stopped or missing runtime"); err != nil {
				return "", err
			}
		}
		if !noAutoupdate {
			if err := autoupdateAgentboxIfNeeded(ctx, c, name, cfg.Agent, errw, updated); err != nil {
				return "", fmt.Errorf("autoupdate %s: %w", name, err)
			}
		}
		// A fresh sandbox means any leftover marker is stale.
		_ = c.Shell(ctx, "sudo", "rm", "-rf", session.StateDirFor(name))
	}

	// A server child can exit normally while its container keeps running.
	// Such holder-only revival bypasses container preparation, so verify the
	// same current publication here before launching a new server process.
	if err := agentbox.VerifyChatServerPublication(cfg); err != nil {
		return "", err
	}
	termName, cols, rows := sessionTermGeometry()
	ringBytes, titleTemplate, titleStrip := sessionHolderTuning(ctx, errw, name, space, cfg.Agent, cfg.Account)
	daemons, err := sessionDaemons(cfg)
	if err != nil {
		return "", err
	}
	start := func(argv []string, token string) error {
		if err := session.StartHolderUnit(ctx, c, session.UnitSpec{
			Agentbox:        name,
			Argv:            sessionTerminalIdentity(ctx).Command(argv),
			Term:            termName,
			Cols:            cols,
			Rows:            rows,
			RingBytes:       ringBytes,
			TitleTemplate:   titleTemplate,
			TitleStrip:      titleStrip,
			HostIntegration: true,
			HostWorkdir:     sessionHostWorkdir(cfg.Workdir),
			LaunchToken:     token,
			Daemons:         daemons,
		}); err != nil {
			return fmt.Errorf("start session holder for %s: %w", name, err)
		}
		return waitForAttachSocket(ctx, c, name)
	}
	if tracked != nil {
		if err := tracked.observeCurrentLaunch(ctx, cfg); err != nil {
			return "", err
		}
		kind, err := tracked.plan(opts, errw)
		if err != nil {
			return "", err
		}
		if kind != "" {
			return cfg.Agent, tracked.launch(ctx, c, cfg, kind, start)
		}
	}
	argv, err := resolveSessionArgv(space, cfg)
	if err != nil {
		return "", err
	}
	return cfg.Agent, start(argv, "")
}

// sessionDaemons is the declared daemons of the box's agent, which the holder
// stops around every launch, manual ones included; agentless boxes have none.
func sessionDaemons(cfg *agentbox.Config) ([]string, error) {
	if !cfg.HasAgent() {
		return nil, nil
	}
	profile, err := lookupAgentProfile(cfg.Agent)
	if err != nil {
		return nil, err
	}
	return profile.SessionDaemons(), nil
}

func sessionHostWorkdir(w agentbox.Workdir) string {
	if w.IsHost() {
		return w.MountSource()
	}
	return ""
}

// resolveSessionArgv rebuilds the persisted session command line: for a real
// agent, the profile command + the profile's DefaultArgs + the box's
// AgentExtraArgs, with the profile's launch_env prefix applied (so claude's
// ~/.claude.json relocation also holds on recreate/attach, not just on the
// initial run). A generic-shell box (agent none) revives yoloctl's built-in
// login shell unless SessionCommand supplies explicit argv. A box originally
// run with a custom positional `-- COMMAND` revives with these defaults —
// argv is not persisted (documented in `box attach --help`).
func resolveSessionArgv(_ string, cfg *agentbox.Config) ([]string, error) {
	if !cfg.HasAgent() {
		if len(cfg.SessionCommand) > 0 {
			return applyLaunchEnv(nil, append([]string(nil), cfg.SessionCommand...)), nil
		}
		return applyLaunchEnv(nil, genericShellArgv()), nil
	}
	profile, err := lookupAgentProfile(cfg.Agent)
	if err != nil {
		return nil, err
	}
	argv := append([]string{profile.Command}, profile.DefaultArgs...)
	argv = append(argv, cfg.AgentExtraArgs...)
	return applyLaunchEnv(profile, argv), nil
}

// sessionHolderTuning reads tui.yaml for a new holder: the replay-ring
// size and the box title template, expanded for this box's identity and
// agent icon, plus the icon the holder strips from agent titles.
// Zero values (no file / unset / unreadable) keep the holder defaults —
// 1 MiB ring, "<box>: <title>" composition. An invalid template or icon
// warns once and falls back to the default composition rather than
// blocking the session. Applied at holder start only — never
// retroactively.
func sessionHolderTuning(ctx context.Context, errw io.Writer, box, space, agent, account string) (ringBytes int, titleTemplate, titleStrip string) {
	t := &config.TUI{}
	icons := t.AgentIconGlyphs()
	if spaceenv.Native() {
		if prefs, _ := ctx.Value(controllerSessionKey{}).(*controller.SessionPreferences); prefs != nil {
			t.Session = config.TUISession{RingBufferBytes: prefs.RingBytes, BoxTitleTemplate: prefs.TitleTemplate}
			icons = prefs.AgentIcons // already resolved against the defaults host-side
		}
	} else {
		var err error
		t, err = config.LoadTUI()
		if err != nil {
			return 0, "", ""
		}
		icons = t.AgentIconGlyphs()
	}
	expanded, strip, err := session.ExpandBoxTitleTemplate(t.GetBoxTitleTemplate(), box, space, agent, account, icons[agent])
	if err != nil {
		_, _ = fmt.Fprintf(errw, "warning: tui.yaml box_title_template or agent_icons is invalid (%v); using the default composition\n", err)
		expanded, strip = "", ""
	}
	return t.Session.RingBufferBytes, expanded, strip
}

// sessionTermGeometry reports the local terminal's TERM and size for a
// holder's initial PTY. Zero values let the holder defaults apply (the
// first attach RESIZE corrects geometry anyway).
func sessionTermGeometry() (termName string, cols, rows int) {
	termName = os.Getenv("TERM")
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		if c, r, err := term.GetSize(fd); err == nil {
			cols, rows = c, r
		}
	}
	return termName, cols, rows
}

// sessionStatus is the probed session state for `box show`.
type sessionStatus struct {
	// State: "running" (holder unit active), "exited" (tombstone — the
	// agent ended while detached and nobody attached since), "lost"
	// (marker without a live holder while the box runs: the holder died
	// and the agent is an unrecoverable orphan, spike F2), or "none".
	State    string
	ExitCode int
	ExitedAt time.Time
}

// sessionSummaryLine renders a sessionStatus for the human-readable
// `box show` view.
func sessionSummaryLine(s sessionStatus, name string) string {
	switch s.State {
	case "running":
		return fmt.Sprintf("running (join it: yoloctl box attach %s)", name)
	case "exited":
		return fmt.Sprintf("session exited with status %d at %s (next attach starts fresh)",
			s.ExitCode, s.ExitedAt.Format(time.RFC3339))
	case "lost":
		return fmt.Sprintf("lost — holder died, session unrecoverable (recover: yoloctl box recreate %s)", name)
	default:
		return "none (start one: yoloctl box attach " + name + ")"
	}
}

// waitForAttachSocket waits for a freshly started holder to bring up
// its attach socket, failing fast when the unit died on startup.
func waitForAttachSocket(ctx context.Context, c *container.Client, name string) error {
	deadline := time.Now().Add(holderStartupTimeout)
	for {
		if c.Shell(ctx, "sudo", "test", "-S", session.SocketPath(name)) == nil {
			return nil
		}
		if !session.HolderUnitActive(ctx, c, name) {
			return fmt.Errorf("session holder for %s exited during startup (unit %s; logs: `%s` in the space VM)",
				name, session.UnitName(name), vmservice.LogHint(c, session.UnitName(name)))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("session holder for %s did not open its attach socket within %s", name, holderStartupTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Identity is captured at launch. Reattaching cannot change a running process's
// environment; it does not change the holder's TERM or terminal query ownership.
func sessionTerminalIdentity(ctx context.Context) termio.TerminalIdentity {
	if spaceenv.Native() {
		if prefs, _ := ctx.Value(controllerSessionKey{}).(*controller.SessionPreferences); prefs != nil {
			return prefs.Terminal
		}
		return termio.TerminalIdentity{}
	}
	return termio.CurrentTerminalIdentity()
}
