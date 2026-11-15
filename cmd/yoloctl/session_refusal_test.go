package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

// refusalEngine: the attach socket never appears, the unit is not active (its
// state probe fails), and `cat` of the refusal record prints the fixture.
func refusalEngine(t *testing.T, record string) (*container.Client, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(dir, "log")
	t.Setenv("REFUSAL_FIXTURE", filepath.Join(dir, "refused"))
	t.Setenv("REFUSAL_LOG", log)
	if record != "" {
		if err := os.WriteFile(filepath.Join(dir, "refused"), []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$REFUSAL_LOG\"\ncase \"$*\" in\n *'sudo cat '*/refused) cat \"$REFUSAL_FIXTURE\" ;;\n *'sudo rm -f '*/refused) exit 0 ;;\n *) exit 97 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return container.NewWithOutput("work", io.Discard, io.Discard), log
}

// Directive 61: a holder that refused because the image has no mkfifo makes
// the start fail with the message naming the missing tool; without that
// record (or with any other content) the start fails generically.
func TestHolderRefusalNamesTheMissingTool(t *testing.T) {
	c, log := refusalEngine(t, "reason: no-mkfifo\n")
	err := waitForAttachSocket(t.Context(), c, "coder")
	if err == nil || err.Error() != "box coder's image has no mkfifo; a session cannot be started in it" {
		t.Fatalf("err %v", err)
	}
	raw, _ := os.ReadFile(log)
	if !strings.Contains(string(raw), "sudo rm -f /var/lib/yoloctl/sessions/coder/refused") {
		t.Fatalf("the record was not removed:\n%s", raw)
	}
	for _, record := range []string{"", "reason: something-else\n", "reason: no-mkfifo\nextra: 1\n"} {
		c, _ := refusalEngine(t, record)
		if err := waitForAttachSocket(t.Context(), c, "coder"); err == nil || !strings.Contains(err.Error(), "exited during startup") {
			t.Fatalf("record %q: %v", record, err)
		}
	}
}
