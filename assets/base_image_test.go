package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseImagePythonRepositoryFailures(t *testing.T) {
	dockerfile, err := AgentboxContexts.ReadFile("containers/agentbox/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	_, step, ok := strings.Cut(string(dockerfile), "    add-apt-repository ")
	if !ok {
		t.Fatal("Python repository setup missing")
	}
	step, _, _ = strings.Cut(step, "\n\n")
	step = "add-apt-repository " + step
	for _, failure := range []string{"repository", "update", "install", ""} {
		t.Run("fail_"+failure, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/bin/sh
set -eu
case "${0##*/}" in
add-apt-repository)
    case " $* " in *" --no-update "*) ;; *) exit 2;; esac
    step=repository;;
apt-get)
    case " $* " in *" -o Acquire::Retries=3 "*) ;; *) exit 2;; esac
    case " $* " in
    *" update "*)
        case " $* " in *" --error-on=any "*) ;; *) exit 2;; esac
        step=update;;
    *" install "*)
        case " $* " in *" -o Dir::Cache::Archives=/var/cache/yoloctl/apt "*) ;; *) exit 2;; esac
        step=install;;
    *) exit 2;;
    esac;;
*) step=${0##*/};;
esac
printf '%s\n' "$step" >> "$TEST_CALLS"
if [ "$step" = "$FAIL_STEP" ]; then
    echo "$step: 503 Service Unavailable" >&2
    exit 100
fi
`
			for _, name := range []string{"add-apt-repository", "apt-get", "rm", "python3.13", "ln"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(mock), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// Every command is a fixture; this must never touch the host's apt
			// state or Python installation, including on macOS test runners.
			cmd := exec.Command("sh", "-c", step)
			cmd.Env = append(os.Environ(), "PATH="+dir, "TEST_CALLS="+filepath.Join(dir, "calls"), "FAIL_STEP="+failure, "PYTHON_VERSION=3.13")
			out, err := cmd.CombinedOutput()
			if failure == "" {
				if err != nil {
					t.Fatalf("successful setup: %v: %s", err, out)
				}
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 100 || !strings.Contains(string(out), "503 Service Unavailable") {
				t.Fatalf("repository failure lost: %v: %s", err, out)
			}
			want := "repository\nupdate\ninstall\nrm\npython3.13\nln\nln\n"
			if failure != "" {
				before, _, _ := strings.Cut(want, failure+"\n")
				want = before + failure + "\n"
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil || string(calls) != want {
				t.Fatalf("setup continued after failure or changed ordering: calls=%q, err=%v; want %q", calls, err, want)
			}
		})
	}
}
