package tui

import "testing"

func TestBoxUseCLIPlacement(t *testing.T) {
	for _, tc := range []struct {
		args []string
		tab  bool
	}{
		{[]string{"box", "use", "coder", "screen"}, false},
		{[]string{"box", "use", "coder", "text", "hello"}, false},
		{[]string{"box", "use", "coder", "text", "--stdin"}, true},
		{[]string{"box", "use", "coder", "batch", "--stdin"}, true},
		{[]string{"box", "use", "coder", "batch", "--stdin", "--stdin=false"}, false},
		{[]string{"box", "use", "coder", "batch", "--stdin=false", "--stdin=true"}, true},
		{[]string{"box", "use", "coder", "batch", "--stdin", "--help"}, false},
		{[]string{"box", "use", "coder", "batch", "--stdin=bad", "--stdin=true"}, false},
		{[]string{"box", "use", "coder", "text", "--", "--stdin"}, false},
	} {
		if got := opensLiveSurface(tc.args); got != tc.tab {
			t.Fatalf("%q: got %v want %v", tc.args, got, tc.tab)
		}
	}
}
