package container

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func TestBuildAndPullGuardBeforeVMCommands(t *testing.T) {
	want := errors.New("low disk")
	var refs []string
	ctx := WithBuildGuard(t.Context(), func(_ context.Context, r []string) error { refs = r; return want })
	c := New("must-not-contact-vm")
	if err := c.Build(ctx, BuildOptions{Tag: "local/result:latest", InputImages: []string{"local/base:latest"}, ContextTar: strings.NewReader("")}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs, []string{"local/result:latest", "local/base:latest"}) {
		t.Fatal(refs)
	}
	if err := c.ImagePull(ctx, "local/base:latest"); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs, []string{"local/base:latest"}) {
		t.Fatal(refs)
	}
}

func TestImageBatchRequiresCompleteOrderedIdentities(t *testing.T) {
	refs := []string{"local/a:one", "local/b:two"}
	images := []ImageDetails{
		{ID: "sha256:" + strings.Repeat("a", 64), RepoTags: []string{"docker.io/local/a:one"}},
		{ID: "sha256:" + strings.Repeat("b", 64), RepoTags: []string{"docker.io/local/b:two"}},
	}
	for _, test := range []struct {
		name    string
		entries []ImageDetails
		valid   bool
	}{
		{"complete", images, true},
		{"missing", images[:1], false},
		{"extra", append(append([]ImageDetails{}, images...), images[0]), false},
		{"reordered", []ImageDetails{images[1], images[0]}, false},
		{"duplicate", []ImageDetails{images[0], images[0]}, false},
		{"no identity", []ImageDetails{{RepoTags: images[0].RepoTags}, images[1]}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.entries)
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseImageInspects(raw, refs)
			if (err == nil) != test.valid || (err != nil && len(got) != 0) {
				t.Fatalf("entries=%v error=%v", got, err)
			}
		})
	}
	if _, err := parseImageInspects([]byte(`[{`), refs); err == nil {
		t.Fatal("accepted truncated inspection")
	}
}

func TestSnapshotIdentityRequiresKnownMetadata(t *testing.T) {
	key, err := snapshotKey([]byte(`[{"Snapshotter":"overlayfs","SnapshotKey":"box-id"}]`))
	if err != nil || key != "box-id" {
		t.Fatalf("%q %v", key, err)
	}
	for _, raw := range []string{`[]`, `{}`, `[{"Snapshotter":"native","SnapshotKey":"box-id"}]`, `[{"Snapshotter":"overlayfs"}]`} {
		if _, err := snapshotKey([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, kind := range []string{"Active", "View"} {
		parent, err := snapshotParent([]byte(`{"Name":"box-id","Kind":"`+kind+`","Parent":"sha256:parent"}`), key)
		if err != nil || parent != "sha256:parent" {
			t.Fatalf("%q %v", parent, err)
		}
	}
	for _, raw := range []string{`{}`, `{"Name":"different","Kind":"Active"}`, `{"Name":"box-id","Kind":"Committed"}`} {
		if _, err := snapshotParent([]byte(raw), key); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	parent, err := snapshotParent([]byte(`{"Name":"box-id","Kind":"Active"}`), key)
	if err != nil || parent != "" {
		t.Fatalf("scratch snapshot: %q %v", parent, err)
	}
}

// Sizes are read from the right, so a transient unpack key with a space
// keeps its name; anything malformed or duplicated is refused whole.
func TestSnapshotUsagesParse(t *testing.T) {
	raw := "KEY                                   SIZE       INODES \n" +
		"sha256:aaa                            1048576    40     \n" +
		"0123abcd                              9500000000 120000 \n" +
		"extract-123 sha256:bbb                4096       1      \n" +
		"tw2r8p0k4c5y                          0          0      \n"
	got, err := parseSnapshotUsages([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]uint64{"sha256:aaa": 1048576, "0123abcd": 9500000000, "extract-123 sha256:bbb": 4096, "tw2r8p0k4c5y": 0}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{
		"",
		"KEY SIZE\n",
		"KEY SIZE INODES\nsha256:aaa 1.0 GiB 40\n",
		"KEY SIZE INODES\nsha256:aaa 10\n",
		"KEY SIZE INODES\nsha256:aaa 10 x\n",
		"KEY SIZE INODES\nsha256:aaa 10 1\nsha256:aaa 11 1\n",
	} {
		if _, err := parseSnapshotUsages([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestContainerSnapshotsParse(t *testing.T) {
	out := map[string]string{}
	raw := `[{"ID":"c1","Snapshotter":"overlayfs","SnapshotKey":"c1"},{"ID":"c2","Snapshotter":"native","SnapshotKey":"c2"},{"ID":"c3","Snapshotter":"overlayfs","SnapshotKey":""}]`
	if err := parseContainerSnapshots([]byte(raw), out); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(out, map[string]string{"c1": "c1"}) {
		t.Fatalf("got %v", out)
	}
	if err := parseContainerSnapshots([]byte("{"), out); err == nil {
		t.Fatal("malformed inspection accepted")
	}
}

// The live layout: ps prints a 12-character prefix of the full ID that
// native inspect reports. An ambiguous or too short prefix matches nothing.
func TestSnapshotForMatchesTruncatedIDs(t *testing.T) {
	a, b := "abcdef012345"+strings.Repeat("0", 52), "abcdef012399"+strings.Repeat("1", 52)
	keys := map[string]string{a: "ka", b: "kb"}
	for id, want := range map[string]string{a: "ka", a[:12]: "ka", b[:12]: "kb", a[:13]: "ka"} {
		if got, ok := SnapshotFor(keys, id); !ok || got != want {
			t.Errorf("%s = %q %v, want %q", id, got, ok, want)
		}
	}
	for _, id := range []string{"abcdef0123", "ffffffffffff", ""} {
		if got, ok := SnapshotFor(keys, id); ok {
			t.Errorf("%q matched %q", id, got)
		}
	}
	keys["abcdef012345"+strings.Repeat("9", 52)] = "kc"
	if got, ok := SnapshotFor(keys, a[:12]); ok {
		t.Errorf("ambiguous prefix matched %q", got)
	}
}
