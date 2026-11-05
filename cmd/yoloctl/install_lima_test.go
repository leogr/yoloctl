package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func TestInstallerLimaAdviceRequiresProvedEffectivePins(t *testing.T) {
	for _, scenario := range []string{"no-pins", "equal", "different", "missing", "inherited", "global-override", "global-default", "unreadable", "unparsable", "invalid-containerd", "invalid-archives"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("YOLOCTL_HOME", t.TempDir())
			boot, _ := resourceSpaceFixture(t)
			desired := []byte("containerd:\n  archives:\n  - arch: x86_64\n    location: https://example.test/nerdctl.tar.gz\n    digest: sha256:" + strings.Repeat("a", 64) + "\n")
			old := vmhost.TemplateWithAccountDisk
			t.Cleanup(func() { vmhost.TemplateWithAccountDisk = old })
			vmhost.TemplateWithAccountDisk = func([]vmhost.Mount, string, bool) []byte {
				if scenario == "no-pins" {
					return []byte("containerd:\n  system: true\n")
				}
				return desired
			}
			home, err := layout.LimaHome()
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "no-pins" {
				dir, err := vmhost.InstanceDir(boot.Name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				raw := desired
				switch scenario {
				case "different":
					raw = []byte(strings.ReplaceAll(string(desired), strings.Repeat("a", 64), strings.Repeat("b", 64)))
				case "missing":
					raw = []byte("containerd:\n  system: true\n")
				case "inherited":
					raw = []byte("base: [template:external]\n")
				case "unparsable":
					raw = []byte("containerd: [broken\n")
				case "invalid-containerd":
					raw = []byte("containerd: invalid\n")
				case "invalid-archives":
					raw = []byte("containerd:\n  archives: invalid\n")
				}
				if scenario != "unreadable" {
					if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "global-override" || scenario == "global-default" {
					if err := os.Mkdir(filepath.Join(home, "_config"), 0700); err != nil {
						t.Fatal(err)
					}
					name := "override.yaml"
					if scenario == "global-default" {
						name = "default.yaml"
					}
					if err := os.WriteFile(filepath.Join(home, "_config", name), desired, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			command, note := installLimaAdvice(boot.Name)
			if scenario == "different" || scenario == "missing" {
				if command == nil || command.Label != "optional" || command.Args[1] != "recreate" || !strings.Contains(command.Note, "starts the VM") {
					t.Fatal(command, note)
				}
				if scenario == "missing" && (!strings.Contains(command.Note, "unpinned") || !strings.Contains(command.Note, "host's Lima default")) {
					t.Fatal("missing unpinned-stack note", command.Note)
				}
			} else if command != nil {
				t.Fatal("unproved recreation", scenario, command, note)
			}
			if scenario == "inherited" || scenario == "global-override" || scenario == "global-default" || scenario == "unreadable" || scenario == "unparsable" || scenario == "invalid-containerd" || scenario == "invalid-archives" {
				if !strings.Contains(note, "could not") {
					t.Fatal(note)
				}
			}
			if scenario == "no-pins" {
				if _, err := os.Stat(home); !os.IsNotExist(err) {
					t.Fatal("no-pin proposal created Lima storage")
				}
			}
			lockRoot, err := layout.ConfigRoot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(lockRoot, "locks")); !os.IsNotExist(err) {
				t.Fatal("proposal acquired lifecycle locks")
			}
		})
	}
}
