package session

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The agent argv reaches the sandbox as one shell command line: each
// argument, a carried model label with spaces and parentheses included,
// arrives whole and unevaluated.
func TestShellJoinKeepsEachArgumentWhole(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	argv := []string{"agy", "--conversation", "0f8fad5b-d9cb-469f-a165-70867728950e", "--model", "Gemini 3.8 Flash (High)", "it's $(id) `x` *"}
	out, err := exec.Command(sh, "-c", `printf '%s\n' `+shellJoin(argv[1:])).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); !slices.Equal(got, argv[1:]) {
		t.Fatalf("shell saw %q, want %q", got, argv[1:])
	}
}
