package devcontainer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseForwardPorts(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []PortMapping
	}{
		{
			name: "ints",
			src:  `{"forwardPorts": [3000, 8080]}`,
			want: []PortMapping{{Published: 3000, Target: 3000}, {Published: 8080, Target: 8080}},
		},
		{
			name: "host_container_strings",
			src:  `{"forwardPorts": ["8080:80", "443:443"]}`,
			want: []PortMapping{{Published: 8080, Target: 80}, {Published: 443, Target: 443}},
		},
		{
			name: "mixed",
			src:  `{"forwardPorts": [3000, "8080:80"]}`,
			want: []PortMapping{{Published: 3000, Target: 3000}, {Published: 8080, Target: 80}},
		},
		{
			name: "absent",
			src:  `{"name": "test"}`,
			want: nil,
		},
		{
			name: "jsonc_line_comments_trailing_comma",
			src: `{
				// This is a comment
				"forwardPorts": [3000, 8080,],
				"name": "test", /* trailing block comment */
			}`,
			want: []PortMapping{{Published: 3000, Target: 3000}, {Published: 8080, Target: 8080}},
		},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".json")
			if err := os.WriteFile(path, []byte(tt.src), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Parse(path)
			if err != nil {
				t.Fatalf("Parse(%s) error: %v", tt.name, err)
			}
			if !reflect.DeepEqual(cfg.ForwardPorts, tt.want) {
				t.Errorf("ForwardPorts = %v, want %v", cfg.ForwardPorts, tt.want)
			}
		})
	}
}

func TestFindForProject(t *testing.T) {
	dir := t.TempDir()

	// No file -> nil cfg, nil err.
	cfg, err := FindForWorkdir(dir)
	if err != nil {
		t.Fatalf("FindForWorkdir(empty) error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("FindForWorkdir(empty) = %+v, want nil", cfg)
	}

	// Place a file at .devcontainer/devcontainer.json — the first
	// conventional path.
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o700); err != nil {
		t.Fatal(err)
	}
	src := `{"forwardPorts": [5173]}`
	if err := os.WriteFile(filepath.Join(dir, ".devcontainer", "devcontainer.json"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = FindForWorkdir(dir)
	if err != nil {
		t.Fatalf("FindForWorkdir(with file) error: %v", err)
	}
	if cfg == nil || len(cfg.ForwardPorts) != 1 || cfg.ForwardPorts[0].Published != 5173 || cfg.ForwardPorts[0].Target != 5173 {
		t.Fatalf("FindForWorkdir(with file) = %+v, want {published:5173,target:5173}", cfg)
	}
}

func TestParsePortEntryInvalid(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name string
		src  string
	}{
		{"object_entry", `{"forwardPorts": [{"port": 3000}]}`},
		{"invalid_string", `{"forwardPorts": ["abc"]}`},
		{"invalid_pair", `{"forwardPorts": ["3000:abc"]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".json")
			if err := os.WriteFile(path, []byte(tt.src), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(path); err == nil {
				t.Errorf("Parse(%s) succeeded, want error", tt.name)
			}
		})
	}
}
