package lima

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Execute the rendered guest scripts against fake package/network commands and
// temporary destinations. This verifies provisioning decisions, not a VM boot.
func provisionScript(t *testing.T, match string) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("guest provisioning uses Linux coreutils")
	}
	var tmpl struct {
		Provision []struct {
			Script string `yaml:"script"`
		} `yaml:"provision"`
	}
	if err := yaml.Unmarshal(TemplateWithAccountDisk(nil, "", false), &tmpl); err != nil {
		t.Fatal(err)
	}
	for _, p := range tmpl.Provision {
		if strings.Contains(p.Script, match) {
			return p.Script
		}
	}
	t.Fatalf("no provision script containing %q", match)
	return ""
}

func provisionWrite(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func runProvision(t *testing.T, script string, success bool) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "bash", "-c", script).CombinedOutput()
	if (err == nil) != success {
		t.Fatalf("provision success=%t, want %t: %v\n%s", err == nil, success, err, out)
	}
}

func TestVMRuntimePackageProvisioning(t *testing.T) {
	script := provisionScript(t, "dpkg-query")
	for _, status := range []string{"installed", "missing", "unpacked"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "state")
			log := filepath.Join(dir, "apt.log")
			provisionWrite(t, state, status, 0o600)
			t.Setenv("TEST_PACKAGE_STATE", state)
			t.Setenv("TEST_APT_LOG", log)
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			fakeDpkgVersions(t, dir)
			provisionWrite(t, filepath.Join(dir, "dpkg-query"), `#!/bin/sh
case "$2" in *Version*) printf '`+caMinimum+`'; exit 0;; esac
if [ "$3" = socat ]; then
  case "$(cat "$TEST_PACKAGE_STATE")" in
    missing) exit 1;;
    unpacked) printf 'install ok unpacked'; exit 0;;
  esac
fi
printf 'install ok installed'
`, 0o755)
			provisionWrite(t, filepath.Join(dir, "apt-get"), `#!/bin/sh
echo "$*" >> "$TEST_APT_LOG"
if [ "$1" = install ]; then printf installed > "$TEST_PACKAGE_STATE"; fi
`, 0o755)
			runProvision(t, script, true)
			runProvision(t, script, true)
			got, err := os.ReadFile(log)
			if status == "installed" {
				if !os.IsNotExist(err) {
					t.Fatalf("installed packages invoked apt: %s, %v", got, err)
				}
			} else if string(got) != "update\ninstall -y --no-install-recommends socat\n" {
				t.Fatalf("expected exactly one missing-package repair, got %q, %v", got, err)
			}
		})
	}
}

// The patched noble release (LP: #2171507) and its neighbors, ordered as
// `dpkg --compare-versions` orders them.
const caMinimum = "20260816ubuntu0.24.04.1"

var caVersions = []string{"20240203", "20260601~24.04.1", caMinimum, "20270101~24.04.1"}

// fakeDpkgVersions implements `dpkg --compare-versions A ge B` over caVersions,
// so the decision does not depend on the test host's package manager.
func fakeDpkgVersions(t *testing.T, dir string) {
	t.Helper()
	var ranks strings.Builder
	for i, v := range caVersions {
		fmt.Fprintf(&ranks, "    %s) echo %d;;\n", v, i+1)
	}
	provisionWrite(t, filepath.Join(dir, "dpkg"), `#!/bin/sh
rank() {
  case "$1" in
`+ranks.String()+`    *) echo 0;;
  esac
}
[ "$1" = --compare-versions ] && [ "$3" = ge ] || exit 2
a=$(rank "$2"); b=$(rank "$4")
[ "$a" -gt 0 ] && [ "$b" -gt 0 ] || exit 2
[ "$a" -ge "$b" ]
`, 0o755)
}

// An image's older CA package must be upgraded, not merely detected as
// installed; current or newer packages stay untouched across boots, and a
// mirror that cannot supply the fix stops provisioning.
func TestCACertificatesProvisioningUpgrade(t *testing.T) {
	script := provisionScript(t, "dpkg-query")
	for _, tc := range []struct {
		name, installed, delivered, apt string
		success                         bool
	}{
		{"current", caMinimum, "", "", true},
		{"newer", "20270101~24.04.1", "", "", true},
		{"old", "20260601~24.04.1", caMinimum, "update\ninstall -y --no-install-recommends ca-certificates\n", true},
		{"base-image-2024", "20240203", caMinimum, "update\ninstall -y --no-install-recommends ca-certificates\n", true},
		{"missing", "", caMinimum, "update\ninstall -y --no-install-recommends ca-certificates\n", true},
		{"stale-mirror", "20260601~24.04.1", "20260601~24.04.1", "update\ninstall -y --no-install-recommends ca-certificates\n", false},
	} {
		// One Architecture: all package serves both guest images; the step
		// must not branch on the guest architecture.
		for _, arch := range []string{"x86_64", "aarch64"} {
			t.Run(tc.name+"/"+arch, func(t *testing.T) {
				dir := t.TempDir()
				version := filepath.Join(dir, "ca-version")
				log := filepath.Join(dir, "apt.log")
				provisionWrite(t, version, tc.installed, 0o600)
				t.Setenv("TEST_CA_VERSION", version)
				t.Setenv("TEST_CA_DELIVERED", tc.delivered)
				t.Setenv("TEST_APT_LOG", log)
				t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
				fakeDpkgVersions(t, dir)
				provisionWrite(t, filepath.Join(dir, "uname"), "#!/bin/sh\necho "+arch+"\n", 0o755)
				provisionWrite(t, filepath.Join(dir, "dpkg-query"), `#!/bin/sh
if [ "$3" = ca-certificates ]; then
  [ -s "$TEST_CA_VERSION" ] || exit 1
  case "$2" in *Version*) cat "$TEST_CA_VERSION"; exit 0;; esac
fi
printf 'install ok installed'
`, 0o755)
				provisionWrite(t, filepath.Join(dir, "apt-get"), `#!/bin/sh
echo "$*" >> "$TEST_APT_LOG"
case " $* " in *" ca-certificates "*) printf '%s' "$TEST_CA_DELIVERED" > "$TEST_CA_VERSION";; esac
`, 0o755)
				runProvision(t, script, tc.success)
				if tc.success {
					runProvision(t, script, true)
				}
				got, err := os.ReadFile(log)
				if tc.apt == "" {
					if !os.IsNotExist(err) {
						t.Fatalf("current CA package invoked apt: %s, %v", got, err)
					}
				} else if string(got) != tc.apt {
					t.Fatalf("expected exactly one CA upgrade, got %q, %v", got, err)
				}
			})
		}
	}
}

func TestTemplateDoesNotProvisionRetiredOpenShell(t *testing.T) {
	rendered := string(TemplateWithAccountDisk(nil, "", false))
	for _, retired := range []string{"/opt/openshell", "SANDBOX_VERSION=", "NVIDIA/OpenShell/releases/download", "sandbox-policy.rego"} {
		if strings.Contains(rendered, retired) {
			t.Fatalf("retired OpenShell provisioning remains: %s", retired)
		}
	}
	if !strings.Contains(rendered, "containerd") {
		t.Fatal("unrelated VM provisioning was lost")
	}
}
