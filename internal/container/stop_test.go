package container

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStopBoundsRuntimeGraceAndReportsFailure(t *testing.T) {
	for _, status := range []string{"0", "7"} {
		t.Run(status, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("LIMA_HOME", t.TempDir())
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			t.Setenv("STOP_TEST_CALLS", log)
			t.Setenv("STOP_TEST_STATUS", status)
			id := strings.Repeat("d", 64)
			t.Setenv("STOP_TEST_CID", id)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$STOP_TEST_CALLS"
case "$*" in
  *"nerdctl inspect probe"*) printf '[{"Id":"%s","Name":"probe"}]\n' "$STOP_TEST_CID" ;;
  *"nerdctl stop --time 30 $STOP_TEST_CID"*) exit "$STOP_TEST_STATUS" ;;
  *) exit 37 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			c := New("trial")
			c.stdout, c.stderr = io.Discard, io.Discard
			if err := c.Stop(t.Context(), "probe"); (err == nil) != (status == "0") {
				t.Fatalf("status %s: %v", status, err)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(raw)
			if !strings.Contains(calls, "nerdctl stop --time 30 "+id) {
				t.Fatal("lost bounded stop fallback", calls)
			}
			if strings.Count(calls, "nerdctl inspect probe\n") != 1 || strings.Count(calls, "\n") != 2 {
				t.Fatal("runtime stop should not issue obsolete ctr --all signals", calls)
			}
		})
	}
}
