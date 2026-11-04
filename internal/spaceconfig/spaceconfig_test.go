package spaceconfig

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001",
		Name:           "default",
		DefaultWorkdir: "/home/me/code",
		Mounts:         []Mount{{Source: "/home/me/code", Writable: true}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "default" || got.DefaultWorkdir != "/home/me/code" {
		t.Errorf("loaded %+v", got)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].Source != "/home/me/code" || !got.Mounts[0].Writable {
		t.Errorf("mounts = %+v", got.Mounts)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
}

func TestList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"prod", "default", "scratch"} {
		cfg := &Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: n, DefaultWorkdir: "/w", Mounts: []Mount{{Source: "/w", Writable: true}}}
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save(%s): %v", n, err)
		}
	}
	names, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"default", "prod", "scratch"}; !slices.Equal(names, want) {
		t.Errorf("List = %v, want sorted %v", names, want)
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	names, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List on empty registry = %v, want none", names)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"ok", Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: "default", DefaultWorkdir: "/w", Mounts: []Mount{{Source: "/w", Writable: true}}}, ""},
		{"bad name", Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: "../evil", DefaultWorkdir: "/w", Mounts: []Mount{{Source: "/w"}}}, "invalid space name"},
		{"no default_workdir", Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: "default", Mounts: []Mount{{Source: "/w"}}}, "default_workdir is required"},
		{"relative default_workdir", Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: "default", DefaultWorkdir: "rel"}, "must be an absolute path"},
		{"relative mount", Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: "default", DefaultWorkdir: "/w", Mounts: []Mount{{Source: "rel"}}}, "must be an absolute path"},
		{"duplicate mount", Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: "default", DefaultWorkdir: "/w", Mounts: []Mount{{Source: "/w"}, {Source: "/w"}}}, "duplicate mount source"},
		{"default not granted", Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Name: "default", DefaultWorkdir: "/home/me/code", Mounts: []Mount{{Source: "/other"}}}, "not under any mount grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestPathUnderGrant(t *testing.T) {
	cfg := &Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Mounts: []Mount{
		{Source: "/home/me/code", Writable: true},
		{Source: "/data", Writable: false},
	}}
	cases := []struct {
		path string
		want bool
	}{
		{"/home/me/code", true},               // exact
		{"/home/me/code/repo", true},          // nested
		{"/home/me/code/repo/pkg/x.go", true}, // deeper
		{"/home/me/codex", false},             // prefix but not a path-boundary child
		{"/home/me", false},                   // parent, not covered
		{"/data/sub", true},
		{"/other", false},
	}
	for _, tc := range cases {
		if got := cfg.PathUnderGrant(tc.path); got != tc.want {
			t.Errorf("PathUnderGrant(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestAddRemoveMount(t *testing.T) {
	cfg := &Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Mounts: []Mount{{Source: "/w", Writable: true}}}

	if !cfg.AddMount("/data", false) {
		t.Error("AddMount(/data) should report a change")
	}
	if cfg.AddMount("/data", false) {
		t.Error("re-adding the identical grant should be a no-op")
	}
	if !cfg.AddMount("/data", true) {
		t.Error("flipping writable should report a change")
	}
	if m, ok := cfg.GrantFor("/data/x"); !ok || !m.Writable {
		t.Errorf("GrantFor(/data/x) = %+v ok=%v, want writable grant", m, ok)
	}
	// Mounts stay sorted by source.
	if cfg.Mounts[0].Source != "/data" || cfg.Mounts[1].Source != "/w" {
		t.Errorf("mounts not sorted: %+v", cfg.Mounts)
	}

	if !cfg.RemoveMount("/data") {
		t.Error("RemoveMount(/data) should report removal")
	}
	if cfg.RemoveMount("/data") {
		t.Error("removing an absent grant should be a no-op")
	}
	if cfg.PathUnderGrant("/data/x") {
		t.Error("/data grant should be gone")
	}
}

func TestGrantForMostSpecific(t *testing.T) {
	cfg := &Config{DefaultAccount: "default", AccountDiskID: "00000000000000000000000000000001", Mounts: []Mount{
		{Source: "/data", Writable: false},   // read-only parent
		{Source: "/data/rw", Writable: true}, // writable child (more specific)
	}}
	// A path under both grants resolves to the MOST SPECIFIC (deepest) one —
	// the writable child, not the read-only parent it nests under.
	if m, ok := cfg.GrantFor("/data/rw/file"); !ok || !m.Writable || m.Source != "/data/rw" {
		t.Errorf("GrantFor(/data/rw/file) = %+v ok=%v, want the writable /data/rw grant", m, ok)
	}
	// A path under only the parent resolves to the parent.
	if m, ok := cfg.GrantFor("/data/ro/file"); !ok || m.Writable || m.Source != "/data" {
		t.Errorf("GrantFor(/data/ro/file) = %+v ok=%v, want the read-only /data grant", m, ok)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestOpenRejectsLegacyAndUnknownFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := New("fresh", "/work", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		strings.Replace(string(raw), "schema_version: 2", "schema_version: 1", 1),
		string(raw) + "global_account: personal\n",
		strings.Replace(string(raw), "default_account: personal", "default_account: none", 1),
		strings.Replace(string(raw), "name: fresh", "name: different", 1),
	} {
		if err := os.WriteFile(cfg.Path(), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load("fresh"); err == nil {
			t.Fatalf("invalid space accepted: %s", body)
		}
	}
}
