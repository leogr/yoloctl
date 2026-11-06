package agentbox

import (
	"os"
	"path/filepath"
	"testing"
)

// The CLI normalization regression supplies this one whole-leaf bind. Exercise
// the actual runtime mount builder after a concurrent workload replaces its
// Anthropic child; that child must never be consumed as a VM-side bind source.
func TestWholeAccountLeafBindSurvivesProfileChildReplacement(t *testing.T) {
	leaf := t.TempDir()
	child := filepath.Join(leaf, "anthropic")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	prepared, err := os.Stat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Name: "fixture", Mounts: []Mount{{Source: leaf, Target: "/home/sandbox/.claude"}}}
	if err := os.Rename(child, child+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), child); err != nil {
		t.Fatal(err)
	}
	accountBinds := 0
	for _, mount := range buildMounts(cfg) {
		if mount.Destination != "/home/sandbox/.claude" {
			if mount.Source == child {
				t.Fatal("runtime consumed the replaceable profile child")
			}
			continue
		}
		accountBinds++
		current, err := os.Stat(mount.Source)
		if err != nil || !os.SameFile(prepared, current) {
			t.Fatal("child replacement redirected the original account bind", err)
		}
	}
	if accountBinds != 1 {
		t.Fatal("missing original account bind")
	}
}
