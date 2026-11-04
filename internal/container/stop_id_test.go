package container

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStopContainerIDKeepsForceAndActualAcknowledgement(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		for _, status := range []int{0, 23} {
			t.Run(strconv.FormatBool(immediate)+"/"+strconv.Itoa(status), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				dir := t.TempDir()
				log := filepath.Join(dir, "commands.log")
				t.Setenv("YOLOCTL_TEST_STOP_ID_LOG", log)
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$YOLOCTL_TEST_STOP_ID_LOG\"\n" +
					"printf '%s\\n' 'No such container: fixture' >&2\nexit " + strconv.Itoa(status) + "\n"
				if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				id := strings.Repeat("a", 64)
				err := NewWithOutput("fixture", io.Discard, io.Discard).StopContainerID(t.Context(), id, immediate)
				if status == 0 && err != nil {
					t.Fatal(err)
				}
				if status != 0 {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != status {
						t.Fatal("lost actual stop failure behind missing-container text", err)
					}
				}
				grace := "30"
				if immediate {
					grace = "0"
				}
				calls, readErr := os.ReadFile(log)
				if readErr != nil || string(calls) != "shell yolo-fixture -- sudo nerdctl stop --time "+grace+" "+id+"\n" {
					t.Fatal("stop changed force/CID or added lookup/retry/fallback", string(calls), readErr)
				}
			})
		}
	}
}

func TestStopContainerIDRefusesNamesPrefixesAndOptionsBeforeEffects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "commands.log")
	t.Setenv("YOLOCTL_TEST_STOP_ID_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$YOLOCTL_TEST_STOP_ID_LOG\"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, immediate := range []bool{false, true} {
		for _, id := range []string{"", "box-name", "--time", strings.Repeat("a", 12), strings.Repeat("A", 64), strings.Repeat("a", 63) + "\n", strings.Repeat("a", 64) + " other"} {
			if err := NewWithOutput("fixture", io.Discard, io.Discard).StopContainerID(t.Context(), id, immediate); err == nil {
				t.Fatal("noncanonical stop target accepted")
			}
		}
	}
	if _, err := os.Lstat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid stop target reached command execution", err)
	}
}
