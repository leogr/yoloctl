package sandbox

import (
	"reflect"
	"testing"
)

func TestParseSandboxNetns(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []string
	}{
		{
			name: "empty output",
			out:  "",
			want: nil,
		},
		{
			name: "bare names",
			out:  "sandbox-9f3c2a1b\nsandbox-0aa1b2c3\n",
			want: []string{"sandbox-9f3c2a1b", "sandbox-0aa1b2c3"},
		},
		{
			name: "id annotations",
			out:  "sandbox-9f3c2a1b (id: 0)\nsandbox-0aa1b2c3 (id: 3)\n",
			want: []string{"sandbox-9f3c2a1b", "sandbox-0aa1b2c3"},
		},
		{
			name: "foreign netns filtered out",
			out:  "cni-4f2a91c0 (id: 1)\nsandbox-9f3c2a1b (id: 2)\ndefault\n",
			want: []string{"sandbox-9f3c2a1b"},
		},
		{
			name: "blank lines and stray whitespace",
			out:  "\n  \nsandbox-9f3c2a1b\n\n",
			want: []string{"sandbox-9f3c2a1b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSandboxNetns(tc.out)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseSandboxNetns(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}
