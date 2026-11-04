package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func namespacePinReservationFixture(t *testing.T) (string, *os.File, *namespacePinLock, *namespacePinReservation, namespacePinRecord, RuntimeRecord) {
	t.Helper()
	pin, record := namespacePinRecordFixture(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	lock, err := lockNamespacePinsAt(context.Background(), control, record.Launch.Binding.Incarnation, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	reservation, err := lock.reserve(pin.Scope, record, pin.Scope.ContainerID, pin.Plan)
	if err != nil {
		t.Fatal(err)
	}
	files := reservation.files
	t.Cleanup(func() { _ = files.Close() })
	var actual namespacePinRecord
	if err := json.Unmarshal(files.current, &actual); err != nil {
		t.Fatal(err)
	}
	return root, control, lock, reservation, actual, record
}

func namespacePinFilesFixture(t *testing.T) (string, *os.File, *namespacePinLock, *namespacePinFiles, namespacePinRecord, RuntimeRecord) {
	t.Helper()
	root, control, lock, reservation, pin, record := namespacePinReservationFixture(t)
	return root, control, lock, reservation.files, pin, record
}

func TestNamespacePinFilesExternalOriginAndObservationCAS(t *testing.T) {
	root, _, lock, files, pin, record := namespacePinFilesFixture(t)
	inc := filepath.Join(root, "namespace-pins", lock.incarnation)
	originName := filepath.Join(inc, "origins", record.Generation+".json")
	original, err := os.ReadFile(originName)
	if err != nil || !bytes.Equal(original, files.origin) {
		t.Fatal("external immutable origin absent", err)
	}
	stale, err := lock.read(record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stale.Close() }()
	// Synthetic bookkeeping only: no actual mount or mount ACK is supplied.
	pin.Phase, pin.DirectoryMountID, pin.PinMountID = pinPresent, 122, 123
	if err := files.writeObservation(pin); err != nil {
		t.Fatal(err)
	}
	pin.PinMountID = 456
	if err := stale.writeObservation(pin); err == nil {
		t.Fatal("stale observation overwrote newer bookkeeping")
	}
	after, err := os.ReadFile(originName)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("mutable observation rewrote immutable external origin", err)
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	if err := files.writeObservation(pin); err == nil {
		t.Fatal("closed metadata handle published")
	}
	cold, err := lock.read(record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal("cold metadata demanded live historical PID", err)
	}
	defer func() { _ = cold.Close() }()
	var got namespacePinRecord
	if err := json.Unmarshal(cold.current, &got); err != nil || got.PinMountID != 123 {
		t.Fatal("cold observation changed progress", err)
	}
}

func TestNamespacePinFilesRefuseExistingOrPartialReservation(t *testing.T) {
	root, _, lock, _, pin, record := namespacePinFilesFixture(t)
	state := filepath.Join(root, "namespace-pins", lock.incarnation, "pins", record.Generation, "state.json")
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.read(record, pin.Scope.ContainerID); err == nil {
		t.Fatal("partial state repaired/adopted")
	}
	if _, err := lock.reserve(pin.Scope, record, pin.Scope.ContainerID, pin.Plan); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing generation reservation retried", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed reservation recreated missing state", err)
	}
}

func TestNamespacePinFilesExternalOriginRejectsWholeDirectoryReplacement(t *testing.T) {
	root, _, lock, _, pin, record := namespacePinFilesFixture(t)
	dir := filepath.Join(root, "namespace-pins", lock.incarnation, "pins", record.Generation)
	state, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.read(record, pin.Scope.ContainerID); err == nil {
		t.Fatal("byte-identical state in replacement directory accepted")
	}
}

func TestNamespacePinFilesRejectForeignOrMalformedLeaves(t *testing.T) {
	for _, change := range []string{"forged-state-origin", "state-link", "state-hardlink", "state-mode", "state-directory", "origin-link", "origin-unknown", "parent-mode", "lock-replacement", "oversize"} {
		t.Run(change, func(t *testing.T) {
			root, _, lock, _, pin, record := namespacePinFilesFixture(t)
			inc := filepath.Join(root, "namespace-pins", lock.incarnation)
			state := filepath.Join(inc, "pins", record.Generation, "state.json")
			origin := filepath.Join(inc, "origins", record.Generation+".json")
			switch change {
			case "forged-state-origin":
				pin.Scope.KernelBootID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
				pin.OriginSHA256, _ = pin.originIdentity()
				raw, _ := json.Marshal(pin)
				if err := os.WriteFile(state, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "state-link", "origin-link":
				name := state
				if change == "origin-link" {
					name = origin
				}
				if err := os.Rename(name, name+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".retained", name); err != nil {
					t.Fatal(err)
				}
			case "state-hardlink":
				if err := os.Link(state, state+".alias"); err != nil {
					t.Fatal(err)
				}
			case "state-mode", "parent-mode":
				name := state
				if change == "parent-mode" {
					name = filepath.Join(inc, "origins")
				}
				if err := os.Chmod(name, 0o755); err != nil {
					t.Fatal(err)
				}
			case "state-directory":
				if err := os.Rename(state, state+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(state, 0o700); err != nil {
					t.Fatal(err)
				}
			case "origin-unknown":
				raw, err := os.ReadFile(origin)
				if err != nil {
					t.Fatal(err)
				}
				raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
				if err := os.WriteFile(origin, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "lock-replacement":
				name := filepath.Join(inc, ".lock")
				if err := os.Rename(name, name+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(state, []byte(strings.Repeat("x", (4<<20)+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := lock.read(record, pin.Scope.ContainerID); err == nil {
				t.Fatal("foreign/malformed namespace bookkeeping accepted")
			}
		})
	}
}

func TestNamespacePinFilesStableLockSerializesAndCancellationIsBounded(t *testing.T) {
	_, control, lock, files, pin, record := namespacePinFilesFixture(t)
	before := lock.identity
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		other, err := lockNamespacePinsAt(ctx, control, lock.incarnation, os.Geteuid(), os.Getegid())
		if other != nil {
			_ = other.Close()
		}
		done <- err
	}()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("competing transaction passed the held lock", err)
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	incarnation := lock.incarnation
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.read(record, pin.Scope.ContainerID); err == nil {
		t.Fatal("closed lock used for metadata")
	}
	other, err := lockNamespacePinsAt(context.Background(), control, incarnation, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if other.identity != before {
		t.Fatal("stable lock inode replaced on reopen")
	}
	cold, err := other.read(record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	_ = cold.Close()
}

func TestNamespacePinFilesRefuseForeignSelectionBeforeReservation(t *testing.T) {
	root, _, lock, _, pin, record := namespacePinFilesFixture(t)
	inc := filepath.Join(root, "namespace-pins", lock.incarnation)
	beforePins, err := os.ReadDir(filepath.Join(inc, "pins"))
	if err != nil {
		t.Fatal(err)
	}
	beforeOrigins, err := os.ReadDir(filepath.Join(inc, "origins"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"cid", "scope", "plan"} {
		scope, plan, cid := pin.Scope, pin.Plan, pin.Scope.ContainerID
		switch change {
		case "cid":
			cid = strings.Repeat("b", 64)
		case "scope":
			scope.Binding.Incarnation = strings.Repeat("b", 32)
		case "plan":
			plan.Inputs.OwnerSHA256 = strings.Repeat("b", 64)
		}
		if _, err := lock.reserve(scope, record, cid, plan); err == nil || errors.Is(err, os.ErrExist) {
			t.Fatalf("foreign %s reached existing-directory reservation: %v", change, err)
		}
	}
	afterPins, err := os.ReadDir(filepath.Join(inc, "pins"))
	if err != nil || len(afterPins) != len(beforePins) {
		t.Fatal("foreign selection changed pin inventory", err)
	}
	afterOrigins, err := os.ReadDir(filepath.Join(inc, "origins"))
	if err != nil || len(afterOrigins) != len(beforeOrigins) {
		t.Fatal("foreign selection changed origin inventory", err)
	}
}

func TestNamespacePinFilesRefusePublicRootAndArbitraryPublication(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = control.Close() }()
	if lock, err := lockNamespacePinsAt(context.Background(), control, strings.Repeat("a", 32), os.Geteuid(), os.Getegid()); err == nil || lock != nil {
		t.Fatal("untrusted root accepted", err)
	}
	for _, name := range []string{"../state.json", "/state.json", strings.Repeat("a", 64), strings.Repeat("a", 64) + ".json", "g" + strings.Repeat("A", 48) + ".json", "g" + strings.Repeat("a", 48), ".lock", "namespace/child"} {
		if err := writePinFileAt(int(control.Fd()), name, nil, false, os.Geteuid(), os.Getegid()); err == nil {
			t.Fatal("arbitrary publication name accepted", name)
		}
	}
	if err := writePinFileAt(int(control.Fd()), "g"+strings.Repeat("a", 48)+".json", nil, true, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("immutable origin replacement accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected root/name caused publication", err)
	}
}
