//go:build linux && amd64

package firecracker

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/ext4sb"
)

// The host reads a formatted disk's UUID and size as e2fsprogs reports
// them, and refuses anything that is not ext4.
func TestSuperblockOfAFormattedDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.ext4")
	if err := sparse(path, 1<<30); err != nil {
		t.Fatal(err)
	}
	if err := formatDisk(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sb, err := ext4sb.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if sb.Size != 1<<30 || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(sb.UUID) {
		t.Fatalf("%+v", sb)
	}
	if dumpe2fs, err := exec.LookPath("dumpe2fs"); err == nil {
		out, err := exec.Command(dumpe2fs, "-h", path).Output()
		if err != nil || !strings.Contains(string(out), sb.UUID) {
			t.Fatalf("dumpe2fs disagrees: %v %s", err, out)
		}
	}
	if _, err := ext4sb.Read(bytes.NewReader(make([]byte, 4096))); err == nil {
		t.Fatal("a zero disk is not ext4")
	}
	if _, err := ext4sb.Read(bytes.NewReader(make([]byte, 100))); err == nil {
		t.Fatal("a short file has no superblock")
	}
}
