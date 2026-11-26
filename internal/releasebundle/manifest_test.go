package releasebundle

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCurrentRejectsInvalidNotices(t *testing.T) {
	for _, kind := range []string{"missing", "changed", "symlink", "empty"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			m := Manifest{Schema: CurrentSchema, Version: "v1.2.3", Channel: "release", Bundle: strings.Repeat("a", 32), OS: "darwin", Arch: "arm64", Files: map[string]string{}}
			names, err := m.Payload()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				content := []byte(name)
				dst := filepath.Join(dir, name)
				if name == NoticesFile {
					switch kind {
					case "missing":
						m.Files[name] = fmt.Sprintf("%x", sha256.Sum256(content))
						continue
					case "changed":
						m.Files[name] = fmt.Sprintf("%x", sha256.Sum256(content))
						content = []byte("changed")
					case "symlink":
						m.Files[name] = fmt.Sprintf("%x", sha256.Sum256([]byte("LICENSE")))
						if err := os.Symlink("LICENSE", dst); err != nil {
							t.Fatal(err)
						}
						continue
					case "empty":
						content = nil
					}
				}
				if _, set := m.Files[name]; !set {
					m.Files[name] = fmt.Sprintf("%x", sha256.Sum256(content))
				}
				if err := os.WriteFile(dst, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "MANIFEST.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"missing": "unexpected bundle entries", "changed": "checksum mismatch: NOTICES", "symlink": "NOTICES", "empty": "NOTICES is empty"}
			if _, err := VerifyCurrent(dir); err == nil || !strings.Contains(err.Error(), want[kind]) {
				t.Fatalf("notice verification: %v, want %q", err, want[kind])
			}
		})
	}
}

func TestManifestIdentityAndPlatformContract(t *testing.T) {
	for _, schema := range []int{LegacySchema, CurrentSchema} {
		for _, osname := range []string{"linux", "darwin"} {
			for _, arch := range []string{"amd64", "arm64"} {
				m := Manifest{Schema: schema, Version: "v1.2.3-qa.1", Channel: "release", Bundle: strings.Repeat("a", 32), OS: osname, Arch: arch, Files: map[string]string{}}
				names, err := m.Payload()
				if err != nil {
					t.Fatal(err)
				}
				checksum := strings.Repeat("a", 64)
				for _, name := range names {
					m.Files[name] = checksum
				}
				if err := m.Validate(); err != nil {
					t.Fatal(err)
				}
				for _, companion := range []string{"yoloctl-linux-amd64", "yoloctl-linux-arm64"} {
					if _, ok := m.Files[companion]; !ok {
						t.Fatalf("missing %s", companion)
					}
				}
				if _, hasNotices := m.Files[NoticesFile]; hasNotices != (schema == CurrentSchema) {
					t.Fatalf("unexpected notice requirement for schema %d", schema)
				}
				if _, hasInit := m.Files["yoloctl-vm-init-linux-amd64"]; hasInit != (osname == "linux" && arch == "amd64") {
					t.Fatalf("unexpected VM init requirement for %s/%s", osname, arch)
				}
				if schema == CurrentSchema {
					delete(m.Files, NoticesFile)
				} else {
					m.Files[NoticesFile] = checksum
				}
				if m.Validate() == nil {
					t.Fatalf("accepted the wrong notice set for schema %d", schema)
				}
				if schema == CurrentSchema {
					m.Files[NoticesFile] = checksum
				} else {
					delete(m.Files, NoticesFile)
				}
				m.Bundle = "../escape"
				if m.Validate() == nil {
					t.Fatal("path escape")
				}
			}
		}
	}
}

func TestManifestRejectsAmbiguousAndUnknownJSON(t *testing.T) {
	for _, raw := range []string{`{"schema":1,"schema":2}`, `{"unknown":1}`, `{} {}`, `{"schema":1}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "MANIFEST.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(dir); err == nil {
			t.Fatalf("accepted: %s", raw)
		}
	}
}
