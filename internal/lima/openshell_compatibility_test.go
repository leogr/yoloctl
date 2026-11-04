package lima

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenShellVMCompatibilityUsesRetainedProvisioning(t *testing.T) {
	current := TemplateWithAccountDisk(nil, "", false)
	if err := checkOpenShellVMConfig(current); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.ReplaceAll(string(current), OpenShellVMMarker, "# old provisioning"), strings.ReplaceAll(string(current), OpenShellVMMarker, "# yoloctl-openshell-vm: nftables-v1"), "# " + OpenShellVMMarker + "\nprovision: []\n", "provision: [{mode: user, script: '" + OpenShellVMMarker + "'}]"} {
		if err := checkOpenShellVMConfig([]byte(raw)); !errors.Is(err, ErrOpenShellVMTooOld) {
			t.Fatal("old/foreign marker accepted", err)
		}
	}
	if err := checkOpenShellVMConfig([]byte("provision: [")); err == nil || errors.Is(err, ErrOpenShellVMTooOld) {
		t.Fatal("malformed YAML became old-VM evidence", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := InstanceDir("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lima.yaml")
	if err := os.WriteFile(path, current, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckOpenShellVM("fixture"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(current) {
		t.Fatal("compatibility read changed VM configuration", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (4<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckOpenShellVM("fixture"); err == nil {
		t.Fatal("unbounded configuration accepted")
	}
}
func TestOpenShellNftablesProvisioningUsesMatchingDistroPackage(t *testing.T) {
	script := provisionScript(t, OpenShellVMMarker)
	for _, initial := range []string{"missing", "1.0.9-1build1", "1.0.9-1ubuntu0.1"} {
		t.Run(initial, func(t *testing.T) {
			dir := t.TempDir()
			state, log, binary := filepath.Join(dir, "version"), filepath.Join(dir, "calls"), filepath.Join(dir, "nft")
			provisionWrite(t, state, initial, 0600)
			t.Setenv("NFT_FIXTURE_STATE", state)
			t.Setenv("NFT_FIXTURE_LOG", log)
			t.Setenv("NFT_FIXTURE_BINARY", binary)
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			provisionWrite(t, filepath.Join(dir, "dpkg-query"), `#!/bin/sh
case "$2" in *Status*) [ "$(cat "$NFT_FIXTURE_STATE")" != missing ] && printf 'install ok installed';; *) exit 2;; esac
`, 0755)
			provisionWrite(t, filepath.Join(dir, "apt-get"), `#!/bin/sh
printf '%s\n' "$*" >> "$NFT_FIXTURE_LOG"
if [ "$1" = install ]; then
 printf 1.0.9-1ubuntu0.1 > "$NFT_FIXTURE_STATE"
 printf '#!/bin/sh\nexit 0\n' > "$NFT_FIXTURE_BINARY"
 chmod 755 "$NFT_FIXTURE_BINARY"
fi
`, 0755)
			if initial != "missing" {
				provisionWrite(t, binary, "#!/bin/sh\nexit 0\n", 0755)
			}
			fixture := strings.ReplaceAll(script, "/usr/sbin/nft", binary)
			runProvision(t, fixture, true)
			runProvision(t, fixture, true)
			calls, err := os.ReadFile(log)
			if initial != "missing" {
				if !os.IsNotExist(err) {
					t.Fatalf("installed nft contacted apt: %s %v", calls, err)
				}
			} else if err != nil || string(calls) != "update\ninstall -y --no-install-recommends nftables\n" {
				t.Fatalf("wrong package selection/retry: %s %v", calls, err)
			}
		})
	}
}

func TestOpenShellNftablesMissingBinaryRefusesProvisionAndReadiness(t *testing.T) {
	script := provisionScript(t, OpenShellVMMarker)
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	provisionWrite(t, filepath.Join(dir, "dpkg-query"), "#!/bin/sh\nprintf 'install ok installed'\n", 0755)
	// Even an apt success without its executable cannot qualify this VM.
	provisionWrite(t, filepath.Join(dir, "apt-get"), "#!/bin/sh\nexit 0\n", 0755)
	binary := filepath.Join(dir, "nft")
	runProvision(t, strings.ReplaceAll(script, "/usr/sbin/nft", binary), false)
	var config struct {
		Probes []struct{ Mode, Description, Script string }
	}
	if err := yaml.Unmarshal(TemplateWithAccountDisk(nil, "", false), &config); err != nil {
		t.Fatal(err)
	}
	var readiness string
	for _, p := range config.Probes {
		if p.Mode == "readiness" && strings.Contains(p.Script, "/usr/sbin/nft") {
			readiness = p.Script
		}
	}
	if readiness == "" {
		t.Fatal("Lima readiness ignores failed nft provisioning")
	}
	fixture := strings.ReplaceAll(readiness, "/usr/sbin/nft", binary)
	runProvision(t, fixture, false)
	provisionWrite(t, binary, "#!/bin/sh\nexit 1\n", 0755)
	runProvision(t, fixture, false)
	provisionWrite(t, binary, "#!/bin/sh\nexit 0\n", 0755)
	runProvision(t, fixture, true)
}
