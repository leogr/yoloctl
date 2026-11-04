package agentprofile

import (
	"bytes"
	"errors"
	"io/fs"
	"reflect"
	"testing"
)

func TestBuiltinNames(t *testing.T) {
	names, err := BuiltinNames()
	if err != nil {
		t.Fatalf("BuiltinNames: %v", err)
	}
	if want := []string{"agy", "claude", "codex", "gemini", "opencode", "pi"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("BuiltinNames = %v, want %v", names, want)
	}
}

func TestBuiltinContent(t *testing.T) {
	raw, err := BuiltinContent("codex")
	if err != nil {
		t.Fatalf("BuiltinContent(codex): %v", err)
	}
	if !bytes.Contains(raw, []byte("name: codex")) {
		t.Fatalf("BuiltinContent(codex) missing 'name: codex':\n%s", raw)
	}
	if _, err := BuiltinContent("not-a-builtin"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("BuiltinContent(not-a-builtin) err = %v, want fs.ErrNotExist", err)
	}
}
