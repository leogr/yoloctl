package openshell

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The launcher shell follows upstream's order and needs an executable
// regular file; none is an error.
func TestLauncherShellOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	stat := func(path string) (os.FileInfo, error) { return os.Stat(filepath.Join(dir, path)) }
	if _, err := launcherShell(stat); err == nil {
		t.Fatal("no shell accepted")
	}
	write("bin/sh", 0o755)
	if got, _ := launcherShell(stat); !reflect.DeepEqual(got, LauncherShells[2]) {
		t.Fatal(got)
	}
	write("usr/bin/bash", 0o755)
	write("bin/bash", 0o644) // not executable: skipped
	if got, _ := launcherShell(stat); !reflect.DeepEqual(got, LauncherShells[1]) {
		t.Fatal(got)
	}
	_ = os.Chmod(filepath.Join(dir, "bin/bash"), 0o755)
	if got, _ := launcherShell(stat); !reflect.DeepEqual(got, LauncherShells[0]) {
		t.Fatal(got)
	}
}
