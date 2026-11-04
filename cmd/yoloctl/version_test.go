package main

import (
	"strings"
	"testing"
)

// extractLimactlVersion mirrors limactlVersion's third-token rule.
func extractLimactlVersion(out string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 {
		return "", false
	}
	return fields[2], true
}

func TestLimactlVersionParse(t *testing.T) {
	cases := []struct {
		out, want string
		ok        bool
	}{
		{"limactl version 1.0.4\n", "1.0.4", true},
		{"limactl version 218b564\n", "218b564", true},
		{"limactl version 1.2.3+build.42\n", "1.2.3+build.42", true},
		{"limactl\n", "", false},
	}
	for _, c := range cases {
		got, ok := extractLimactlVersion(c.out)
		if got != c.want || ok != c.ok {
			t.Errorf("input %q: got (%q,%v) want (%q,%v)", c.out, got, ok, c.want, c.ok)
		}
	}
}
