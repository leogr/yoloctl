package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/leogr/yoloctl/internal/termio"
)

// Use a child process to inherit the real worker's fd 5 without replacing a
// descriptor owned by the test runner. Preparation services and command apply
// are simulated; the request's view ownership and preparation loop are real.
func TestControllerViewSurvivesPreparation(t *testing.T) {
	const childEnv = "YOLOCTL_TEST_VIEW_PREPARATION"
	const queued = "input-before-preparation"
	const reply = "view-survived-preparation"
	mode := os.Getenv(childEnv)
	if mode == "" {
		for _, mode := range []string{"image", "npm", "image-npm", "no-view-image", "command-error"} {
			t.Run(mode, func(t *testing.T) {
				parent, child, err := termio.ViewPair()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = parent.Close(); _ = child.Close() }()
				null, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = null.Close() }()
				if _, err := parent.Write([]byte(queued)); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControllerViewSurvivesPreparation$")
				cmd.Env = append(os.Environ(), childEnv+"="+mode)
				cmd.ExtraFiles = []*os.File{null, null, child}
				var output bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &output
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				_ = child.Close()
				if err := cmd.Wait(); err != nil {
					t.Fatalf("worker view fixture: %v\n%s", err, output.String())
				}
				if strings.HasPrefix(mode, "no-view") {
					return
				}
				if err := parent.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(parent)
				if err != nil || string(got) != reply {
					t.Fatalf("view reply and EOF = %q, %v", got, err)
				}
			})
		}
		return
	}

	withView := !strings.HasPrefix(mode, "no-view")
	wantImage := strings.Contains(mode, "image")
	wantProbe := strings.Contains(mode, "npm")
	var view *os.File
	assertView := func(ctx context.Context) {
		t.Helper()
		current, _ := ctx.Value(controllerViewKey{}).(*os.File)
		if !withView {
			if current != nil {
				t.Fatal("request without a view acquired one")
			}
			return
		}
		if current == nil {
			t.Fatal("managed request lost its view")
		}
		if view != nil && current != view {
			t.Fatal("preparation reopened the inherited view")
		}
		view = current
		if _, err := view.Stat(); err != nil {
			t.Fatalf("view closed during preparation: %v", err)
		}
	}
	acquisitions, probes, passes := 0, 0, 0
	executing := false
	previousProbe := probeNpmLatest
	t.Cleanup(func() { probeNpmLatest = previousProbe })
	probeNpmLatest = func(ctx context.Context, _ string) (*agentLatestInfo, error) {
		assertView(ctx)
		probes++
		return &agentLatestInfo{}, nil
	}
	state := &imageWarmState{enabled: true, entries: map[string]warmImage{}, call: func(ctx context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
		if executing {
			t.Fatal("image preparation/drain ran inside the apply pass")
		}
		assertView(ctx)
		switch r.Action {
		case "preparing", "prepared":
			return controller.ImageReply{}, nil
		case "resolve":
			return controller.ImageReply{Digest: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}, nil
		case "acquire":
			acquisitions++
			return controller.ImageReply{Pin: controller.PinnedImage{JobID: "fixture", Digest: "sha256:" + strings.Repeat("c", 64)}}, nil
		default:
			t.Fatalf("unexpected image action %q", r.Action)
			return controller.ImageReply{}, nil
		}
	}}
	wantErr := errors.New("command failed")
	err := runPreparedControllerWorker(t.Context(), controller.WorkerInput{Request: controller.Request{View: withView}}, state, func(ctx context.Context, _ controller.WorkerInput) error {
		executing = true
		defer func() { executing = false }()
		assertView(ctx)
		passes++
		if wantImage && acquisitions == 0 {
			return &imageWarmNeeded{prepared: agentboximage.PreparedContext{
				DesiredRef: "local/fixture:one", BaseRef: "ubuntu:24.04",
				Recipe: imagejob.Recipe{Builder: "fixture/1", Dockerfile: []byte("FROM ubuntu:24.04\n")},
			}}
		}
		if wantProbe && probes == 0 {
			return &npmProbeNeeded{key: "fixture", profile: agentprofile.Profile{NpmPackage: "fixture"}}
		}
		if withView {
			if err := view.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, len(queued))
			if _, err := io.ReadFull(view, data); err != nil || string(data) != queued {
				t.Fatalf("queued view input = %q, %v", data, err)
			}
			if _, err := view.Write([]byte(reply)); err != nil {
				t.Fatal(err)
			}
		}
		if mode == "command-error" {
			return wantErr
		}
		return nil
	})
	if mode == "command-error" {
		if !errors.Is(err, wantErr) {
			t.Fatalf("command error lost: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	wantPasses := 1
	if wantImage {
		wantPasses++
	}
	if wantProbe {
		wantPasses++
	}
	if passes != wantPasses || wantImage != (acquisitions == 1) || wantProbe != (probes == 1) {
		t.Fatalf("passes=%d image preparations=%d npm probes=%d", passes, acquisitions, probes)
	}
	if withView {
		if _, err := view.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("view not closed after request: %v", err)
		}
	}
}
