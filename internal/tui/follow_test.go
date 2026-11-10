package tui

import "testing"

func TestFollowUsesTheSharedLiveSurfaceRouting(t *testing.T) {
	for _, args := range [][]string{{"follow", "notes.md"}, {"follow", "--host", "log"}, {"follow", "--space", "dev", "file"}} {
		if !opensLiveSurface(args) {
			t.Fatal("follow stayed in palette", args)
		}
	}
	for _, args := range [][]string{{"follow", "--help"}, {"follow", "-h"}, {"help", "follow"}} {
		if opensLiveSurface(args) {
			t.Fatal("follow help opened a live pane", args)
		}
	}
}
