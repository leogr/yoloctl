//go:build linux

package vmanchor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLaunchObservationIdentityUnavailableHasNoData(t *testing.T) {
	var missing *LaunchAnchor
	lost := errors.New("original anchor lost")
	for _, anchor := range []*LaunchAnchor{missing, {}, {closed: true, boot: "retained-but-closed"}, {failed: lost, boot: "retained-but-lost"}} {
		boot, fingerprint, err := anchor.ObservationIdentity()
		if err == nil || boot != "" || fingerprint != "" {
			t.Fatalf("unavailable anchor exposed correlation: %q %q %v", boot, fingerprint, err)
		}
		if anchor != nil && anchor.failed == lost && !errors.Is(err, lost) {
			t.Fatal("original sticky error replaced", err)
		}
	}
}

// A separate body must execute on a suitable executor. The existing base and
// FirecrackerSource getter tests do not execute this new anchor accessor.
// This is a finite owned child mechanism fixture, never PID1/VM admission.
func TestLaunchObservationIdentityOriginalAnchorAndClose(t *testing.T) {
	source := fixtureSource(t)
	boot, fingerprint, err := source.ObservationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := source.PrepareLaunch()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := transfer.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLaunchObservationIdentityChild$")
	cmd.Env = append(os.Environ(), "YOLOCTL_LAUNCH_IDENTITY_CHILD=1")
	cmd.ExtraFiles = transfer.ChildFiles()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := transfer.PublishStarted(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("new anchor accessor child: %v: %s", err, stderr.String())
	}
	var got struct {
		Boot        string `json:"boot"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Boot != boot || got.Fingerprint != fingerprint {
		t.Fatalf("original source and original child correlation disagree: %+v %v", got, err)
	}
}

func TestLaunchObservationIdentityChild(t *testing.T) {
	if os.Getenv("YOLOCTL_LAUNCH_IDENTITY_CHILD") != "1" {
		return
	}
	anchor, claimed, err := consumeInheritedLaunch(3, 4, os.Getppid(), time.Second)
	if err != nil || !claimed || anchor == nil {
		os.Exit(20)
	}
	boot, fingerprint, err := anchor.ObservationIdentity()
	if err != nil {
		_ = anchor.Close()
		os.Exit(21)
	}
	again, next, err := anchor.ObservationIdentity()
	if err != nil || again != boot || next != fingerprint {
		_ = anchor.Close()
		os.Exit(22)
	}
	if err := anchor.Close(); err != nil {
		os.Exit(23)
	}
	if b, f, err := anchor.ObservationIdentity(); err == nil || b != "" || f != "" {
		os.Exit(24)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Boot        string `json:"boot"`
		Fingerprint string `json:"fingerprint"`
	}{boot, fingerprint}); err != nil {
		os.Exit(25)
	}
	os.Exit(0)
}
