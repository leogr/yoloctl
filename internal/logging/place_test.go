package logging

import (
	"testing"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestLabelMapsEveryActor(t *testing.T) {
	for actor, want := range map[string][2]string{
		Host: {"host", "yoloctl"}, VM: {"host", "vm"}, Install: {"host", "install"},
		Controller: {"space", "controller"}, Builder: {"space", "builder"}, Box: {"space", "box"},
		Session: {"space", "session"}, Storage: {"space", "storage"}, Audit: {"space", "audit"}, Chat: {"space", "chat"},
		"unknown": {"", "unknown"},
	} {
		if p, n := Label(actor); p != want[0] || n != want[1] {
			t.Errorf("%s -> %s▸%s", actor, p, n)
		}
	}
	// The longest label fits the column with room for the gap.
	if w := len("space") + 1 + len("controller"); w > LabelWidth {
		t.Fatalf("label %d wider than %d", w, LabelWidth)
	}
}

// ParseLabel reads both this release's and earlier plain labels.
func TestParseLabelBothForms(t *testing.T) {
	for _, tc := range []struct {
		head, actor, executor string
		tool, ok              bool
	}{
		{"host>yoloctl", Host, "", true, true},
		{"host>vm", VM, "", true, true},
		{"space>builder", Builder, "", true, true},
		{"space>vm", "", "", true, false},     // vm runs on the host
		{"host>builder", "", "", true, false}, // builder runs in the space
		{"builder/buildkit", Builder, "buildkit", false, true},
		{"controller", Controller, "", false, true},
		{"falco", "", "", false, false},
		{">external", "", "", false, false}, // an empty place is not a yoloctl label
		{"space>", "", "", false, false},
	} {
		actor, executor, tool, ok := ParseLabel(tc.head)
		if ok != tc.ok || ok && (actor != tc.actor || executor != tc.executor || tool != tc.tool) {
			t.Errorf("%q -> %q %q %v %v", tc.head, actor, executor, tool, ok)
		}
	}
	for text, want := range map[string]string{"buildkit: Image ready": "buildkit", "kernel: [0.0] x": "kernel",
		"autoupdate: checking": "", "openshell_supervisor: x": "", "Image ready: done": "", "two words: x": "", "": ""} {
		if tool, _, _ := CutTool(text); tool != want {
			t.Errorf("CutTool(%q) = %q", text, tool)
		}
	}
}

// A named space is hidden only at normal info, and each outer command starts
// with nothing named.
func TestHideNamedSpace(t *testing.T) {
	BeginCommand()
	t.Cleanup(BeginCommand)
	if HideNamedSpace(KeySpace, "falco") {
		t.Fatal("hidden before it was named")
	}
	if !NameSpace("falco") || NameSpace("falco") {
		t.Fatal("NameSpace must report only the first naming")
	}
	l := prettylog.New(nil, prettylog.Options{Format: prettylog.FormatWire})
	for v, want := range map[prettylog.Verbosity]bool{prettylog.Normal: true, prettylog.Quiet: false, prettylog.Errors: false, prettylog.Trace: false} {
		restore := SetLogger(l)
		mu.Lock()
		settings.Verbosity = v
		mu.Unlock()
		if got := HideNamedSpace(KeySpace, "falco"); got != want {
			t.Errorf("verbosity %v: hidden=%v", v, got)
		}
		restore()
	}
	if HideNamedSpace(KeySpace, "other") || HideNamedSpace(KeyBox, "falco") {
		t.Fatal("only the named space's field is hidden")
	}
	BeginCommand()
	if NameSpace("falco") == false {
		t.Fatal("a new outer command names the space again")
	}
}
