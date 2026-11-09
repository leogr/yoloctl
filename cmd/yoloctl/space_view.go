package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// newSpaceViewCmd is host-only: it renders on the host terminal and reads the
// space through one long-lived controller stream.
func newSpaceViewCmd() *cobra.Command {
	var (
		interval time.Duration
		noMotion bool
	)
	cmd := &cobra.Command{
		Use:   "view [SPACE]",
		Short: "Show a live, clickable view of a space's boxes and resources",
		Long: `Opens a live view of one space: the host workdirs and ports that cross into
the VM, CPU and memory shared by boxes and builds, every box with its agent,
account, workdir, ports, features and live usage, chat rooms observed between
boxes, the VM's system and data disks, and the accounts on the data disk.

Click a box (or select it and press Enter) to attach. Attaching to a stopped
box or an exited session asks first, because it starts them. Inside the yoloctl
TUI the attachment opens in a new tab; elsewhere it takes over the terminal
until you detach. Right-click a box, or press i, to inspect it. Click an
account, feature, capability, port, workdir, chat room, the VM gauges or a
disk for details; they show the same data as the matching command. The
system disk holds boxes, images and build caches; its details measure what
fills it (yoloctl space storage). A disk that runs low (under 4 GiB free, 15%
of a smaller disk, or from 85% used) or almost full (under 2 GiB, 10%, or from
95%) is flagged in the
title row and announced once. Click
a box's memory row for its live breakdown: total charge, limit or shared pool,
and an estimated working set.

The view reads the space; it never starts a VM. A stopped space is shown as
unavailable until it is started. Press ? for keys.

  yoloctl space view
  yoloctl space view dev --interval 2s`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpaceView(cmd, args, interval, !noMotion)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", defaultTelemetryInterval, "Sampling interval, between 250ms and 10s")
	cmd.Flags().BoolVar(&noMotion, "no-motion", false, "Disable animation (pulses, gliding gauges and flashes)")
	return cmd
}

func runSpaceView(cmd *cobra.Command, args []string, interval time.Duration, motion bool) error {
	if operator, err := loadOperatorClient(); err != nil {
		return err
	} else if operator != nil {
		return asUserError(errors.New("box operator cannot open space view"))
	}
	if _, err := parseTelemetryInterval([]string{interval.String()}); err != nil {
		return asUserError(err)
	}
	space, err := resolveSpacePositional(args)
	if err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) || !stdinIsTerminal(cmd.InOrStdin()) {
		return asUserError(errors.New("space view needs an interactive terminal; use space show or box list for scripts"))
	}
	prefs, err := config.LoadTUI()
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	backend := ""
	if driver, err := vmhost.ForSpace(space); err == nil {
		backend = driver.Kind()
	}
	home, _ := os.UserHomeDir()
	// The inherited session channel belongs to the hosting TUI tab. Nothing
	// here uses it, but no child may inherit it either.
	view, err := termio.InheritedView()
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(view)
	requests := tabRequests(cmd.Context())
	opts := spaceview.Options{
		Space:    space,
		Interval: interval,
		VM:       spaceview.VMInfo{Backend: backend},
		Home:     home,
		Icons:    prefs.GetAgentIcons(),
		Motion:   motion,
		Connect: func(ctx context.Context, send func(spacetelemetry.Frame)) error {
			return streamSpaceView(ctx, space, interval, send)
		},
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			return runViewCommand(ctx, self, args)
		},
		AttachCmd: func(box string) *exec.Cmd {
			c := exec.Command(self, "box", "attach", box, "--space", space)
			c.Env = viewChildEnv()
			return c
		},
	}
	if requests != nil {
		defer func() { _ = requests.Close() }()
		var mu sync.Mutex
		opts.OpenTab = func(box string) error {
			mu.Lock()
			defer mu.Unlock()
			return termio.WriteTabRequest(requests, termio.TabRequest{Op: "attach", Space: space, Box: box})
		}
	}
	if prefs.BrowserPolicy() != "off" {
		opts.OpenURL = hostintegration.OpenURL
	}
	return spaceview.Run(cmd.Context(), opts)
}

// streamSpaceView runs one telemetry stream. It checks the selected build and
// that the VM is running first; it never boots anything.
func streamSpaceView(ctx context.Context, space string, interval time.Duration, send func(spacetelemetry.Frame)) error {
	probe, cancel := context.WithTimeout(ctx, 2*vmhost.GuestProbeTimeout)
	boot, err := checkControllerSpace(probe, space)
	cancel()
	if err != nil {
		return err
	}
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	pr, pw := io.Pipe()
	stderr := &tailBuffer{limit: 4096}
	done := make(chan error, 1)
	go func() {
		req := controller.Request{ID: rand.Text(), Method: spaceTelemetryMethod, Space: space, Dash: -1,
			Flags: map[string][]string{"interval": {interval.String()}}}
		err := callHostController(streamCtx, boot, req, controller.ClientIO{Out: pw, Err: stderr})
		err = vmhost.RecordDiagnostics("space telemetry", []byte(stderr.String()), err)
		if err == nil {
			err = io.EOF
		}
		_ = pw.CloseWithError(err)
		done <- err
	}()
	dec := spacetelemetry.NewDecoder(pr)
	for {
		f, err := dec.Decode()
		if err != nil {
			stop()
			_ = pr.CloseWithError(err)
			callErr := <-done
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if callErr != nil && !errors.Is(callErr, io.EOF) && !errors.Is(callErr, context.Canceled) {
				err = callErr
			}
			if detail := strings.TrimSpace(stderr.String()); detail != "" && !strings.Contains(err.Error(), detail) {
				err = fmt.Errorf("%w: %s", err, detail)
			}
			return err
		}
		send(f)
	}
}

// tailBuffer keeps the last bytes written, for error context.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// runViewCommand runs this binary for a popup's details. Output is bounded;
// stderr becomes the error text.
func runViewCommand(ctx context.Context, self string, args []string) ([]byte, error) {
	c := exec.CommandContext(ctx, self, args...)
	c.Env = viewChildEnv()
	stdout := &boundedBuffer{Limit: 8 << 20}
	stderr := &tailBuffer{limit: 4096}
	c.Stdout, c.Stderr = stdout, stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(lastLine(msg))
		}
		return nil, err
	}
	return bytes.Clone(stdout.Bytes()), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// viewChildEnv drops the private channels a TUI tab gave this process; a
// child that saw them would try to use descriptors it does not own.
func viewChildEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, termio.ViewFDEnv+"=") || strings.HasPrefix(kv, termio.TabRequestFDEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
