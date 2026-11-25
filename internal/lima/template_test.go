package lima

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

func TestTemplatePinsFullContainerStackForBothGuestArchitectures(t *testing.T) {
	manifest, err := os.ReadFile("../../assets/vm/firecracker/pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins struct {
		Nerdctl struct {
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		} `json:"nerdctl.tgz"`
	}
	if err := json.Unmarshal(manifest, &pins); err != nil {
		t.Fatal(err)
	}
	for _, format := range []bool{false, true} {
		var tmpl struct {
			Containerd struct {
				System   bool `yaml:"system"`
				User     bool `yaml:"user"`
				Archives []struct {
					Location string `yaml:"location"`
					Arch     string `yaml:"arch"`
					Digest   string `yaml:"digest"`
				} `yaml:"archives"`
			} `yaml:"containerd"`
		}
		if err := yaml.Unmarshal(TemplateWithAccountDisk(nil, testAccountDiskID, format), &tmpl); err != nil {
			t.Fatal(err)
		}
		if !tmpl.Containerd.System || tmpl.Containerd.User || len(tmpl.Containerd.Archives) != 2 {
			t.Fatalf("format=%t: expected system containerd with two full archives: %+v", format, tmpl.Containerd)
		}
		arches := map[string]struct{ arch, sha string }{
			"x86_64":  {"amd64", "1f12c1a5840744fdf7029483c55a69813d44aa2ec1ef879d52d81ff6a3049ecd"},
			"aarch64": {"arm64", "fe0ef0e9840ee8932640e6058f84b7fa98050f9726e44af44883a5b804ae830e"},
		}
		for _, archive := range tmpl.Containerd.Archives {
			want, ok := arches[archive.Arch]
			if !ok {
				t.Fatalf("unexpected or duplicate guest architecture %q", archive.Arch)
			}
			delete(arches, archive.Arch)
			url := "https://github.com/containerd/nerdctl/releases/download/v2.4.2/nerdctl-full-2.4.2-linux-" + want.arch + ".tar.gz"
			if archive.Location != url || archive.Digest != "sha256:"+want.sha {
				t.Fatalf("unexpected full stack pin: %+v", archive)
			}
			if archive.Arch == "x86_64" && (archive.Location != pins.Nerdctl.URL || archive.Digest != "sha256:"+pins.Nerdctl.SHA256) {
				t.Fatalf("Lima amd64 stack differs from Firecracker: %+v", archive)
			}
		}
	}
}

func TestTemplatePinsMinimalImagesForBothGuestArchitectures(t *testing.T) {
	var tmpl struct {
		Images []struct {
			Location string `yaml:"location"`
			Arch     string `yaml:"arch"`
			Digest   string `yaml:"digest"`
		} `yaml:"images"`
	}
	if err := yaml.Unmarshal(TemplateWithAccountDisk(nil, "", false), &tmpl); err != nil {
		t.Fatal(err)
	}
	if len(tmpl.Images) != 2 {
		t.Fatalf("expected two pinned images, got %d", len(tmpl.Images))
	}
	arches := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}
	for _, img := range tmpl.Images {
		arch, ok := arches[img.Arch]
		if !ok {
			t.Fatalf("unexpected or duplicate guest architecture %q", img.Arch)
		}
		delete(arches, img.Arch)
		if !strings.HasPrefix(img.Location, "https://cloud-images.ubuntu.com/minimal/releases/noble/release-") ||
			!strings.HasSuffix(img.Location, "/ubuntu-24.04-minimal-cloudimg-"+arch+".img") ||
			!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(img.Digest) {
			t.Fatalf("image must use a dated official Minimal release and checksum: %+v", img)
		}
	}
}

func TestTemplateUsesLimaHostDefaults(t *testing.T) {
	// The same template must work for native Linux/macOS binaries and an
	// amd64 binary running under Rosetta on Apple Silicon. Lima knows the
	// actual host; selecting QEMU from runtime.GOARCH breaks that last case.
	for _, format := range []bool{false, true} {
		result := TemplateWithAccountDisk([]Mount{{Location: "/project", Writable: true}}, testAccountDiskID, format)
		var fields map[string]any
		if err := yaml.Unmarshal(result, &fields); err != nil {
			t.Fatalf("unmarshal rendered template: %v", err)
		}
		for _, key := range []string{"vmType", "arch", "mountType"} {
			if value, ok := fields[key]; ok {
				t.Errorf("format=%t: template overrides Lima's %s default with %v", format, key, value)
			}
		}

		// Delegating transport selection must retain explicit host grants and
		// the separately owned account disk, including formatting authority.
		var tmpl struct {
			Mounts []Mount `yaml:"mounts"`
			Disks  []struct {
				Name   string `yaml:"name"`
				Format bool   `yaml:"format"`
				FSType string `yaml:"fsType"`
			} `yaml:"additionalDisks"`
		}
		if err := yaml.Unmarshal(result, &tmpl); err != nil {
			t.Fatal(err)
		}
		if len(tmpl.Mounts) != 1 || tmpl.Mounts[0].Location != "/project" || !tmpl.Mounts[0].Writable {
			t.Fatalf("unexpected host grants: %+v", tmpl.Mounts)
		}
		if len(tmpl.Disks) != 1 || tmpl.Disks[0].Name != layout.AccountDiskName(testAccountDiskID) ||
			tmpl.Disks[0].Format != format || tmpl.Disks[0].FSType != "ext4" {
			t.Fatalf("unexpected account disk: %+v", tmpl.Disks)
		}
	}
}
