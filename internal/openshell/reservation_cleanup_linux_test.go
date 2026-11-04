package openshell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ownedReservationFixture(t *testing.T) *NativeReservation {
	t.Helper()
	root, origin := reservationFixture(t)
	r, err := reserveOwnedContainerAt(root, origin, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}
func populateReservationFixture(t *testing.T, r *NativeReservation) {
	t.Helper()
	generation := "generations/g" + strings.Repeat("d", 48)
	for _, directory := range []string{"generations", generation, generation + "/boundary", generation + "/supervisor", generation + "/status", generation + "/status/forwards"} {
		if err := os.Mkdir(filepath.Join(r.Directory(), directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"current.json", generation + "/record.json", generation + "/boundary/bootstrap.json", generation + "/supervisor/auth.json", generation + "/status/activation.key", generation + "/status/forwards/" + strings.Repeat("e", 32) + ".lock"} {
		if err := os.WriteFile(filepath.Join(r.Directory(), name), []byte("private fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestOwnedReservationCleanupRemovesOnlyRetainedFreshTree(t *testing.T) {
	r := ownedReservationFixture(t)
	populateReservationFixture(t, r)
	sibling := filepath.Join(filepath.Dir(r.Directory()), strings.Repeat("f", 32))
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveAfterContainerRemoval(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(r.Directory()); !os.IsNotExist(err) {
		t.Fatal("owned tree retained", err)
	}
	if _, err := os.Lstat(sibling); err != nil {
		t.Fatal("other incarnation affected", err)
	}
	if err := r.RemoveAfterContainerRemoval(t.Context()); err == nil {
		t.Fatal("missing directory became acknowledged cleanup")
	}
}
func TestOwnedReservationCleanupRefusesUntrustedInventoryBeforeUnlink(t *testing.T) {
	for _, change := range []string{"extra", "symlink", "hardlink", "mode", "origin", "replaced", "parent-replaced", "role-owner", "cancelled", "generation"} {
		t.Run(change, func(t *testing.T) {
			r := ownedReservationFixture(t)
			populateReservationFixture(t, r)
			marker := filepath.Join(r.Directory(), "current.json")
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("retain"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch change {
			case "extra":
				if err := os.WriteFile(filepath.Join(r.Directory(), "unknown"), []byte("retain"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, marker); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, marker); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(marker, 0o644); err != nil {
					t.Fatal(err)
				}
			case "origin":
				if err := os.WriteFile(filepath.Join(r.Directory(), "origin.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.Rename(r.Directory(), r.Directory()+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(r.Directory(), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "parent-replaced":
				parent := filepath.Dir(r.Directory())
				if err := os.Rename(parent, parent+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(r.Directory(), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}

			case "role-owner":
				r.owners.boundaryUID++

			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "generation":
				if err := os.Mkdir(filepath.Join(r.Directory(), "generations", "bad-generation"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.RemoveAfterContainerRemoval(ctx); err == nil {
				t.Fatal("untrusted cleanup accepted")
			}
			if _, err := os.Lstat(marker); err != nil {
				t.Fatal("inventory failure deleted member", err)
			}
			if raw, err := os.ReadFile(outside); err != nil || string(raw) != "retain" {
				t.Fatal("outside affected", err)
			}
		})
	}
}
func TestOwnedReservationCloseRetainsRuntimeState(t *testing.T) {
	r := ownedReservationFixture(t)
	path := r.Directory()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "origin.json")); err != nil {
		t.Fatal("worker lifetime deleted runtime", err)
	}
	if err := r.RemoveAfterContainerRemoval(t.Context()); err == nil {
		t.Fatal("closed handle adopted state")
	}
}
