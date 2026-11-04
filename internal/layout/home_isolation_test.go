package layout_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestTestBinaryDropsInheritedHome(t *testing.T) {
	// The package init ran before this test: an inherited override is gone.
	if value, ok := os.LookupEnv("YOLOCTL_HOME"); ok {
		t.Fatalf("test binary kept an inherited YOLOCTL_HOME=%q", value)
	}
	// An explicit test override still takes precedence.
	home := filepath.Join(t.TempDir(), "owned")
	t.Setenv("YOLOCTL_HOME", home)
	if got, err := layout.ConfigRoot(); err != nil || got != home {
		t.Fatal(got, err)
	}
}

// Owned sentinel regression (archyAstra R1): a developer exports a live home,
// then runs ordinary state-writing tests with a direct `go test`. The fixtures
// write their own XDG-isolated trees, and the exported home stays byte-identical.
func TestInheritedHomeSurvivesStateWritingTests(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test in a subprocess")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain unavailable")
	}
	sentinel := filepath.Join(t.TempDir(), "live-home")
	files := map[string]string{
		"spaces/default/space.yaml": "sentinel default bootstrap\n",
		"spaces/prod/space.yaml":    "sentinel prod bootstrap\n",
		"spaces/scratch/space.yaml": "sentinel scratch bootstrap\n",
		"features/sentinel.yaml":    "sentinel feature\n",
	}
	for name, data := range files {
		path := filepath.Join(sentinel, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotTree(t, sentinel)
	root, err := exec.Command(goTool, "env", "GOMOD").Output()
	if err != nil || strings.TrimSpace(string(root)) == "" {
		t.Skip("module root unavailable")
	}
	cmd := exec.Command(goTool, "test", "-count=1", "-run", "TestSaveLoadRoundTrip|TestList|User",
		"./internal/spaceconfig", "./internal/features")
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(root)))
	cmd.Env = append(os.Environ(), "YOLOCTL_HOME="+sentinel)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("state-writing tests failed: %v\n%s", err, out)
	}
	if after := snapshotTree(t, sentinel); !equalTrees(before, after) {
		t.Fatalf("state-writing tests changed the inherited home:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			tree[rel+"/"] = ""
			return nil
		}
		raw, err := os.ReadFile(path)
		tree[rel] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
