package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/tui/term"
)

// The actual trusted CLI-child topology, with setup stderr on its PTY and an
// unused private view channel: a setup failure never publishes a holder view.
func TestStandaloneSetupFailureRetainsActualChildDiagnostic(t *testing.T) {
	s, err := term.Start(term.Options{Argv: []string{"sh", "-c", "printf '\033[31mError: operator endpoint unavailable\033[0m\n' >&2; exit 2"}, TrustedView: true, Rows: 12, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	<-s.Done()
	got := attachedCommandResult(s)
	var status *exec.ExitError
	if !errors.As(got, &status) || status.ExitCode() != 2 || !strings.Contains(got.Error(), "Error: operator endpoint unavailable") {
		t.Fatalf("setup cause or original child status lost: %v", got)
	}
	if strings.ContainsAny(got.Error(), "\x1b\r") || strings.Contains(got.Error(), "Press Enter") || strings.Contains(got.Error(), "✗ exit") {
		t.Fatalf("terminal controls or frontend banner escaped: %q", got)
	}
	if s.HolderView() {
		t.Fatal("PTY diagnostic became a holder view")
	}
}

func TestStandaloneSetupSuccessAndEmptyFailure(t *testing.T) {
	for _, script := range []string{"printf success; exit 0", "exit 7"} {
		s, err := term.Start(term.Options{Argv: []string{"sh", "-c", script}, TrustedView: true, Rows: 12, Cols: 80})
		if err != nil {
			t.Fatal(err)
		}
		<-s.Done()
		got := attachedCommandResult(s)
		s.Close()
		if strings.Contains(script, "exit 0") {
			if got != nil {
				t.Fatal("successful setup became failure", got)
			}
		} else {
			var status *exec.ExitError
			if !errors.As(got, &status) || status.ExitCode() != 7 || got.Error() != "exit status 7" {
				t.Fatalf("empty failure changed: %v", got)
			}
		}
	}
}

func TestStandaloneSetupOutputIsBoundedPlainTail(t *testing.T) {
	text := attachedSetupOutput(strings.Repeat("x", 8000) + "\nError: last cause\x1b\a\n✗ exit 2\nPress Enter to close this tab\n")
	if len(text) > attachedSetupOutputLimit || !strings.HasSuffix(text, "Error: last cause") || strings.ContainsAny(text, "\x1b\a") {
		t.Fatalf("setup output was not bounded plain tail: len=%d", len(text))
	}
}

// The re-attach hint extends the close line; the setup tail drops it too.
func TestStandaloneSetupOutputDropsHintedBanner(t *testing.T) {
	text := attachedSetupOutput("Error: cause\n✗ exit 137\nPress Enter to close this tab · r to re-attach to api\n")
	if text != "Error: cause" {
		t.Fatalf("setup output kept the banner: %q", text)
	}
}
