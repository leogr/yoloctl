package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err == nil || err.Error() != "box coder's image has no mkfifo (install coreutils in the image); a session cannot be started in it" {
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

func TestHolderStartupCancellationDuringProbe(t *testing.T) {
	for _, target := range []string{"socket", "unit"} {
		t.Run(target, func(t *testing.T) {
			c, log := refusalEngine(t, "")
			dir := filepath.Dir(log)
			t.Setenv("STALLED_HOLDER_PROBE", target)
			script := `#!/bin/sh
case "$*" in
 *'sudo test -S '*) stage=socket ;;
 *'systemctl is-active '*) stage=unit ;;
 *) printf '%s\n' unexpected >> "$REFUSAL_LOG"; exit 1 ;;
esac
printf '%s\n' "$stage" >> "$REFUSAL_LOG"
if [ "$stage" = "$STALLED_HOLDER_PROBE" ]; then exec sleep 10; fi
exit 1
`
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- waitForAttachSocket(ctx, c, "coder") }()
			probe := time.NewTicker(5 * time.Millisecond)
			defer probe.Stop()
			wait := time.NewTimer(2 * time.Second)
			defer wait.Stop()
			for {
				if raw, _ := os.ReadFile(log); strings.Contains(string(raw), target+"\n") {
					break
				}
				select {
				case err := <-finished:
					t.Fatalf("startup returned before stalled %s: %v", target, err)
				case <-wait.C:
					t.Fatalf("startup did not enter stalled %s", target)
				case <-probe.C:
				}
			}
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("startup returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup did not cancel")
			}
			if raw, _ := os.ReadFile(log); strings.Contains(string(raw), "unexpected") {
				t.Fatalf("startup treated cancellation as a dead holder: %s", raw)
			}
		})
	}
}

func TestHolderOwnedStartupExpiryKeepsSocketDiagnostic(t *testing.T) {
	c, log := refusalEngine(t, "")
	script := `#!/bin/sh
case "$*" in
 *'sudo test -S '*) exit 1 ;;
 *'systemctl is-active '*) printf 'active\n' ;;
 *) printf 'unexpected\n' >> "$REFUSAL_LOG"; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(filepath.Dir(log), "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := ownedHolderStartupTimeout
	ownedHolderStartupTimeout = 80 * time.Millisecond
	t.Cleanup(func() { ownedHolderStartupTimeout = previous })
	owned := context.WithValue(t.Context(), controllerOwnedSetupKey{}, true)
	if err := waitForAttachSocket(owned, c, "coder"); err == nil || err.Error() != "session holder for coder did not open its attach socket within 80ms" {
		t.Fatalf("independent readiness timeout: %v", err)
	}
	ctx, cancel := context.WithTimeout(owned, 20*time.Millisecond)
	defer cancel()
	if err := waitForAttachSocket(ctx, c, "coder"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("earlier caller deadline: %v", err)
	}
	if raw, _ := os.ReadFile(log); len(raw) != 0 {
		t.Fatalf("unexpected probe after expiry: %s", raw)
	}
}
