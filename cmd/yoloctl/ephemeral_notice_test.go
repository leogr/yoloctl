package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

func TestCreateEphemeralNoticeAfterImageWarmReentry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubStoppedSpaceResolver(t)
	saveSpaceForTest(t, "default", t.TempDir())
	var output bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &output))
	opts := agentboxCreateOptions{Name: "ephemeral-notice", Space: "default", Agent: "none", Account: "none", Image: defaultAgentboxImage, ManifestOnly: true, workdir: agentbox.NewEphemeralWorkdir("ephemeral-notice")}
	attempts := 0
	ctx := context.WithValue(t.Context(), warmGateKey{}, func() error {
		attempts++
		if attempts == 1 {
			return &imageWarmNeeded{}
		}
		return nil
	})
	if err := createAgentbox(ctx, opts, io.Discard, nil); !imageWarmPending(err) {
		t.Fatal("expected warm unwind", err)
	}
	if strings.Contains(output.String(), "is --workdir-mode ephemeral") {
		t.Fatal("preparation pass emitted notice", output.String())
	}
	if err := createAgentbox(ctx, opts, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(output.String(), "is --workdir-mode ephemeral"); n != 1 {
		t.Fatal("notice count", n, output.String())
	}
}
