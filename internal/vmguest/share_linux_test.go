//go:build linux

package vmguest

import (
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestShareTransportWithSilentHost(t *testing.T) {
	for _, phase := range []string{"before readiness", "after readiness", "mount expiry", "stalled output"} {
		t.Run(phase, func(t *testing.T) {
			guest, host := net.Pipe()
			defer func() { _ = host.Close() }()
			cmd := exec.Command("sh", "-c", "exit 7")
			drain := guestWaitTimeout
			switch phase {
			case "mount expiry":
				cmd = exec.Command("sleep", "60")
			case "stalled output":
				cmd = exec.Command("sh", "-c", "printf final; exit 7")
				drain = time.Millisecond
			}
			done, abort, err := startShareTransport(cmd, guest, drain)
			if err != nil {
				t.Fatal(err)
			}
			defer abort()
			watchdog := time.NewTimer(30 * time.Second)
			defer watchdog.Stop()
			deadline := make(chan time.Time, 1)
			if phase == "mount expiry" {
				deadline <- time.Now()
			}
			observed, watched := make(chan error, 1), make(chan error, 1)
			go func() {
				observed <- awaitShare("/fixture", func() bool { return phase == "after readiness" }, done, deadline, abort,
					func(err error) { watched <- err })
			}()
			select {
			case err = <-observed:
			case <-watchdog.C:
				t.Fatal("readiness blocked on silent host")
			}
			switch phase {
			case "after readiness":
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-watched:
				case <-watchdog.C:
					t.Fatal("mount watcher did not get child exit")
				}
			case "mount expiry":
				if !errors.Is(err, errShareMountDeadline) || !strings.Contains(err.Error(), "/fixture") {
					t.Fatal(err)
				}
				select {
				case err = <-done:
				case <-watchdog.C:
					t.Fatal("expiry did not retire child and copiers")
				}
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || phase != "mount expiry" && exit.ExitCode() != 7 {
				t.Fatalf("missing exact child exit: %v", err)
			}
			if _, owned := ownedChildren.Load(cmd.Process.Pid); owned {
				t.Fatal("child still registered")
			}
			_ = host.SetReadDeadline(time.Now().Add(30 * time.Second))
			if _, err := host.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("transport left open: %v", err)
			}
		})
	}
}

func TestShareTransportDeliversFinalBytes(t *testing.T) {
	guest, host := net.Pipe()
	defer func() { _ = host.Close() }()
	_ = host.SetDeadline(time.Now().Add(30 * time.Second))
	cmd := exec.Command("sh", "-c", `read -r line && printf 'response:%s\n' "$line"`)
	done, abort, err := startShareTransport(cmd, guest, guestWaitTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	if _, err := io.WriteString(host, "request\n"); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(host)
	if err != nil || string(raw) != "response:request\n" {
		t.Fatalf("share received %q: %v", raw, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestShareWaitIsPerGrant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, delay := range []time.Duration{6 * time.Second, 119 * time.Second} {
			start := time.Now()
			done := make(chan error, 1)
			timer := time.NewTimer(guestWaitTimeout)
			err := awaitShare("/fixture", func() bool { return time.Since(start) >= delay }, done, timer.C,
				func() { t.Error("grant canceled") }, func(error) {})
			timer.Stop()
			if err != nil || time.Since(start) < delay {
				t.Fatal(err, time.Since(start))
			}
			done <- nil
		}
	})
}
