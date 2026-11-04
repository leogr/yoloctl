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

func TestRemoveContainerIDKeepsExactResourceAndActualAcknowledgement(t *testing.T) {
	for _, status := range []int{0, 23} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			log := filepath.Join(dir, "commands.log")
			t.Setenv("YOLOCTL_TEST_REMOVE_ID_LOG", log)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_TEST_REMOVE_ID_LOG"
exit ` + strconv.Itoa(status) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 64)
			err := NewWithOutput("fixture", io.Discard, io.Discard).RemoveContainerID(t.Context(), id)
			if status == 0 && err != nil {
				t.Fatal(err)
			}
			if status != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != status {
					t.Fatal("lost actual removal failure", err)
				}
			}
			calls, readErr := os.ReadFile(log)
			if readErr != nil || string(calls) != "shell yolo-fixture -- sudo nerdctl rm "+id+"\n" {
				t.Fatal("removal used name/force/retry/probe or another resource", string(calls), readErr)
			}
		})
	}
}

func TestRemoveContainerIDRefusesNamesAliasesAndArgumentsBeforeEffects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "commands.log")
	t.Setenv("YOLOCTL_TEST_REMOVE_ID_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$YOLOCTL_TEST_REMOVE_ID_LOG\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "box-name", "--force", strings.Repeat("a", 12), strings.Repeat("A", 64), strings.Repeat("a", 63) + "\n", strings.Repeat("a", 64) + " other"} {
		if err := NewWithOutput("fixture", io.Discard, io.Discard).RemoveContainerID(t.Context(), id); err == nil {
			t.Fatal("noncanonical cleanup target accepted")
		}
	}
	if _, err := os.Lstat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid cleanup target reached command execution", err)
	}
}
