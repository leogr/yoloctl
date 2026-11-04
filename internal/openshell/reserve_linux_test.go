package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func reservationFixture(t *testing.T) (string, containerOrigin) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root, containerOrigin{"space", "disk", "uuid", "box", strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 64)}
}

func TestReserveNativeRefusesOrdinaryExecutorBeforeWriting(t *testing.T) {
	if _, err := ReserveNativeContainer(context.Background(), bootstrapInput().Binding, QualifiedRelease{}, ""); err == nil {
		t.Fatal("ordinary executor reserved native state")
	}
}

func TestFreshContainerReservationIsPrivateAndNeverAdoptsExistingState(t *testing.T) {
	root, origin := reservationFixture(t)
	directory, err := reserveContainerAt(root, origin, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "containers"), directory} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatal("reservation directory is not private", err)
		}
	}
	name := filepath.Join(directory, "origin.json")
	raw, info, err := readReservationOriginForTest(name)
	if err != nil || privateFileOwner(info, os.Geteuid(), os.Getegid()) != nil {
		t.Fatal("origin is not private regular metadata", err)
	}
	var got containerOrigin
	if json.Unmarshal(raw, &got) != nil || got != origin {
		t.Fatal("reservation origin changed")
	}
	if files, err := os.ReadDir(directory); err != nil || len(files) != 1 || files[0].Name() != "origin.json" {
		t.Fatal("reservation created credentials/generation/launch material", err)
	}
	changed := origin
	changed.Box = "different"
	if _, err := reserveContainerAt(root, changed, os.Geteuid(), os.Getegid()); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing reservation adopted or repaired", err)
	}
	after, err := os.ReadFile(name)
	if err != nil || string(after) != string(raw) {
		t.Fatal("foreign reservation origin changed")
	}
}

func readReservationOriginForTest(name string) ([]byte, os.FileInfo, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(name)
	return raw, info, err
}

func TestFreshContainerReservationRefusesLinksModesAndInvalidNames(t *testing.T) {
	for _, mode := range []string{"parent-link", "parent-mode", "existing-empty", "existing-link", "invalid-name"} {
		t.Run(mode, func(t *testing.T) {
			root, origin := reservationFixture(t)
			parent := filepath.Join(root, "containers")
			outside := t.TempDir()
			if mode == "parent-link" {
				if err := os.Symlink(outside, parent); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "parent-mode":
				if err := os.Chmod(parent, 0o755); err != nil {
					t.Fatal(err)
				}
			case "existing-empty":
				if err := os.Mkdir(filepath.Join(parent, origin.Incarnation), 0o700); err != nil {
					t.Fatal(err)
				}
			case "existing-link":
				if err := os.Symlink(outside, filepath.Join(parent, origin.Incarnation)); err != nil {
					t.Fatal(err)
				}
			case "invalid-name":
				origin.Incarnation = "../outside"
			}
			if _, err := reserveContainerAt(root, origin, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("untrusted or existing reservation accepted")
			}
			if files, err := os.ReadDir(outside); err != nil || len(files) != 0 {
				t.Fatal("reservation followed outside link", err)
			}
			if mode == "parent-mode" {
				if info, err := os.Lstat(parent); err != nil || info.Mode().Perm() != 0o755 {
					t.Fatal("untrusted directory mode repaired", err)
				}
			}
		})
	}
}

func TestFreshContainerReservationHasOneConcurrentWinner(t *testing.T) {
	root, origin := reservationFixture(t)
	// Initialize the private collection; the actual incarnation is still absent.
	if err := os.Mkdir(filepath.Join(root, "containers"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := reserveContainerAt(root, origin, os.Geteuid(), os.Getegid())
			errorsOut <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsOut)
	winners, collisions := 0, 0
	for err := range errorsOut {
		if err == nil {
			winners++
		} else if errors.Is(err, os.ErrExist) {
			collisions++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || collisions != 1 {
		t.Fatal("reservation did not preserve exclusive ownership", winners, collisions)
	}
}
