package lima

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCatalogMountsRetainDestinationAndReadOnlyOnUpdate(t *testing.T) {
	mounts := []Mount{
		{Location: "/projects", Writable: true},
		{Location: "/host/config/agents", MountPoint: "/etc/yoloctl/catalogs/agents", Uncached: true},
	}
	initial := TemplateWithBootSettings(mounts, strings.Repeat("a", 32), false, 6, "8GiB", "40GiB")
	updated, err := setMountsInYAML([]byte("cpus: 6\nmounts: []\n"), mounts)
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"location": "/projects", "writable": true},
		{"location": "/host/config/agents", "mountPoint": "/etc/yoloctl/catalogs/agents", "writable": false, "9p": map[string]any{"cache": "none"}},
	}
	for name, raw := range map[string][]byte{"create": initial, "update": updated} {
		t.Run(name, func(t *testing.T) {
			var doc struct {
				Mounts []map[string]any `yaml:"mounts"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(doc.Mounts, want) {
				t.Fatalf("catalog mount changed on %s: %#v", name, doc.Mounts)
			}
		})
	}
}

func TestSetMountsInYAML_ReplacesExistingMounts(t *testing.T) {
	// Realistic-ish shape: top-level mapping with mounts and a couple of
	// sibling keys around it.
	in := []byte(`cpus: 4
memory: "4GiB"
mounts:
- location: "~"
  writable: false
containerd:
  system: true
`)
	out, err := setMountsInYAML(in, []Mount{
		{Location: "~", Writable: false},
		{Location: "/tmp/p1", Writable: true},
	})
	if err != nil {
		t.Fatalf("setMountsInYAML: %v", err)
	}
	s := string(out)
	wantContains := []string{
		"cpus: 4",
		"memory: ", // value formatting can vary; just check the key
		`location: "~"`,
		`location: "/tmp/p1"`,
		"writable: true",
		"containerd:",
		"system: true",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in output:\n%s", w, s)
		}
	}
}

func TestSetMountsInYAML_AppendsWhenMissing(t *testing.T) {
	in := []byte(`cpus: 4
containerd:
  system: true
`)
	out, err := setMountsInYAML(in, []Mount{{Location: "/tmp/x", Writable: true}})
	if err != nil {
		t.Fatalf("setMountsInYAML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "mounts:") {
		t.Errorf("expected mounts: key to be appended, got:\n%s", s)
	}
	if !strings.Contains(s, `location: "/tmp/x"`) {
		t.Errorf("expected new mount entry, got:\n%s", s)
	}
}

func TestSetMountsInYAML_EmptyListRendersEmptySequence(t *testing.T) {
	in := []byte(`mounts:
- location: "~"
  writable: false
`)
	out, err := setMountsInYAML(in, nil)
	if err != nil {
		t.Fatalf("setMountsInYAML: %v", err)
	}
	s := string(out)
	if strings.Contains(s, `location: "~"`) {
		t.Errorf("old mount should be removed:\n%s", s)
	}
	if !strings.Contains(s, "mounts:") {
		t.Errorf("mounts: key should still exist:\n%s", s)
	}
}

func TestSetMountsInYAML_PreservesOtherKeysOrder(t *testing.T) {
	in := []byte(`a: 1
b: 2
mounts:
- location: "old"
  writable: false
c: 3
`)
	out, err := setMountsInYAML(in, []Mount{{Location: "new", Writable: true}})
	if err != nil {
		t.Fatalf("setMountsInYAML: %v", err)
	}
	s := string(out)
	aIdx := strings.Index(s, "a:")
	bIdx := strings.Index(s, "b:")
	mIdx := strings.Index(s, "mounts:")
	cIdx := strings.Index(s, "c:")
	if aIdx >= bIdx || bIdx >= mIdx || mIdx >= cIdx {
		t.Errorf("expected order a < b < mounts < c, got positions %d %d %d %d:\n%s",
			aIdx, bIdx, mIdx, cIdx, s)
	}
}
