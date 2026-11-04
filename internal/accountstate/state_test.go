package accountstate

import (
	"os/exec"
	"testing"
)

func TestGuestFilesystemSafety(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "python3", "testdata/test_guest.py", "guest.py")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guest filesystem tests: %v\n%s", err, out)
	}
}
