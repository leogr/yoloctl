package openshell

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mainExitFixture writes one generation's record.json and a PID 1 status
// for it, owned by the test user, and returns the record path and its hash.
// mainBase is the trusted root of the fixtures (production: "/").
var mainBase string

func mainExitFixture(t *testing.T, status func(RuntimeRecord) RuntimeStatus) (string, string, RuntimeRecord) {
	t.Helper()
	f := newMaterialFixture(t)
	mainBase = t.TempDir()
	dir := filepath.Join(mainBase, "generations", f.record.Generation)
	if err := os.MkdirAll(filepath.Join(dir, "status"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.record)
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "record.json")
	if err := os.WriteFile(record, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if status != nil {
		body, err := json.Marshal(status(f.record))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status", "runtime.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(raw)
	return record, hex.EncodeToString(sum[:]), f.record
}

func mainExited(code int, phase string) func(RuntimeRecord) RuntimeStatus {
	return func(r RuntimeRecord) RuntimeStatus {
		s := observeRuntimeStatus(r, phase, "main-exited", nil, time.Unix(1_800_000_000, 0))
		s.MainExit = &code
		return s
	}
}

func TestReadMainExitReadsTheSelectedGenerationsCode(t *testing.T) {
	uid, gid := os.Geteuid(), os.Getegid()
	for _, tc := range []struct {
		code  int
		phase string
	}{{23, "stopped"}, {0, "stopped"}, {137, "stopping"}} {
		path, sum, _ := mainExitFixture(t, mainExited(tc.code, tc.phase))
		code, ok, err := readMainExitAt(mainBase, path, sum, uid, gid)
		if err != nil || !ok || code != tc.code {
			t.Fatalf("%+v: code %d ok %v err %v", tc, code, ok, err)
		}
	}
	// A running box's status holds no code.
	path, sum, _ := mainExitFixture(t, func(r RuntimeRecord) RuntimeStatus {
		s := observeRuntimeStatus(r, "ready", "confirmed", nil, time.Unix(1_800_000_000, 0))
		s.SupervisorPID, s.BoundaryPID = 10, 11
		return s
	})
	if code, ok, err := readMainExitAt(mainBase, path, sum, uid, gid); err != nil || ok {
		t.Fatalf("ready status: code %d ok %v err %v", code, ok, err)
	}
}

func TestReadMainExitRefusesAnythingButTheSelectedRecord(t *testing.T) {
	uid, gid := os.Geteuid(), os.Getegid()
	// Another record than the selected one.
	path, sum, _ := mainExitFixture(t, mainExited(23, "stopped"))
	other := sum[:63] + "0"
	if other == sum {
		other = sum[:63] + "1"
	}
	if _, _, err := readMainExitAt(mainBase, path, other, uid, gid); err == nil {
		t.Fatal("a record other than the selected one was read")
	}
	// A status that belongs to another generation.
	path, sum, record := mainExitFixture(t, nil)
	foreign := record
	foreign.SessionID = "other-session"
	body, _ := json.Marshal(mainExited(23, "stopped")(foreign))
	_ = os.WriteFile(filepath.Join(filepath.Dir(path), "status", "runtime.json"), body, 0o600)
	if _, _, err := readMainExitAt(mainBase, path, sum, uid, gid); err == nil {
		t.Fatal("another generation's status was accepted")
	}
	// No status yet, a linked status, a loose mode: errors, never a code.
	path, sum, _ = mainExitFixture(t, nil)
	if _, ok, err := readMainExitAt(mainBase, path, sum, uid, gid); err == nil || ok {
		t.Fatal("a missing status gave a result")
	}
	// A linked status inside the same base (only the link can refuse it).
	path, sum, record = mainExitFixture(t, nil)
	linked := filepath.Join(mainBase, "elsewhere.json")
	body, _ = json.Marshal(mainExited(23, "stopped")(record))
	_ = os.WriteFile(linked, body, 0o600)
	if err := os.Symlink(linked, filepath.Join(filepath.Dir(path), "status", "runtime.json")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readMainExitAt(mainBase, path, sum, uid, gid); err == nil || ok {
		t.Fatal("a linked status was followed")
	}
	// The same status as a regular file is read: the link alone refused it.
	statusPath := filepath.Join(filepath.Dir(path), "status", "runtime.json")
	_ = os.Remove(statusPath)
	if err := os.WriteFile(statusPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, ok, err := readMainExitAt(mainBase, path, sum, uid, gid); err != nil || !ok || code != 23 {
		t.Fatalf("the regular status: code %d ok %v err %v", code, ok, err)
	}
	path, sum, _ = mainExitFixture(t, mainExited(23, "stopped"))
	_ = os.Chmod(filepath.Join(filepath.Dir(path), "status", "runtime.json"), 0o644)
	if _, ok, err := readMainExitAt(mainBase, path, sum, uid, gid); err == nil || ok {
		t.Fatal("a non-private status was read")
	}
	if _, _, err := readMainExitAt(mainBase, "relative/record.json", sum, uid, gid); err == nil {
		t.Fatal("a relative record path was accepted")
	}
}
