package features

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinAptInstallersRetainDownloadsAndRefreshIndexes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"codex", "pi", "opencode", "cargo", "python-3.12", "yolo.agent.observe", "yolo.agent.observe-codex", "yolo.agent.observe-agy"} {
		t.Run(name, func(t *testing.T) {
			recipe, err := GenerateDockerfile("base", []string{name})
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{
				"target=/var/lib/apt/lists,sharing=locked",
				"target=/var/cache/yoloctl/apt,sharing=locked",
				"update --error-on=any", "-o Acquire::Retries=3",
				"-o Dir::Cache::Archives=/var/cache/yoloctl/apt", "-o APT::Keep-Downloaded-Packages=true",
			} {
				if !strings.Contains(recipe, part) {
					t.Fatalf("installer lacks %q", part)
				}
			}
			if strings.Contains(recipe, "rm -rf /var/lib/apt/lists") {
				t.Fatal("installer deletes the metadata cache")
			}
		})
	}
}

func TestAptIndexFailureStopsPackageInstallation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		dir := t.TempDir()
		mock := `#!/bin/sh
printf '%s\n' "$*" >> "$APT_CALLS"
case " $* " in *" update "*) if [ "$FAIL_UPDATE" = yes ]; then echo 'index unavailable' >&2; exit 100; fi;; esac
`
		if err := os.WriteFile(filepath.Join(dir, "apt-get"), []byte(mock), 0o755); err != nil {
			t.Fatal(err)
		}
		_, script, ok := strings.Cut(aptInstallDockerfile("ripgrep fd-find"), "\n    apt-get ")
		if !ok {
			t.Fatal("missing apt command")
		}
		cmd := exec.Command("sh", "-c", "apt-get "+script)
		flag := "no"
		if fail {
			flag = "yes"
		}
		cmd.Env = append(os.Environ(), "PATH="+dir, "APT_CALLS="+filepath.Join(dir, "calls"), "FAIL_UPDATE="+flag)
		out, err := cmd.CombinedOutput()
		calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if fail {
			if err == nil || !strings.Contains(string(out), "index unavailable") || strings.Contains(string(calls), "install") {
				t.Fatalf("failed refresh reached install: %v %s %s", err, out, calls)
			}
		} else if err != nil || !strings.Contains(string(calls), "install -y --no-install-recommends ripgrep fd-find") {
			t.Fatalf("successful refresh missed package installation: %v %s %s", err, out, calls)
		}
	}
}
