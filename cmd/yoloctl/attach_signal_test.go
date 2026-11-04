package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
)

const (
	attachSignalSocketEnv = "YOLOCTL_TEST_ATTACH_SIGNAL_SOCKET"
	attachSignalModeEnv   = "YOLOCTL_TEST_ATTACH_SIGNAL_MODE"
	attachSignalRequestID = "attach-signal-request-0123"
)

var (
	attachSignalHello   = controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "build-1", Space: "work", Generation: "generation-1"}
	attachSignalSockets atomic.Int64
)

// TestAttachSignalClientProcess is the real CLI client of the regressions
// below: the process signal context, the attach or box-use wrapper, the
// controller client and the exit-status mapping. It is a no-op otherwise.
func TestAttachSignalClientProcess(t *testing.T) {
	socket := os.Getenv(attachSignalSocketEnv)
	if socket == "" {
		return
	}
	ctx, stop := commandSignalContext()
	defer stop()
	cmd := &cobra.Command{Use: "attach"}
	for _, flag := range []string{"passthrough", "box-use", "no-attach", "manifest-only"} {
		cmd.Flags().Bool(flag, false, "")
	}
	if os.Getenv(attachSignalModeEnv) == "box use" {
		_ = cmd.Flags().Set("box-use", "true")
	}
	cmd.SetContext(ctx)
	cmd.SetIn(os.Stdin)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	target := controllerTarget{Space: "work", Operator: true, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return err
		}
		return controller.Call(ctx, conn, attachSignalHello, req, streams)
	}}
	req := controller.Request{ID: attachSignalRequestID, Method: "box.attach", Space: "work", Args: []string{"coder"}, Dash: -1}
	err := callControllerTerminal(cmd, target, req)
	if err != nil && !cmd.SilenceErrors {
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
	}
	stop()
	os.Exit(classifyError(err))
}

// ownedSetupController serves one attach whose authorized setup waits until
// the test lets it hand off, recording the acknowledgment and any signal that
// would have reached the worker's process group. Once attached it ends on the
// first forwarded signal like the remote attach CLI, unless ignoreSignals.
type ownedSetupController struct {
	s             *controller.Server
	socket        string
	ignoreSignals bool
	authorized    chan struct{}
	proceed       chan struct{}
	released      chan error
	handedOff     chan struct{}
	signals       chan int
	served        chan struct{}
}

func newOwnedSetupController(t *testing.T, ignoreSignals bool) *ownedSetupController {
	t.Helper()
	c := &ownedSetupController{
		socket:        fmt.Sprintf("@yoloctl-attach-signal-%d-%d", os.Getpid(), attachSignalSockets.Add(1)),
		ignoreSignals: ignoreSignals, authorized: make(chan struct{}), proceed: make(chan struct{}),
		released: make(chan error, 1), handedOff: make(chan struct{}), signals: make(chan int, 4), served: make(chan struct{}),
	}
	c.s = &controller.Server{Hello: attachSignalHello, Receipts: controller.Receipts{Dir: t.TempDir()},
		Operations:     map[string]controller.Operation{"box.attach": controllerOperations["box.attach"]},
		CheckPrincipal: func(context.Context, controller.Principal) error { return nil },
		Execute: func(ctx context.Context, _ controller.Principal, _ controller.Request, streams controller.Streams) (int, error) {
			if err := streams.BeginEffects(ctx, controller.EffectIntent{}); err != nil {
				return 2, err
			}
			close(c.authorized)
			<-c.proceed
			err := streams.Release()
			c.released <- err
			if err != nil {
				for {
					select {
					case sig := <-streams.Signals:
						c.signals <- sig
					default:
						return 0, nil
					}
				}
			}
			close(c.handedOff)
			for {
				select {
				case sig := <-streams.Signals:
					c.signals <- sig
					if !c.ignoreSignals {
						return 0, nil
					}
				case <-ctx.Done():
					return 0, nil
				}
			}
		}}
	listener, err := net.Listen("unix", c.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		defer close(c.served)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		c.s.ServeConn(context.Background(), conn, controller.Principal{})
	}()
	return c
}

// startAttachSignalClient starts the real CLI client process of one mode.
func startAttachSignalClient(t *testing.T, c *ownedSetupController, mode string) (*exec.Cmd, <-chan error, *lockedBuffer) {
	t.Helper()
	client := exec.Command(os.Args[0], "-test.run=^TestAttachSignalClientProcess$")
	client.Env = append(os.Environ(), attachSignalSocketEnv+"="+c.socket, attachSignalModeEnv+"="+mode,
		"XDG_CONFIG_HOME="+t.TempDir())
	stderr := &lockedBuffer{}
	client.Stdout, client.Stderr = stderr, stderr
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- client.Wait() }()
	t.Cleanup(func() { _ = client.Process.Kill() })
	return client, exited, stderr
}

// lockedBuffer is read by the test while the client process writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// One ordinary signal (kill, timeout(1), a closing terminal's SIGHUP) during
// an authorized attach setup ends the real CLI client promptly with the
// detached status; setup finishes once without it and nothing fences.
func TestAttachClientSignalDuringOwnedSetupDetaches(t *testing.T) {
	for _, mode := range []string{"box attach", "box use"} {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
			t.Run(mode+"/"+sig.String(), func(t *testing.T) {
				c := newOwnedSetupController(t, false)
				client, exited, stderr := startAttachSignalClient(t, c, mode)
				await(t, c.authorized, "the attach setup to be authorized")
				if err := client.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				var err error
				select {
				case err = <-exited:
				case <-time.After(5 * time.Second):
					t.Fatalf("one %v did not end the client; output:\n%s", sig, stderr.String())
				}
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != exitDetached {
					t.Fatalf("client exit %v, want status %d; output:\n%s", err, exitDetached, stderr.String())
				}
				out := stderr.String()
				for _, want := range []string{attachSignalRequestID, "keeps starting the session", "check: yoloctl box session coder --space work", "reattach: yoloctl box attach coder --space work"} {
					if !strings.Contains(out, want) {
						t.Fatalf("client output lacks %q:\n%s", want, out)
					}
				}
				close(c.proceed)
				if err := <-c.released; !errors.Is(err, controller.ErrClientDetached) {
					t.Fatal("setup acknowledgment", err)
				}
				await(t, c.served, "the owned setup to finish")
				select {
				case got := <-c.signals:
					t.Fatalf("signal %d reached the setup worker", got)
				default:
				}
				// box use's frontend closes with a SIGTERM frame whatever the process signal was.
				want := controller.SignalName(int(sig))
				if mode == "box use" {
					want = "SIGTERM"
				}
				r, err := c.s.Receipts.Read(attachSignalRequestID)
				if err != nil || r.State != "complete" || r.Status != 0 || !r.SetupComplete || r.EffectPhase != controller.EffectStarted ||
					!r.ClientDetached || r.DetachSignal != want || c.s.RecoveryRequired {
					t.Fatalf("receipt=%+v recovery=%v err=%v", r, c.s.RecoveryRequired, err)
				}
			})
		}
	}
}

// After the attachment starts, one signal is forwarded to it and ends it; a
// second ends the client unconditionally, even when the attachment ignores
// the first. Neither detaches the client before handoff (status 75).
func TestAttachClientSignalAfterHandoff(t *testing.T) {
	exitWithin := func(t *testing.T, exited <-chan error, stderr *lockedBuffer, what string) int {
		t.Helper()
		select {
		case err := <-exited:
			var exit *exec.ExitError
			if err == nil {
				return 0
			}
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			return exit.ExitCode()
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not end the client; output:\n%s", what, stderr.String())
		}
		return -1
	}
	forwarded := func(t *testing.T, c *ownedSetupController, want syscall.Signal) {
		t.Helper()
		select {
		case got := <-c.signals:
			if got != int(want) {
				t.Fatalf("forwarded signal %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v was not forwarded to the attachment", want)
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run("one/"+sig.String(), func(t *testing.T) {
			c := newOwnedSetupController(t, false)
			close(c.proceed)
			client, exited, stderr := startAttachSignalClient(t, c, "box attach")
			await(t, c.handedOff, "the attachment to start")
			if err := <-c.released; err != nil {
				t.Fatal("setup acknowledgment", err)
			}
			if err := client.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if status := exitWithin(t, exited, stderr, "one "+sig.String()); status != 0 || strings.Contains(stderr.String(), "keeps starting the session") {
				t.Fatalf("client exit %d, want the attachment's 0 without the detached notice; output:\n%s", status, stderr.String())
			}
			forwarded(t, c, sig)
			await(t, c.served, "the attachment to finish")
			r, err := c.s.Receipts.Read(attachSignalRequestID)
			if err != nil || r.State != "complete" || r.Status != 0 || !r.SetupComplete || r.ClientDetached || r.DetachSignal != "" || c.s.RecoveryRequired {
				t.Fatalf("receipt=%+v recovery=%v err=%v", r, c.s.RecoveryRequired, err)
			}
		})
	}
	t.Run("second signal", func(t *testing.T) {
		c := newOwnedSetupController(t, true)
		close(c.proceed)
		client, exited, stderr := startAttachSignalClient(t, c, "box attach")
		await(t, c.handedOff, "the attachment to start")
		if err := client.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		forwarded(t, c, syscall.SIGTERM)
		select {
		case err := <-exited:
			t.Fatalf("the client ended on a forwarded signal its attachment ignored: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := client.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if status := exitWithin(t, exited, stderr, "a second SIGTERM"); status == exitDetached {
			t.Fatalf("an attached client reported the before-handoff status %d", status)
		}
		await(t, c.served, "the attachment to end")
		r, err := c.s.Receipts.Read(attachSignalRequestID)
		if err != nil || r.State == "uncertain" || !r.SetupComplete || r.ClientDetached || r.DetachSignal != "" || c.s.RecoveryRequired {
			t.Fatalf("receipt=%+v recovery=%v err=%v", r, c.s.RecoveryRequired, err)
		}
	})
}
