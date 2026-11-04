package agentbox

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
)

const (
	withdrawNativeUnit = "yoloctl-openshell-port-web-8080-80.service"
	withdrawLegacyUnit = "yoloctl-port-web-8081-81.service"
	withdrawOtherUnit  = "yoloctl-openshell-port-other-9090-90.service"
)

// Reuse the finite Lima argv fixture, adding only deterministic collection and
// cancellation barriers. No VM, systemctl, nerdctl or namespace is exercised.
func nativeWithdrawalFixture(t *testing.T, units string) (string, *container.Client) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	raw, err := os.ReadFile("../port/testdata/limactl-forward-fixture.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), "set -eu\n", `set -eu
withdraw_wait() {
  : > "$YOLOCTL_TEST_FORWARD_DIR/$1-ready"
  waits=0
  while [ ! -f "$YOLOCTL_TEST_FORWARD_DIR/$1-continue" ]; do
    [ "$waits" -lt 500 ] || exit 96
    waits=$((waits + 1))
    sleep 0.01
  done
}
`, 1)
	script = strings.Replace(script, `cat "$YOLOCTL_TEST_FORWARD_DIR/units"`, `if [ "$count" = 2 ] && [ "${YOLOCTL_TEST_WITHDRAW_BARRIER:-}" = inventory ]; then
          withdraw_wait inventory
        fi
        if [ "$count" -lt 3 ]; then
          cat "$YOLOCTL_TEST_FORWARD_DIR/units"
        else
          cat "$YOLOCTL_TEST_FORWARD_DIR/remaining"
        fi`, 1)
	script = strings.Replace(script, `yoloctl-openshell-port-*) exit "${YOLOCTL_TEST_FORWARD_STOP_EXIT:-0}"`, `yoloctl-openshell-port-*)
            if [ "${YOLOCTL_TEST_WITHDRAW_BARRIER:-}" = stop ]; then withdraw_wait stop; fi
            exit "${YOLOCTL_TEST_FORWARD_STOP_EXIT:-0}"`, 1)
	for name, data := range map[string]string{
		"limactl": script, "units": units, "remaining": withdrawOtherUnit + " loaded active running peer\n", "commands.log": "",
	} {
		mode := os.FileMode(0o600)
		if name == "limactl" {
			mode = 0o700
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_TEST_FORWARD_DIR", dir)
	return dir, container.NewWithOutput("fixture", io.Discard, io.Discard)
}

func nativeWithdrawalCommands(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "commands.log"))
	if err != nil {
		t.Fatal(err)
	}
	commands := string(raw)
	if strings.Contains(commands, "is-active") || strings.Contains(commands, "nerdctl") || strings.Count(commands, "list-units") != strings.Count(commands, "list-units --all ") {
		t.Fatal("withdrawal used coarse state, a box effect or incomplete inventory", commands)
	}
	return commands
}

func TestWithdrawNativeForwardsWaitsForCollectionWithoutRepeatingStops(t *testing.T) {
	dir, client := nativeWithdrawalFixture(t, withdrawNativeUnit+" loaded inactive dead retained\n"+withdrawLegacyUnit+" loaded failed failed retained\n"+withdrawOtherUnit+" loaded active running peer\n")
	if err := WithdrawNativeForwards(t.Context(), client, "web"); err != nil {
		t.Fatal(err)
	}
	commands := nativeWithdrawalCommands(t, dir)
	if strings.Count(commands, "list-units --all ") != 3 || strings.Count(commands, "systemctl stop "+withdrawNativeUnit+"\n") != 1 || strings.Count(commands, "systemctl stop "+withdrawLegacyUnit+"\n") != 1 || strings.Contains(commands, "systemctl stop "+withdrawOtherUnit) {
		t.Fatal("delayed collection repeated a Stop or withdrew a peer", commands)
	}
	if strings.Count(commands, "systemctl reset-failed ") != 2 {
		t.Fatal("acknowledged stopped units were not collected", commands)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(commands), "\n") {
		if !strings.Contains(line, "reset-failed") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 5 || !strings.Contains(lines[0], "list-units --all ") || !strings.HasSuffix(lines[1], "systemctl stop "+withdrawNativeUnit) || !strings.HasSuffix(lines[2], "systemctl stop "+withdrawLegacyUnit) || !strings.Contains(lines[3], "list-units --all ") || !strings.Contains(lines[4], "list-units --all ") {
		t.Fatal("collection inventory did not follow both acknowledgements", commands)
	}
}

func TestWithdrawNativeForwardsLostAcknowledgementStopsEverySuffix(t *testing.T) {
	dir, client := nativeWithdrawalFixture(t, withdrawNativeUnit+" loaded failed failed retained\n"+withdrawLegacyUnit+" loaded inactive dead retained\n")
	t.Setenv("YOLOCTL_TEST_FORWARD_STOP_EXIT", "23")
	err := WithdrawNativeForwards(t.Context(), client, "web")
	// A failed stop reply with a live context is a plain error (old path);
	// only a lost reply would be uncertain. Nothing later runs either way.
	var uncertain *NativeEffectsUncertain
	var exit *exec.ExitError
	if errors.As(err, &uncertain) || !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatal("failed Stop reply misclassified", err)
	}
	commands := nativeWithdrawalCommands(t, dir)
	if strings.Count(commands, "systemctl stop ") != 1 || strings.Count(commands, "list-units --all ") != 1 {
		t.Fatal("lost acknowledgement reached another Stop or later state", commands)
	}
}

func TestWithdrawNativeForwardsOversizedInitialRosterCannotAuthorizeStop(t *testing.T) {
	dir, client := nativeWithdrawalFixture(t, strings.Repeat(withdrawNativeUnit+" loaded inactive dead retained\n", 20000))
	err := WithdrawNativeForwards(t.Context(), client, "web")
	commands := nativeWithdrawalCommands(t, dir)
	if err == nil || strings.Contains(commands, "systemctl stop ") || strings.Count(commands, "list-units --all ") != 1 {
		t.Fatal("oversized initial inventory authorized effects or became absence", err, commands)
	}
}

func TestWithdrawNativeForwardsPostAckInventoryFailureRemainsUncertain(t *testing.T) {
	dir, client := nativeWithdrawalFixture(t, withdrawNativeUnit+" loaded inactive dead retained\n")
	t.Setenv("YOLOCTL_TEST_FORWARD_LIST_FAIL_AT", "2")
	err := WithdrawNativeForwards(t.Context(), client, "web")
	// A failed read-only inventory after the acknowledged stops is a plain
	// error, never success and never uncertain.
	var uncertain *NativeEffectsUncertain
	var exit *exec.ExitError
	if errors.As(err, &uncertain) || !errors.As(err, &exit) || exit.ExitCode() != 24 {
		t.Fatal("unavailable inventory became success or uncertain", err)
	}
	commands := nativeWithdrawalCommands(t, dir)
	if strings.Count(commands, "systemctl stop ") != 1 || strings.Count(commands, "list-units --all ") != 2 {
		t.Fatal("post-ACK proof failure retried a mutation or observation", commands)
	}
}

func TestWithdrawNativeForwardsCancellationNeverReplaysStop(t *testing.T) {
	for _, barrier := range []string{"stop", "inventory"} {
		t.Run(barrier, func(t *testing.T) {
			dir, client := nativeWithdrawalFixture(t, withdrawNativeUnit+" loaded inactive dead retained\n")
			t.Setenv("YOLOCTL_TEST_WITHDRAW_BARRIER", barrier)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			coordinated := make(chan error, 1)
			go func() {
				wait, stop := context.WithTimeout(t.Context(), 3*time.Second)
				defer stop()
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(filepath.Join(dir, barrier+"-ready")); err == nil {
						cancel()
						if barrier == "inventory" {
							coordinated <- os.WriteFile(filepath.Join(dir, barrier+"-continue"), nil, 0o600)
						} else {
							coordinated <- nil
						}
						return
					}
					select {
					case <-wait.Done():
						coordinated <- wait.Err()
						return
					case <-ticker.C:
					}
				}
			}()
			err := WithdrawNativeForwards(ctx, client, "web")
			if waitErr := <-coordinated; waitErr != nil {
				t.Fatal("fixture did not reach cancellation barrier", waitErr, err)
			}
			var uncertain *NativeEffectsUncertain
			if barrier == "stop" {
				if !errors.As(err, &uncertain) {
					t.Fatal("cancelled Stop acknowledgement was treated as known", err)
				}
			} else if err != nil || ctx.Err() != context.Canceled {
				t.Fatal("cancelled caller prevented bounded post-ACK collection proof", err, ctx.Err())
			}
			commands := nativeWithdrawalCommands(t, dir)
			wantLists := 1
			if barrier == "inventory" {
				wantLists = 3
			}
			if strings.Count(commands, "systemctl stop ") != 1 || strings.Count(commands, "list-units --all ") != wantLists {
				t.Fatal("cancellation replayed Stop or crossed its acknowledgement boundary", commands)
			}
		})
	}
}

// The reported destroy bug: acknowledged stopped forward units that systemd
// keeps listed (a forwarder that exits non-zero on SIGTERM, started without
// --collect) end in one named warning, not an uncertain outcome.
func TestWithdrawNativeForwardsRetainedStoppedUnitsAreANotice(t *testing.T) {
	dir, client := nativeWithdrawalFixture(t, withdrawNativeUnit+" loaded failed failed retained\n")
	if err := os.WriteFile(filepath.Join(dir, "remaining"), []byte(withdrawNativeUnit+" loaded failed failed retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var notices []error
	old := nativeForwardNotice
	nativeForwardNotice = func(err error) { notices = append(notices, err) }
	defer func() { nativeForwardNotice = old }()
	err := WithdrawNativeForwards(t.Context(), client, "web")
	if err != nil || len(notices) != 1 || !strings.Contains(notices[0].Error(), withdrawNativeUnit) {
		t.Fatal("retained stopped forward unit was not a named warning", err, notices)
	}
	if strings.Count(nativeWithdrawalCommands(t, dir), "systemctl stop ") != 1 {
		t.Fatal("retained unit triggered another Stop")
	}
}
