//go:build linux

package agentbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testRemovalSpace = nativeRemovalSpace{Space: "s", DiskID: "d", DiskUUID: "u"}

func writeTestOrigin(t *testing.T, root, incarnation, box string) string {
	t.Helper()
	dir := filepath.Join(root, incarnation)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(nativeRemovalOrigin{Space: "s", DiskID: "d", DiskUUID: "u", Box: box, Incarnation: incarnation,
		Qualification: strings.Repeat("a", 64), HelperSHA256: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "origin.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func removalFixture(t *testing.T) (string, uint32, uint32) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "containers")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root, uint32(os.Getuid()), uint32(os.Getgid())
}

func TestMissingNativeRemovalRetainsStaleSameBoxRecordWithNotice(t *testing.T) {
	root, uid, gid := removalFixture(t)
	stale := writeTestOrigin(t, root, "00112233445566778899aabbccddeeff", "coder")
	writeTestOrigin(t, root, "ffeeddccbbaa99887766554433221100", "peer")
	notices := nativeRemovalNotices(context.Background(), root, "coder", testRemovalSpace, uid, gid)
	if len(notices) != 1 || !strings.Contains(notices[0], stale) || !strings.Contains(notices[0], "retained (not deleted)") {
		t.Fatalf("want one retained notice naming %s, got %q", stale, notices)
	}
	if _, err := os.Stat(filepath.Join(stale, "origin.json")); err != nil {
		t.Fatalf("stale record must be left in place: %v", err)
	}
}

func TestMissingNativeRemovalNeverRefusesOnOtherOrBadRecords(t *testing.T) {
	root, uid, gid := removalFixture(t)
	writeTestOrigin(t, root, "ffeeddccbbaa99887766554433221100", "peer")
	if notices := nativeRemovalNotices(context.Background(), root, "coder", testRemovalSpace, uid, gid); len(notices) != 0 {
		t.Fatalf("another box's valid record is not reported: %q", notices)
	}
	bad := filepath.Join(root, "not-an-incarnation")
	if err := os.Mkdir(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := writeTestOrigin(t, root, "0123456789abcdef0123456789abcdef", "coder")
	if err := os.WriteFile(filepath.Join(foreign, "origin.json"), []byte(`{"space":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	notices := nativeRemovalNotices(context.Background(), root, "coder", testRemovalSpace, uid, gid)
	if len(notices) != 2 || !strings.Contains(strings.Join(notices, "\n"), bad) || !strings.Contains(strings.Join(notices, "\n"), foreign) {
		t.Fatalf("unreadable records must be named, not refused: %q", notices)
	}
	for _, n := range notices {
		if !strings.Contains(n, "left in place") {
			t.Fatalf("notice must say the record was left: %q", n)
		}
	}
}

func TestMissingNativeRemovalAbsentDirectoryIsSilent(t *testing.T) {
	_, uid, gid := removalFixture(t)
	if notices := nativeRemovalNotices(context.Background(), filepath.Join(t.TempDir(), "missing"), "coder", testRemovalSpace, uid, gid); notices != nil {
		t.Fatalf("absent records directory: %q", notices)
	}
}

func TestMissingNativeRemovalReportsUnexpectedRootOwnership(t *testing.T) {
	root, uid, gid := removalFixture(t)
	writeTestOrigin(t, root, "00112233445566778899aabbccddeeff", "coder")
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	notices := nativeRemovalNotices(context.Background(), root, "coder", testRemovalSpace, uid, gid)
	if len(notices) != 1 || !strings.Contains(notices[0], "left in place") {
		t.Fatalf("writable records root must be reported, not trusted: %q", notices)
	}
}
