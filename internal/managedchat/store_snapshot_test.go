package managedchat

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestManifestSnapshotRetainsPrivateStrictBoundedRead(t *testing.T) {
	for _, change := range []string{"symlink", "hardlink", "mode", "directory", "oversize", "unknown-field", "disk"} {
		t.Run(change, func(t *testing.T) {
			s, inventory := fixture(t)
			r := ensure(t, s, inventory)
			path := filepath.Join(s.Disk, layout.ChatManifest)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReadManifestSnapshot(); err != nil || got.ID != r.ID {
				t.Fatal(got, err)
			}
			switch change {
			case "symlink":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".link"); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Chmod(filepath.Join(s.Disk, "config"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(path, bytes.Repeat([]byte(" "), MaxDocument+1), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown-field":
				raw := append(bytes.TrimSpace(before)[:len(bytes.TrimSpace(before))-1], []byte(",\"unknown\":true}")...)
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "disk":
				s.DiskID = NewID()
			}
			if _, err := s.ReadManifestSnapshot(); err == nil {
				t.Fatal("snapshot bypassed private/native validation", change)
			}
		})
	}
}
