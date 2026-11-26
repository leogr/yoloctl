package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestCreateRefusesMissingPoolOrBuildGuardBeforeContainerCommand(t *testing.T) {
	for _, buildGuard := range []bool{false, true} {
		t.Run(fmt.Sprint(buildGuard), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("LIMA_HOME", t.TempDir())
			dir := t.TempDir()
			log := filepath.Join(dir, "commands.log")
			t.Setenv("YOLOCTL_TEST_CREATE_LOG", log)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			// ImagePresent intentionally distinguishes known absence from an
			// unavailable inspection. Name the absence so this branch actually
			// reaches the build guard; an unexplained exit1 must stop earlier.
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_TEST_CREATE_LOG"
case "$*" in
  *'nerdctl image inspect'*) printf 'no such image\n' >&2 ;;
esac
exit 1
`
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			guardError := errors.New("fixture build admission denied")
			if buildGuard {
				ctx = WithBuildGuard(ctx, func(_ context.Context, refs []string) error {
					if !slices.Equal(refs, []string{"unit-image"}) {
						t.Fatal("build guard did not receive selected image", refs)
					}
					return guardError
				})
			}
			client := New("fixture")
			if !buildGuard {
				fixtureCommandWatchdog(t, client, vmReadFailSafe)
			}
			_, err := client.Create(ctx, RunOptions{Name: "fixture", Image: "unit-image", WorkloadPool: true})
			if err == nil || buildGuard && !errors.Is(err, guardError) {
				t.Fatal("create bypassed existing guard", err)
			}
			raw, readErr := os.ReadFile(log)
			if readErr != nil || strings.Contains(string(raw), "nerdctl create") || strings.Contains(string(raw), "nerdctl run") || strings.Contains(string(raw), "--cgroup-manager") {
				t.Fatal("guard failure reached a container effect", string(raw), readErr)
			}
		})
	}
}

func TestCreateKeepsRunGuardsAndOptionsWithoutStarting(t *testing.T) {
	for _, create := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("create%v/failure%v", create, failed), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv("LIMA_HOME", t.TempDir())
				dir := t.TempDir()
				log := filepath.Join(dir, "commands.log")
				t.Setenv("YOLOCTL_TEST_CREATE_LOG", log)
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("YOLOCTL_TEST_CREATE_ID", strings.Repeat("a", 64))
				budget, _ := resources.Workloads(4 * resources.GiB)
				exit := 0
				if failed {
					exit = 23
				}
				script := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >> "$YOLOCTL_TEST_CREATE_LOG"
case "$*" in
  *'cat /sys/fs/cgroup/'*) printf '%d\nmax\n0\nMemTotal: 4194304 kB\nMemAvailable: 100 kB\n' ;;
  *'nerdctl --cgroup-manager systemd create --cgroup-parent yoloctl-workloads.slice'*|*'nerdctl --cgroup-manager systemd run --cgroup-parent yoloctl-workloads.slice'*) printf '%%s\n' "$YOLOCTL_TEST_CREATE_ID"; exit %d ;;
  *) exit 97 ;;
esac
`, budget.Max, exit)
				if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				client := New("fixture")
				fixtureCommandWatchdog(t, client, vmReadFailSafe)
				var stdout bytes.Buffer
				client.stdout, client.stderr = &stdout, io.Discard
				options := RunOptions{Name: "fixture", Image: "unit-image", WorkloadPool: true, Detach: true, User: "0", CPUs: "2", Memory: "2g",
					AddCaps: []string{"SYS_ADMIN"}, SeccompProfile: "unconfined", AppArmorProfile: "unconfined", Labels: map[string]string{"owner": "fixture"},
					Mounts: []Mount{{Source: "/unit/project", Destination: "/workdir", ReadOnly: true}}, Env: []string{"PUBLIC=visible"}, Command: []string{"/unit/helper", "fixed-role"}}
				var err error
				var id string
				action := "run"
				if create {
					action = "create"
					id, err = client.Create(t.Context(), options)
				} else {
					err = client.Run(t.Context(), options)
				}
				if failed {
					var failure *exec.ExitError
					if !errors.As(err, &failure) || failure.ExitCode() != 23 {
						t.Fatal("create/run result replaced", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if create && ((!failed && id != strings.Repeat("a", 64)) || (failed && id != "") || stdout.Len() != 0) {
					t.Fatal("create did not retain only its successful private acknowledgement", err)
				}
				// A detached run's stdout is only the new container's ID: not shown.
				if !create && stdout.Len() != 0 {
					t.Fatal("detached run echoed its container ID", stdout.String())
				}
				raw, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				calls := string(raw)
				if strings.Count(calls, "nerdctl ") != 1 || !strings.Contains(calls, "systemd "+action+" --cgroup-parent yoloctl-workloads.slice") || slices.Contains(strings.Fields(calls), "-d") == create || strings.Contains(calls, "nerdctl start") {
					t.Fatal("unstarted create launched/retried or Run changed", calls)
				}
				for _, want := range []string{"--name fixture", "--label owner=fixture", "--cap-add SYS_ADMIN", "--security-opt seccomp=unconfined", "--security-opt apparmor=unconfined", "--user 0", "--cpus 2", "--memory 2147483648", "-v /unit/project:/workdir:ro", "-e PUBLIC=visible", "unit-image /unit/helper fixed-role"} {
					if !strings.Contains(calls, want) {
						t.Fatal("shared create/run option lost", want, calls)
					}
				}
			})
		}
	}
}

func TestCreateAcknowledgementRefusesChatterOversizeAndFailedCommandDespiteValidID(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		exit         int
	}{
		{"valid", strings.Repeat("a", 64) + "\n", 0},
		{"empty", "", 0}, {"short", "abc\n", 0}, {"uppercase", strings.Repeat("A", 64), 0},
		{"two-identities", strings.Repeat("a", 64) + "\n" + strings.Repeat("b", 64), 0},
		{"chatter", "untrusted-output-value\n" + strings.Repeat("a", 64), 0},
		{"oversize", strings.Repeat(" ", 1024) + strings.Repeat("a", 64), 0},
		{"failed-with-id", strings.Repeat("a", 64) + "\n", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			t.Setenv("YOLOCTL_TEST_ACK_DIR", dir)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >> "$YOLOCTL_TEST_ACK_DIR/commands.log"
case "$*" in *'sudo nerdctl create --quiet '*) ;; *) exit 97;; esac
cat "$YOLOCTL_TEST_ACK_DIR/reply"
exit %d
`, tc.exit)
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "reply"), []byte(tc.output), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			id, err := NewWithOutput("fixture", &stdout, io.Discard).Create(t.Context(), RunOptions{Name: "fixture", Image: "unit-image"})
			if tc.name == "valid" {
				if err != nil || id != strings.Repeat("a", 64) {
					t.Fatal("valid create acknowledgement lost", err)
				}
			} else if err == nil || id != "" {
				t.Fatal("invalid acknowledgement became ownership", tc.name)
			}
			if tc.exit != 0 {
				var failure *exec.ExitError
				if !errors.As(err, &failure) || failure.ExitCode() != tc.exit {
					t.Fatal("command result suppressed by emitted ID", err)
				}
			}
			if stdout.Len() != 0 || err != nil && strings.Contains(err.Error(), "untrusted-output-value") {
				t.Fatal("untrusted acknowledgement output exposed")
			}
			calls, readErr := os.ReadFile(filepath.Join(dir, "commands.log"))
			if readErr != nil || strings.Count(string(calls), "sudo nerdctl create ") != 1 || strings.Contains(string(calls), "inspect") || strings.Contains(string(calls), " stop ") || strings.Contains(string(calls), " start ") {
				t.Fatal("acknowledgement failure triggered another action", readErr)
			}
		})
	}
}

func TestRunPrivateEnvironmentUsesOnlyKeyInNativeEngineArgs(t *testing.T) {
	const child = "YOLOCTL_TEST_PRIVATE_RUN_CHILD"
	const value = "synthetic private envelope"
	if os.Getenv(child) == "" {
		opts := RunOptions{Name: "fixture", Image: "fixture", PrivateEnv: "PRIVATE=" + value, Detach: true}
		if err := New("fixture").Run(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "native space controller") {
			t.Fatal("non-native engine accepted private input", err)
		}
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunPrivateEnvironmentUsesOnlyKeyInNativeEngineArgs$")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native engine fixture failed: %s", out)
		}
		return
	}
	err := spaceenv.Configure(spaceenv.Context{
		Schema: 1, Space: "fixture", DiskID: strings.Repeat("a", 32), DiskUUID: "uuid",
		DiskMount: "/mnt/native", ConfigDir: "/mnt/native/config", CatalogDir: spaceenv.CatalogRoot,
		HostConfigRoot: "/home/developer/.config/yoloctl", UID: 1000, GID: 1000, Bundle: "bundle", Generation: "generation",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_TEST_PRIVATE_ARGV", filepath.Join(dir, "argv"))
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$YOLOCTL_TEST_PRIVATE_ARGV"
[ "$PRIVATE" = 'synthetic private envelope' ] || exit 99
printf '%s\n' "$PRIVATE" >&2
exit 23
`
	if err := os.WriteFile(filepath.Join(dir, "nerdctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var out, warnings bytes.Buffer
	client := NewWithOutput("fixture", &out, &warnings)
	opts := RunOptions{Name: "fixture", Image: "fixture", PrivateEnv: "PRIVATE=" + value, Detach: true, Env: []string{"PUBLIC=visible", "PRIVATE=old"}}
	err = client.Run(t.Context(), opts)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || out.Len() != 0 || warnings.Len() != 0 {
		t.Fatal("private launch exposed engine output or lost its failure status")
	}
	args, err := os.ReadFile(os.Getenv("YOLOCTL_TEST_PRIVATE_ARGV"))
	if err != nil || !bytes.Contains(args, []byte("-e\nPRIVATE\n")) || !bytes.Contains(args, []byte("-e\nPUBLIC=visible\n")) || bytes.Contains(args, []byte("PRIVATE=")) || bytes.Contains(args, []byte(value)) {
		t.Fatal("private engine arguments were incorrect or contained values")
	}
}
