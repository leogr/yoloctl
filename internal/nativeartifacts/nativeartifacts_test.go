package nativeartifacts

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReleaseArchiveLookup(t *testing.T) {
	fsys := fstest.MapFS{"embedded/openshell-sandbox-x86_64.tar.gz": {Data: []byte("archive")}}
	got, err := archiveIn(fsys, "embedded", "openshell-sandbox-x86_64.tar.gz")
	if err != nil || string(got) != "archive" {
		t.Fatalf("%q %v", got, err)
	}
	got[0] = 'X'
	if again, _ := archiveIn(fsys, "embedded", "openshell-sandbox-x86_64.tar.gz"); string(again) != "archive" {
		t.Fatal("callers receive a copy")
	}
	if _, err := archiveIn(fsys, "embedded", "openshell-supervisor-x86_64.tar.gz"); !errors.Is(err, ErrNotBuilt) || !strings.Contains(err.Error(), "openshell-supervisor-x86_64.tar.gz") {
		t.Fatalf("absent archive: %v", err)
	}
	for _, bad := range []string{"../x.tar.gz", "x.tgz", "", "A.tar.gz", "embedded/x.tar.gz", ".keep"} {
		if _, err := archiveIn(fsys, "embedded", bad); err == nil || !strings.Contains(err.Error(), "invalid native release archive name") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestSelectionIsAllOrNothing(t *testing.T) {
	full := fstest.MapFS{
		"d/manifest.json":       {Data: []byte(`{"pins":{}}`)},
		"d/manifest.sha256":     {Data: []byte("ab")},
		"d/sandbox-policy.rego": {Data: []byte("package sandbox\n")},
	}
	m, h, r, err := selectionIn(full, "d")
	if err != nil || string(m) != `{"pins":{}}` || h != "ab" || string(r) != "package sandbox\n" {
		t.Fatal(m, h, r, err)
	}
	if m, h, r, err := selectionIn(fstest.MapFS{}, "d"); err != nil || m != nil || h != "" || r != nil {
		t.Fatal("absent selection is not empty", err)
	}
	delete(full, "d/manifest.sha256")
	if _, _, _, err := selectionIn(full, "d"); err == nil || !strings.Contains(err.Error(), "run make build") {
		t.Fatal("partial selection accepted", err)
	}
}
