package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

// TestParsePortFlags covers the --port flag parsing shared by box creation/copy/settings:
// "published:target" (Docker host:container order) and a bare "target"
// (then published == target). host_ip/protocol are left at their manifest
// defaults (omitted), surfaced via the Port resolvers.
func TestParsePortFlags(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []agentbox.Port
		wantErr bool
	}{
		{"nil", nil, nil, false},
		{"empty", []string{}, nil, false},
		{
			name: "published_target",
			in:   []string{"8080:3000"},
			want: []agentbox.Port{{Published: 8080, Target: 3000}},
		},
		{
			name: "bare_target",
			in:   []string{"3000"},
			want: []agentbox.Port{{Published: 3000, Target: 3000}},
		},
		{
			name: "multiple",
			in:   []string{"8080:3000", "5173"},
			want: []agentbox.Port{{Published: 8080, Target: 3000}, {Published: 5173, Target: 5173}},
		},
		{"bad_published", []string{"abc:3000"}, nil, true},
		{"bad_target", []string{"8080:xyz"}, nil, true},
		{"out_of_range", []string{"0:3000"}, nil, true},
		{"empty_spec", []string{""}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePortFlags(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePortFlags(%v) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePortFlags(%v) error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePortFlags(%v) = %+v, want %+v", tt.in, got, tt.want)
			}
			// Parsed ports carry no explicit host_ip/protocol; they resolve to
			// the documented defaults.
			for _, p := range got {
				if p.HostIP != "" || p.Protocol != "" {
					t.Errorf("parsed port should leave host_ip/protocol unset, got %+v", p)
				}
				if p.ResolvedHostIP() != agentbox.DefaultPortHostIP || p.ResolvedProtocol() != agentbox.DefaultPortProtocol {
					t.Errorf("resolved defaults mismatch for %+v", p)
				}
			}
		})
	}
}

// TestParseMountFlags covers the --mount flag parsing shared by box creation/copy/settings:
// "source:target[:ro]" with a normalized host source + absolute
// target, the optional ":ro" suffix → ReadOnly, and malformed-spec rejection.
func TestParseMountFlags(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := []struct {
		name    string
		in      []string
		want    []agentbox.Mount
		wantErr bool
	}{
		{"nil", nil, nil, false},
		{"empty", []string{}, nil, false},
		{
			name: "source_target",
			in:   []string{"/h/src:/c/dst"},
			want: []agentbox.Mount{{Source: "/h/src", Target: "/c/dst"}},
		},
		{
			name: "read_only_suffix",
			in:   []string{"/h/src:/c/dst:ro"},
			want: []agentbox.Mount{{Source: "/h/src", Target: "/c/dst", ReadOnly: true}},
		},
		{
			name: "multiple",
			in:   []string{"/h/a:/c/a", "/h/b:/c/b:ro"},
			want: []agentbox.Mount{
				{Source: "/h/a", Target: "/c/a"},
				{Source: "/h/b", Target: "/c/b", ReadOnly: true},
			},
		},
		{"relative_source", []string{"rel:/c/dst"}, []agentbox.Mount{{Source: filepath.Join(cwd, "rel"), Target: "/c/dst"}}, false},
		{"tilde_source", []string{"~/auth.json:/c/dst:ro"}, []agentbox.Mount{{Source: filepath.Join(home, "auth.json"), Target: "/c/dst", ReadOnly: true}}, false},
		{"relative_target", []string{"/h/src:rel"}, nil, true},
		{"missing_target", []string{"/h/src"}, nil, true},
		{"bad_mode", []string{"/h/src:/c/dst:rw"}, nil, true},
		{"too_many_parts", []string{"/h/src:/c/dst:ro:extra"}, nil, true},
		{"empty_spec", []string{""}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMountFlags(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMountFlags(%v) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMountFlags(%v) error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseMountFlags(%v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseEnvFlags covers the --env flag parsing shared by box creation/copy/settings:
// "KEY=VALUE" split on the FIRST '=' (so the value may contain '='),
// stored as a LITERAL EnvVar; a missing '=' or empty KEY is an error. Per the
// escape-hatch contract, a secret-LOOKING key is NOT rejected.
func TestParseEnvFlags(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    map[string]agentbox.EnvVar
		wantErr bool
	}{
		{"nil", nil, nil, false},
		{"empty", []string{}, nil, false},
		{
			name: "simple",
			in:   []string{"FOO=bar"},
			want: map[string]agentbox.EnvVar{"FOO": {Literal: "bar"}},
		},
		{
			name: "value_with_equals",
			in:   []string{"X=a=b"},
			want: map[string]agentbox.EnvVar{"X": {Literal: "a=b"}},
		},
		{
			name: "empty_value_ok",
			in:   []string{"EMPTY="},
			want: map[string]agentbox.EnvVar{"EMPTY": {Literal: ""}},
		},
		{
			// Escape hatch: a secret-looking KEY is accepted as a literal (not
			// rejected). Secrets belong on the account, but --env is the
			// non-secret-by-convention literal path and never second-guesses.
			name: "secret_looking_key_allowed",
			in:   []string{"ANTHROPIC_API_KEY=sk-literal"},
			want: map[string]agentbox.EnvVar{"ANTHROPIC_API_KEY": {Literal: "sk-literal"}},
		},
		{
			name: "multiple",
			in:   []string{"A=1", "B=2"},
			want: map[string]agentbox.EnvVar{"A": {Literal: "1"}, "B": {Literal: "2"}},
		},
		{"missing_equals", []string{"NOVALUE"}, nil, true},
		{"empty_key", []string{"=value"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnvFlags(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseEnvFlags(%v) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEnvFlags(%v) error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseEnvFlags(%v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}
