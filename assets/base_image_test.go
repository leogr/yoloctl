package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseImageRepositoriesFailBeforeInstallation(t *testing.T) {
	dockerfile, err := AgentboxContexts.ReadFile("containers/agentbox/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	// The existing bootstrap transaction installs repository prerequisites;
	// Node and Python registration must then share a single strict refresh.
	_, step, ok := strings.Cut(string(dockerfile), "    install -d -m 0755 /usr/share/keyrings")
	if !ok {
		t.Fatal("combined Node/Python repository setup missing")
	}
	step, _, _ = strings.Cut(step, "\n\n")
	step = "install -d -m 0755 /usr/share/keyrings" + step
	if strings.Count(string(dockerfile), "update --error-on=any") != 2 || strings.Contains(string(dockerfile), "setup_${NODE_MAJOR}") {
		t.Fatal("base must use two explicit updates, without the updating NodeSource bootstrap script")
	}
	for _, failure := range []string{"curl", "gpg", "repository", "update", "install", ""} {
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
        case " $* " in *" nodejs python3.13 python3.13-venv "*) ;; *) exit 2;; esac
        step=install;;
    *) exit 2;;
    esac;;
dpkg) printf 'arm64\n'; exit 0;;
install) step=directories;;
*) step=${0##*/};;
esac
printf '%s\n' "$step" >> "$TEST_CALLS"
if [ "$step" = "$FAIL_STEP" ]; then
    echo "$step: 503 Service Unavailable" >&2
    exit 100
fi
`
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"install", "curl", "gpg", "chmod", "dpkg", "add-apt-repository", "apt-get", "rm", "python3.13", "ln"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(mock), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// Redirect every write to our fixture. No host apt or Python command
			// can run: PATH contains only the fake executables above.
			fixture := strings.NewReplacer("/usr/share/keyrings", filepath.Join(dir, "keys"),
				"/etc/apt/sources.list.d", filepath.Join(dir, "sources"),
				"/etc/apt/preferences.d", filepath.Join(dir, "preferences"),
				"/tmp/yoloctl-nodesource.asc", filepath.Join(dir, "key.asc")).Replace(step)
			for _, name := range []string{"keys", "sources", "preferences"} {
				if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "-c", fixture)
			cmd.Env = append(os.Environ(), "PATH="+bin, "TEST_CALLS="+filepath.Join(dir, "calls"), "FAIL_STEP="+failure, "PYTHON_VERSION=3.13", "NODE_MAJOR=22")
			out, err := cmd.CombinedOutput()
			if failure == "" {
				if err != nil {
					t.Fatalf("successful setup: %v: %s", err, out)
				}
				source, err := os.ReadFile(filepath.Join(dir, "sources", "nodesource.sources"))
				if err != nil || !strings.Contains(string(source), "URIs: https://deb.nodesource.com/node_22.x\n") || !strings.Contains(string(source), "Architectures: arm64\nSigned-By: "+filepath.Join(dir, "keys", "nodesource.gpg")) {
					t.Fatalf("Node source lost its version, architecture or signing-key binding: %q (%v)", source, err)
				}
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 100 || !strings.Contains(string(out), "503 Service Unavailable") {
				t.Fatalf("repository failure lost: %v: %s", err, out)
			}
			want := "directories\ncurl\ngpg\nchmod\nrm\nrepository\nupdate\ninstall\npython3.13\nln\nln\n"
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
