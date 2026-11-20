package features

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAgentStateParentsImageStep(t *testing.T) {
	feature := AgentStateParentsRuntime("/home/sandbox/data space/$literal/leaf")
	dockerfile, installing, hasInstall, err := InstallPlan("custom/base", []string{feature})
	if err != nil || !hasInstall || !slices.Equal(installing, []string{feature}) {
		t.Fatalf("install plan = %v, %v, %v", installing, hasInstall, err)
	}
	argv := stateParentsArgs(t, dockerfile)
	want := []string{"/home/sandbox", "/home/sandbox/data space", "/home/sandbox/data space/$literal"}
	if len(argv) < 4 || !slices.Equal(argv[4:], want) {
		t.Fatalf("parent paths = %v, want %v", argv, want)
	}
	for _, symlink := range []bool{false, true} {
		root := t.TempDir()
		args := slices.Clone(argv)
		for i := 4; i < len(args); i++ {
			args[i] = filepath.Join(root, strings.TrimPrefix(args[i], "/"))
		}
		parent := args[len(args)-1]
		if symlink {
			target := filepath.Join(root, "elsewhere")
			if err := os.MkdirAll(args[4], 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, args[5]); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("parent setup: %v: %s", err, out)
		}
		if info, err := os.Stat(parent); symlink {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("setup followed symlink: %v", err)
			}
		} else if err != nil || !info.IsDir() {
			t.Fatalf("parent was not created: %v", err)
		}
		if _, err := os.Stat(filepath.Join(parent, "leaf")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("setup touched the state leaf: %v", err)
		}
	}
}

func stateParentsArgs(t *testing.T, dockerfile string) []string {
	t.Helper()
	_, run, found := strings.Cut("\n"+dockerfile, "\nUSER root\nRUN ")
	if !found {
		t.Fatalf("missing root setup step in %s", dockerfile)
	}
	var argv []string
	if err := json.Unmarshal([]byte(run), &argv); err != nil {
		t.Fatal(err)
	}
	return argv
}

func TestOwnedAgentStateParentsChownsOnlyNewParents(t *testing.T) {
	feature := AgentStateParentsRuntime("/home/sandbox/existing/new/nested/leaf")
	dockerfile, err := OwnedAgentStateParentsDockerfile(feature, 501, 20)
	if err != nil {
		t.Fatal(err)
	}
	argv := stateParentsArgs(t, dockerfile)
	root := t.TempDir()
	for i := 4; i < len(argv); i++ {
		argv[i] = filepath.Join(root, strings.TrimPrefix(argv[i], "/"))
	}
	home := filepath.Join(root, "home/sandbox")
	existing := filepath.Join(home, "existing")
	sibling := filepath.Join(home, "helper-owned")
	for _, dir := range []string{existing, sibling} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(root, "chown.log")
	t.Setenv("CHOWN_LOG", log)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(root, "chown"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$CHOWN_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("owned parent setup: %v: %s", err, out)
	}
	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	newParent := filepath.Join(existing, "new")
	want := []string{"-h", "--", "501:20", newParent, "-h", "--", "501:20", filepath.Join(newParent, "nested")}
	if got := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n"); !slices.Equal(got, want) {
		t.Fatalf("chown arguments = %q, want %q", got, want)
	}
}
