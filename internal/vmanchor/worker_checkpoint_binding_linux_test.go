//go:build linux

package vmanchor

import (
	"context"
	"testing"

	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

func TestZeroHandoffAndPhysicalOwnerCannotMintOriginalSource(t *testing.T) {
	var absent *WorkerHandoff
	if err := absent.BindCheckpoint(context.Background(), nil); err == nil {
		t.Fatal("nil handoff bound")
	}
	if err := new(WorkerHandoff).BindCheckpoint(context.Background(), workercheckpoint.Take(nil)); err == nil {
		t.Fatal("transport constructor minted genuine source")
	}
	if err := new(WorkerHandoff).CheckParent(context.Background(), "box"); err == nil {
		t.Fatal("zero handoff reported parent source success")
	}
}

// Test matchCheckpoint only as copied PRIMARY metadata, never construct a
// WorkerHandoff from it: equal values alone are not source/custody evidence.
func TestCheckpointJoinRejectsSlotIdentityCreatorMovement(t *testing.T) {
	original := workerDescriptor{FD: 4, Device: 1, Inode: 2, Mode: 0140000}
	base := workercheckpoint.Identity{FD: 4, Device: 1, Inode: 2, Mode: 0140000, PeerPID: 111, PeerUID: 0, PeerGID: 0}
	for _, field := range []string{"fd", "device", "inode", "mode", "peer", "uid", "gid"} {
		t.Run(field, func(t *testing.T) {
			changed := base
			switch field {
			case "fd":
				changed.FD++
			case "device":
				changed.Device++
			case "inode":
				changed.Inode++
			case "mode":
				changed.Mode++
			case "peer":
				changed.PeerPID++
			case "uid":
				changed.PeerUID++
			case "gid":
				changed.PeerGID++
			}
			if matchCheckpoint(original, 111, changed) == nil {
				t.Fatal("changed primary join accepted")
			}
		})
	}
}
