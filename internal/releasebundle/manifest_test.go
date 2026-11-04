package releasebundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestIdentityAndPlatformContract(t *testing.T) {
	for _, osname := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			names, err := Payload(osname, arch)
			if err != nil {
				t.Fatal(err)
			}
			m := Manifest{Schema: 1, Version: "v1.2.3-qa.1", Channel: "release", Bundle: strings.Repeat("a", 32), OS: osname, Arch: arch, Files: map[string]string{}}
			for _, name := range names {
				m.Files[name] = strings.Repeat("a", 64)
			}
			if err := m.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, ok := m.Files["yoloctl-linux-amd64"]; !ok {
				t.Fatal("missing amd64")
			}
			if _, ok := m.Files["yoloctl-linux-arm64"]; !ok {
				t.Fatal("missing arm64")
			}
			m.Bundle = "../escape"
			if m.Validate() == nil {
				t.Fatal("path escape")
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
