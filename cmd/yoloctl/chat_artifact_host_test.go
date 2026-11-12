package main

import (
	"testing"

	"github.com/leogr/yoloctl/internal/chatimage"
	"github.com/leogr/yoloctl/internal/tui"
)

// The TUI's reader is this binary's controller client, and it refuses an
// invalid request before routing anywhere.
func TestPreviewReaderIsWiredAndValidatesFirst(t *testing.T) {
	if tui.ReadChatArtifact == nil {
		t.Fatal("TUI has no artifact reader")
	}
	if _, err := readChatArtifactForPreview(t.Context(), "", chatimage.Frame{Artifact: "https://example.org/x"}); err == nil {
		t.Fatal("invalid request accepted")
	}
}
