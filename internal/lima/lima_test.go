package lima

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseInstances(t *testing.T) {
	input := `{"name":"yoloctl","status":"Running","vmType":"qemu","arch":"x86_64","cpus":4,"memory":4294967296,"disk":42949672960,"dir":"/home/u/.lima/yoloctl"}
{"name":"other","status":"Stopped","vmType":"qemu","arch":"x86_64","cpus":2,"memory":2147483648,"disk":21474836480,"dir":"/home/u/.lima/other"}
`
	got, err := parseInstances(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseInstances: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances, want 2", len(got))
	}
	if got[0].Name != "yoloctl" || got[0].Status != "Running" || got[0].CPUs != 4 {
		t.Errorf("first instance mismatch: %+v", got[0])
	}
	if got[1].Name != "other" || got[1].Status != "Stopped" {
		t.Errorf("second instance mismatch: %+v", got[1])
	}
}

func TestParseInstancesEmpty(t *testing.T) {
	got, err := parseInstances(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseInstances: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d instances, want 0", len(got))
	}
}

func TestDefaultTemplateRenders(t *testing.T) {
	out := TemplateWithAccountDisk(nil, "", false)
	if len(out) == 0 {
		t.Fatal("TemplateWithMounts is empty")
	}
	s := string(out)
	if strings.Contains(s, "{{") || strings.Contains(s, "}}") {
		t.Errorf("rendered template still contains template delimiters: %q", findFirstUnsubstituted(s))
	}
	mustContain := []string{
		"images:",
		"containerd:",
		"provision:",
		"groupadd -g 2000 sandbox",
		"useradd -u 2000",
		"ca-certificates curl socat",
	}
	for _, want := range mustContain {
		if !strings.Contains(s, want) {
			t.Errorf("rendered template missing %q", want)
		}
	}
	for _, retired := range []string{"package openshell.sandbox", "/opt/openshell", "/v0.0.72/"} {
		if strings.Contains(s, retired) {
			t.Errorf("rendered template still provisions retired runtime %q", retired)
		}
	}
}

func TestTemplateAddsLocalHostnameOnce(t *testing.T) {
	var template struct {
		Provision []struct {
			Mode   string `yaml:"mode"`
			Script string `yaml:"script"`
		} `yaml:"provision"`
	}
	if err := yaml.Unmarshal(TemplateWithAccountDisk(nil, "", false), &template); err != nil {
		t.Fatal(err)
	}
	if len(template.Provision) == 0 || template.Provision[0].Mode != "system" {
		t.Fatal("missing initial system provision script")
	}
	const declaration = "add_local_hostname() {\n"
	const invocation = "\n}\nvm_hostname=$(hostname)\nadd_local_hostname \"$vm_hostname\" /etc/hosts\n"
	_, body, hasDeclaration := strings.Cut(template.Provision[0].Script, declaration)
	body, _, hasInvocation := strings.Cut(body, invocation)
	if !hasDeclaration || !hasInvocation {
		t.Fatal("missing local hostname function or invocation")
	}
	script := "set -euo pipefail\n" + declaration + body + "\n}\nadd_local_hostname \"$1\" \"$2\"\n"

	for _, tc := range []struct {
		name, initial, want string
	}{
		{"mixed-case alias", "192.0.2.7 canonical LiMa-WoRk\n", "192.0.2.7 canonical LiMa-WoRk\n"},
		{"unterminated line", "127.0.0.1 localhost", "127.0.0.1 localhost\n127.0.1.1 lima-work\n"},
		{"unterminated comment", "# lima-work", "# lima-work\n127.0.1.1 lima-work\n"},
		{"different aliases", "192.0.2.7 lima-work-other # lima-work\n", "192.0.2.7 lima-work-other # lima-work\n127.0.1.1 lima-work\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hosts")
			if err := os.WriteFile(path, []byte(tc.initial), 0600); err != nil {
				t.Fatal(err)
			}
			for boot := 1; boot <= 2; boot++ {
				cmd := exec.CommandContext(t.Context(), "bash", "-c", script, "hosts-fixture", "lima-work", path)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("boot %d: %v\n%s", boot, err, out)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.want {
					t.Fatalf("boot %d: hosts = %q, want %q", boot, got, tc.want)
				}
			}
		})
	}
}

func findFirstUnsubstituted(s string) string {
	i := strings.Index(s, "{{")
	if i < 0 {
		return ""
	}
	end := strings.Index(s[i:], "}}")
	if end < 0 {
		return s[i:min(i+40, len(s))]
	}
	return s[i : i+end+2]
}
