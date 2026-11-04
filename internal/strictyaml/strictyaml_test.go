package strictyaml

import (
	"strings"
	"testing"
)

type doc struct {
	Name    string            `yaml:"name"`
	Secrets map[string]string `yaml:"secrets,omitempty"`
}

func TestDecodeValid(t *testing.T) {
	var d doc
	if err := Decode([]byte("name: a\nsecrets:\n  K: v\n"), &d); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Name != "a" || d.Secrets["K"] != "v" {
		t.Errorf("decoded %+v", d)
	}
}

// TestDecodeRejectsUnknownField: a misspelled key is an error naming the
// field, never a silently dropped mapping (fail closed).
func TestDecodeRejectsUnknownField(t *testing.T) {
	var d doc
	err := Decode([]byte("name: a\nsecretz:\n  K: v\n"), &d)
	if err == nil {
		t.Fatal("unknown field accepted")
	}
	if !strings.Contains(err.Error(), "secretz") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

// TestDecodeRejectsTrailingDocument: only one document per manifest — a
// second `---` document cannot ride along unread.
func TestDecodeRejectsTrailingDocument(t *testing.T) {
	var d doc
	err := Decode([]byte("name: a\n---\nname: b\n"), &d)
	if err == nil {
		t.Fatal("trailing document accepted")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error %q should mention the trailing document", err)
	}
	// An explicit empty second document is still a second document.
	if err := Decode([]byte("name: a\n---\n"), &doc{}); err == nil {
		t.Error("empty trailing document accepted")
	}
}

// TestDecodeEmptyInput: empty and comment-only input decode to the zero
// value (like yaml.Unmarshal); the caller's own validation rejects them with
// its context.
func TestDecodeEmptyInput(t *testing.T) {
	for _, raw := range []string{"", "\n", "# just a comment\n"} {
		var d doc
		if err := Decode([]byte(raw), &d); err != nil {
			t.Errorf("Decode(%q): %v", raw, err)
		}
		if d.Name != "" {
			t.Errorf("Decode(%q) produced %+v, want zero value", raw, d)
		}
	}
}
