package lima

import (
	"strings"
	"testing"
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
