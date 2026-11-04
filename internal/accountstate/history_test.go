package accountstate

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// Exercise the live-smoke oracle in independent processes. Lifecycle and native
// mount preservation are tested by scripts/smoke/agent-state.sh on a Lima host.
func runHistoryProbe(t *testing.T, root, action string, count int) ([]byte, error) {
	t.Helper()
	return exec.CommandContext(t.Context(), "python3", "../../scripts/smoke/native-large-append.py",
		action, root, strconv.Itoa(count)).CombinedOutput()
}

func TestSyntheticHistoryResumesAcrossProcesses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "history")
	for _, step := range []struct {
		action string
		count  int
	}{{"init", 0}, {"verify", 8}, {"resume", 8}, {"verify", 16},
		{"resume", 16}, {"verify", 24}, {"resume", 24}, {"verify", 32}} {
		if out, err := runHistoryProbe(t, root, step.action, step.count); err != nil {
			t.Fatalf("%s %d: %v\n%s", step.action, step.count, err, out)
		}
	}
}

func TestSyntheticHistoryRejectsCorruptionBeforeResume(t *testing.T) {
	for _, damage := range []string{"zero-filled-range", "truncated-tail", "missing-turn", "duplicate-turn"} {
		t.Run(damage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "history")
			if out, err := runHistoryProbe(t, root, "init", 0); err != nil {
				t.Fatalf("init: %v\n%s", err, out)
			}
			path := filepath.Join(root, "synthetic-history.jsonl")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "zero-filled-range":
				clear(data[270000:280000])
			case "truncated-tail":
				data = data[:len(data)-137]
			case "missing-turn":
				data = data[bytes.IndexByte(data, '\n')+1:]
			case "duplicate-turn":
				data = append(data, data[:bytes.IndexByte(data, '\n')+1]...)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"verify", "resume"} {
				out, err := runHistoryProbe(t, root, action, 8)
				if err == nil || !bytes.Contains(out, []byte("history mismatch")) {
					t.Fatalf("%s accepted damage or failed for another reason: %v\n%s", action, err, out)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, data) {
				t.Fatal("failed resume modified damaged history")
			}
		})
	}
}
