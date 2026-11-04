package termio

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestTerminalIdentityPreservesHostPreferencesWithoutShellEvaluation(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("TERM_PROGRAM_VERSION", "1.3.0")
	id := CurrentTerminalIdentity()
	argv := id.Command([]string{"sh", "-c", `printf '%s\n%s' "$TERM_PROGRAM" "$TERM_PROGRAM_VERSION"`})
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil || string(out) != "ghostty\n1.3.0" {
		t.Fatalf("terminal identity lost: %q, %v", out, err)
	}
	id.Version = "$(exit 37); 'literal'"
	argv = id.Command([]string{"sh", "-c", `printf '%s' "$TERM_PROGRAM_VERSION"`})
	out, err = exec.Command(argv[0], argv[1:]...).Output()
	if err != nil || string(out) != id.Version {
		t.Fatalf("version interpreted as shell text: %q, %v", out, err)
	}
}

func TestTerminalIdentityBoundsAndUnknownHost(t *testing.T) {
	for _, id := range []TerminalIdentity{
		{Version: "1.3.0"}, {Program: "ghostty\n"}, {Program: "ghostty", Version: "1\x00"},
		{Program: strings.Repeat("x", 129)}, {Program: "ghostty", Version: "\x1b[2J"},
	} {
		if id.Valid() {
			t.Fatalf("accepted invalid identity: %+v", id)
		}
		if strings.ContainsRune(id.Version, 0) {
			continue
		}
		t.Setenv("TERM_PROGRAM", id.Program)
		t.Setenv("TERM_PROGRAM_VERSION", id.Version)
		if got := CurrentTerminalIdentity(); got != (TerminalIdentity{}) {
			t.Fatalf("invalid host identity forwarded: %+v", got)
		}
	}
	argv := []string{"agent", "--help"}
	if got := (TerminalIdentity{}).Command(argv); !reflect.DeepEqual(got, argv) {
		t.Fatalf("unknown host invented terminal metadata: %q", got)
	}
}
