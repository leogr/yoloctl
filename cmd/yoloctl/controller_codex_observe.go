package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/sshtransport"
	"golang.org/x/crypto/ssh"
)

// The Codex daemon read. While someone reads the box inventory, the
// controller asks each tracked Codex box's own app-server daemon, at most once
// a minute, for the current thread's model, effort and state. The read runs in
// the box as the sandbox user through the confined SSH path `box exec` uses.
// It is not an operation: no receipt, never replayed. Like storage
// maintenance it runs only on an idle, certain controller and yields to every
// admitted mutation. It holds no box lock: a read of a box being replaced just
// fails, and the writer appends only to an existing launch generation.
const (
	codexObserveInterval = time.Minute
	codexObserveRun      = 30 * time.Second
)

// codexObserveBoxBudget bounds one box's read, connection included; the
// writer bounds its own daemon read to 500 ms inside it.
var codexObserveBoxBudget = 3 * time.Second

func startControllerCodexObserve(parent context.Context, server *controller.Server) func() {
	return scheduleCodexObserve(parent, server, runCodexObserveProcess, codexObserveInterval, os.Stderr)
}

// scheduleCodexObserve wakes on a finished inventory read and runs at most
// once per interval, however many viewers read; reads during the wait join
// that run. ReadFinished never blocks the read that calls it.
func scheduleCodexObserve(parent context.Context, server *controller.Server, run func(context.Context) error, interval time.Duration, out io.Writer) func() {
	ctx, cancel := context.WithCancel(parent)
	wake := make(chan struct{}, 1)
	server.ReadFinished = func(method string) {
		if method != "box.inventory" && method != "box.list" {
			return
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var last time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			if wait := time.Until(last.Add(interval)); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			select {
			case <-wake:
			default:
			}
			last = time.Now()
			_, err := server.RunMaintenance(ctx, func(ctx context.Context) error {
				ctx, cancel := context.WithTimeout(ctx, codexObserveRun)
				defer cancel()
				return run(ctx)
			})
			if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				_, _ = fmt.Fprintf(out, "codex observation deferred: %v\n", err)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func runCodexObserveProcess(ctx context.Context) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, "controller-internal", "observe-codex")
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8",
		spaceenv.ContextEnv + "=" + spaceenv.ContextPath}
	cmd.Stderr = os.Stderr
	return waitStorageGCProcess(ctx, cmd)
}

// codexObserveTarget is one tracked Codex launch to read.
type codexObserveTarget struct {
	box    string
	gen    uint64
	thread string
}

// collectCodexObservation is the short-lived worker: it reads every target,
// one box at a time, each within its budget. A box that cannot be read is
// skipped; the next run tries again.
func collectCodexObservation(ctx context.Context) error {
	c := spaceenv.Current()
	catalogs, _, cleanup, err := controller.CatalogSnapshot(c.CatalogDir, spaceenv.RunRoot)
	if err != nil {
		return err
	}
	defer cleanup()
	spaceenv.UseCatalogSnapshot(catalogs)
	targets, err := codexObserveTargets(ctx, container.New(c.Space), c.Space)
	if err != nil {
		return err
	}
	return readCodexTargets(ctx, c.Space, targets)
}

// readCodexTargets reads one box at a time, each within its budget; a box
// that cannot be read is skipped and the next run tries again.
func readCodexTargets(ctx context.Context, space string, targets []codexObserveTarget) error {
	for _, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		boxCtx, cancel := context.WithTimeout(ctx, codexObserveBoxBudget)
		_ = codexObserveExec(boxCtx, space, t.box, []string{features.AgentObserveCodexQuery, strconv.FormatUint(t.gen, 10), t.thread})
		cancel()
	}
	return nil
}

// codexObserveTargets lists the boxes whose profile reports through the Codex
// reporter and whose tracked launch runs now: the holder is live with that
// launch's token, as box session shows it. The thread is the conversation
// that view shows after folding this launch's live reports (the current one,
// else the assigned one), so a first report and a switch are both followed.
// Each box's probe and fold run within its budget.
func codexObserveTargets(ctx context.Context, c *container.Client, space string) ([]codexObserveTarget, error) {
	names, err := agentbox.ListNames(space)
	if err != nil {
		return nil, err
	}
	var targets []codexObserveTarget
	for _, name := range names {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cfg, err := agentbox.Load(space, name)
		if err != nil || !cfg.HasAgent() {
			continue
		}
		profile, err := agentprofile.Lookup(cfg.Agent)
		if err != nil || profile.PushFeature() != "yolo.agent.observe-codex" {
			continue
		}
		if t, ok := selectCodexTarget(ctx, c, cfg, profile); ok {
			targets = append(targets, t)
		}
	}
	return targets, nil
}

func selectCodexTarget(ctx context.Context, c *container.Client, cfg *agentbox.Config, profile *agentprofile.Profile) (codexObserveTarget, bool) {
	ctx, cancel := context.WithTimeout(ctx, codexObserveBoxBudget)
	defer cancel()
	session, token := probeBoxSession(ctx, c, cfg.Name)
	if session.State != boxSessionRunning || session.Error != "" || token == "" {
		return codexObserveTarget{}, false
	}
	// Each box's fold gets the live-read budget a box session command has,
	// within what remains of the box's own.
	beginObservationViewBudget(ctx)
	defer resetObservationViewBudget()
	st := agentsession.Describe(cfg, profile, true, token)
	if !st.LiveLaunch() || st.Launch == nil || st.Launch.State != agentsession.StateLaunched {
		return codexObserveTarget{}, false
	}
	thread := st.ID
	if st.Current != nil {
		thread = st.Current.ID
	}
	if !agentsession.ValidID(thread) {
		return codexObserveTarget{}, false
	}
	return codexObserveTarget{box: cfg.Name, gen: st.Launch.Generation, thread: thread}, true
}

// codexObserveExec runs argv in the box as the sandbox user through the
// confined OpenShell SSH endpoint, the path `box exec` uses; tests replace it.
var codexObserveExec = func(ctx context.Context, space, box string, argv []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	proxy := exec.CommandContext(ctx, executable, "ssh-proxy", "--space", space, box)
	conn, err := sshtransport.ProcessConn(ctx, proxy)
	if err != nil {
		return err
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = null.Close() }()
	return sshtransport.Run(ctx, conn, &ssh.ClientConfig{User: "sandbox", HostKeyCallback: ssh.InsecureIgnoreHostKey()},
		remoteCommandString(argv), false, null, io.Discard, io.Discard)
}
