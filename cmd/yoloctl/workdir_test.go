package main

import (
	"errors"
	"io"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

// isContainerGone treats "no such container" / "not found" as benign so
// `box destroy` still cleans up the declaration + VM dirs when the
// runtime container is already gone.
func TestIsContainerGone(t *testing.T) {
	gone := []error{
		nil,
		errors.New("Error: no such container: coder"),
		errors.New(`container "coder" not found`),
	}
	for _, e := range gone {
		if !isContainerGone(e) {
			t.Errorf("isContainerGone(%v) = false, want true", e)
		}
	}
	if isContainerGone(errors.New("permission denied")) {
		t.Error("a genuine error must not be treated as container-gone")
	}
}

func TestBuildWorkdir_Validation(t *testing.T) {
	// --workdir and --workdir-mode ephemeral are mutually exclusive.
	if _, err := buildWorkdir("box", "/tmp/x", agentbox.WorkdirModeEphemeral, io.Discard); err == nil {
		t.Error("expected error for --workdir + --workdir-mode ephemeral")
	}
	// Host mode with no workdir → required-workdir error.
	if _, err := buildWorkdir("box", "", agentbox.WorkdirModeHost, io.Discard); err == nil {
		t.Error("expected error when neither --workdir nor --workdir-mode ephemeral is set")
	}
}

func TestBuildWorkdir_Ephemeral(t *testing.T) {
	wd, err := buildWorkdir("box", "", agentbox.WorkdirModeEphemeral, io.Discard)
	if err != nil {
		t.Fatalf("buildWorkdir ephemeral: %v", err)
	}
	if wd.Mode != agentbox.WorkdirModeEphemeral {
		t.Errorf("mode = %q, want ephemeral", wd.Mode)
	}
	if wd.Source != "/var/lib/yoloctl/agentboxes/box" {
		t.Errorf("ephemeral source = %q", wd.Source)
	}
}

func TestBuildWorkdir_HostCreatesMissingDir(t *testing.T) {
	parent := t.TempDir()
	workdir := parent + "/newrepo" // does not exist yet
	wd, err := buildWorkdir("box", workdir, agentbox.WorkdirModeHost, io.Discard)
	if err != nil {
		t.Fatalf("buildWorkdir host: %v", err)
	}
	if !wd.IsHost() {
		t.Errorf("mode = %q, want host", wd.Mode)
	}
	if wd.Canonical == "" {
		t.Error("host workdir missing canonical path")
	}
	// buildWorkdir creates the host dir so Lima can mount it.
	if _, exists, err := agentbox.CanonicalizeWorkdir(workdir); err != nil {
		t.Fatalf("canonicalize: %v", err)
	} else if !exists {
		t.Error("buildWorkdir did not create the host workdir")
	}
}
