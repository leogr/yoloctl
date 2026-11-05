package spacearchive

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/layout"
)

func nativeTestFile(t *testing.T, leaf, name, content string) {
	t.Helper()
	p := filepath.Join(leaf, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func nativeTestLink(t *testing.T, leaf, name, target string) {
	t.Helper()
	p := filepath.Join(leaf, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

func nativeTestSocket(t *testing.T, dest string) {
	t.Helper()
	// Bind at a short path, then move the stale socket inode to the fixture;
	// Go's per-test directory can exceed the Unix socket address length limit.
	dir, err := os.MkdirTemp("", "ysa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(socket, dest); err != nil {
		t.Fatal(err)
	}
}

func TestExportCodexStateWithoutChangingSource(t *testing.T) {
	for _, agent := range []string{"codex", "custom-codex"} {
		t.Run(agent, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			profile, err := agentprofile.Lookup("codex")
			if err != nil {
				t.Fatal(err)
			}
			if agent != "codex" {
				profile.Name = agent
				profile.Command = "custom-launcher"
				profile.StateContainerPath = "/custom/codex"
				if err := profile.Save(); err != nil {
					t.Fatal(err)
				}
			}
			source, boot := fixture(t)
			leaf := filepath.Join(source, "account-state/trial", agent)
			files := map[string]string{
				"auth.json":                                      "synthetic credentials",
				"sessions/history.jsonl":                         "saved session",
				"app-server-daemon/settings.json":                "persistent settings",
				"app-server-daemon/daemon.stderr.log":            "diagnostic log",
				"app-server-daemon/other.pid":                    "not a recognized runtime file",
				"tmp/arg0-linux-keep/saved.txt":                  "not the exact scratch subtree",
				"tmp/keep.txt":                                   "not the arg0 scratch tree",
				"packages/app-server-daemon/releases/test/codex": "synthetic executable",
			}
			for name, data := range files {
				nativeTestFile(t, leaf, name, data)
			}
			alias, err := layout.AccountStatePath("trial", agent)
			if err != nil {
				t.Fatal(err)
			}
			links := map[string]string{
				"packages/app-server-daemon/current": profile.StateContainerPath + "/packages/app-server-daemon/releases/test",
				"disk-alias":                         leaf + "/auth.json",
				"native-alias":                       alias + "/auth.json",
				"nested/leaf-root":                   profile.StateContainerPath,
				"dangling":                           profile.StateContainerPath + "/future",
				"relative":                           "auth.json",
			}
			for name, target := range links {
				nativeTestLink(t, leaf, name, target)
			}
			runtimeLinks := map[string]string{
				"tmp/arg0-linux/codex-arg0QZimBM/apply_patch": "/home/sandbox/.npm-global/bin/codex",
				"tmp/arg0/codex-helper/apply_patch":           "/home/sandbox/.npm-global/bin/codex",
				"app-server-control/app-server-control.sock":  "/tmp/codex-daemon-1000/control",
			}
			for name, target := range runtimeLinks {
				nativeTestLink(t, leaf, name, target)
			}
			pids := []string{"daemon.pid", "daemon-updater.pid", "app-server.pid", "app-server-updater.pid"}
			for _, name := range pids {
				nativeTestFile(t, leaf, "app-server-daemon/"+name, "12345")
			}
			sockets := []string{"app-server-daemon/daemon-updater.sock", "arbitrary-socket-name"}
			for _, name := range sockets {
				nativeTestSocket(t, filepath.Join(leaf, name))
			}
			state := stateFile(t)
			if err := WriteState(t.Context(), state, source, boot); err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			if err := Write(t.Context(), &archive, *boot, state); err != nil {
				t.Fatal(err)
			}
			restored := stateFile(t)
			m, _, err := Read(t.Context(), &archive, restored)
			if err != nil {
				t.Fatal(err)
			}
			dest := t.TempDir()
			if err := os.Mkdir(filepath.Join(dest, "account-state"), 0700); err != nil {
				t.Fatal(err)
			}
			boot.UID, boot.GID = os.Getuid(), os.Getgid()
			if _, err := restored.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			if err := RestoreState(t.Context(), restored, dest, boot, m.StateSHA256, m.StateSize); err != nil {
				t.Fatal(err)
			}
			targetLeaf := filepath.Join(dest, "account-state/trial", agent)
			for name, want := range files {
				for _, root := range []string{leaf, targetLeaf} {
					if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(got) != want {
						t.Fatalf("%s: data changed: %q, %v", filepath.Join(root, name), got, err)
					}
				}
			}
			for name, want := range links {
				if got, err := os.Readlink(filepath.Join(leaf, name)); err != nil || got != want {
					t.Fatalf("source link %s changed: %q, %v", name, got, err)
				}
				if got, err := os.Readlink(filepath.Join(targetLeaf, name)); err != nil || filepath.IsAbs(got) {
					t.Fatalf("restored link %s is not relative: %q, %v", name, got, err)
				}
			}
			for _, name := range []string{"disk-alias", "native-alias", "relative", "nested/leaf-root/auth.json"} {
				if got, err := os.ReadFile(filepath.Join(targetLeaf, name)); err != nil || string(got) != files["auth.json"] {
					t.Fatalf("restored link %s does not resolve inside the new leaf: %q, %v", name, got, err)
				}
			}
			if got, err := os.ReadFile(filepath.Join(targetLeaf, "packages/app-server-daemon/current/codex")); err != nil || string(got) != "synthetic executable" {
				t.Fatalf("daemon release link broken: %q, %v", got, err)
			}
			for name, want := range runtimeLinks {
				if got, err := os.Readlink(filepath.Join(leaf, name)); err != nil || got != want {
					t.Fatalf("source runtime link %s changed: %q, %v", name, got, err)
				}
				if _, err := os.Lstat(filepath.Join(targetLeaf, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("runtime link %s survived restore: %v", name, err)
				}
			}
			for _, name := range pids {
				if got, err := os.ReadFile(filepath.Join(leaf, "app-server-daemon", name)); err != nil || string(got) != "12345" {
					t.Fatalf("source pid file %s changed: %q, %v", name, got, err)
				}
				if _, err := os.Lstat(filepath.Join(targetLeaf, "app-server-daemon", name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("runtime pid %s survived restore: %v", name, err)
				}
			}
			for _, name := range sockets {
				if info, err := os.Lstat(filepath.Join(leaf, name)); err != nil || info.Mode()&os.ModeSocket == 0 {
					t.Fatalf("source socket %s changed: %v", name, err)
				}
				if _, err := os.Lstat(filepath.Join(targetLeaf, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("socket %s survived restore: %v", name, err)
				}
			}
		})
	}
}

func TestExportRejectsUnsafeLinksWithEntryName(t *testing.T) {
	for _, target := range []string{
		"/home/sandbox/.codex/../.claude/auth.json",
		"/home/sandbox/.codex/symlink/../auth.json", // Do not silently clean through a link.
		"/home/sandbox/.codex/back\\slash", "../claude/history",
	} {
		t.Run(target, func(t *testing.T) {
			source, boot := fixture(t)
			leaf := filepath.Join(source, "account-state/trial/codex")
			nativeTestLink(t, leaf, "bad-link", target)
			err := WriteState(t.Context(), io.Discard, source, boot)
			if err == nil || !strings.Contains(err.Error(), `"accounts/trial/codex/bad-link"`) {
				t.Fatalf("unsafe export lacks offending entry: %v", err)
			}
		})
	}
}

func TestExportRuntimeNamesDoNotExcludeUnrelatedAgents(t *testing.T) {
	source, boot := fixture(t)
	leaf := filepath.Join(source, "account-state/trial/claude")
	names := []string{"tmp/arg0/file", "app-server-daemon/daemon.pid", "app-server-control/app-server-control.sock"}
	for _, name := range names {
		nativeTestFile(t, leaf, name, "keep me")
	}
	var data bytes.Buffer
	if err := WriteState(t.Context(), &data, source, boot); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	tr := tar.NewReader(&data)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[h.Name] = true
	}
	for _, name := range names {
		if !seen["accounts/trial/claude/"+name] {
			t.Fatalf("excluded unrelated agent data: %s", name)
		}
	}
}

func TestAdditionalAccountStateViewLinksRemainWithinTheSameLeaf(t *testing.T) {
	p := nativeExportPolicy{}
	for _, tc := range []struct{ entry, target, want string }{
		{"trial/claude/current-profile", "/home/sandbox/.config/anthropic/credentials/default.json", "anthropic/credentials/default.json"},
		{"trial/claude/anthropic/current", "/home/sandbox/.config/anthropic/credentials/default.json", "credentials/default.json"},
		{"trial/claude/anthropic/primary-alias", "/home/sandbox/.claude/anthropic/credentials/default.json", "credentials/default.json"},
		{"trial/claude/root-alias", "/home/sandbox/.config/anthropic", "anthropic"},
		{"trial/codex/no-cross-agent-alias", "/home/sandbox/.config/anthropic/credentials/default.json", "/home/sandbox/.config/anthropic/credentials/default.json"},
	} {
		got, err := p.relativeLink("/native/account-state", tc.entry, tc.target)
		if err != nil || got != tc.want {
			t.Fatalf("relative link=%q want=%q error=%v", got, tc.want, err)
		}
	}
}

func TestClaudeProfileTreeSurvivesWholeSpaceArchive(t *testing.T) {
	source, boot := fixture(t)
	leaf := filepath.Join(source, "account-state/trial/claude")
	files := map[string]string{
		".credentials.json":               `{"claudeAiOauth":{"accessToken":"synthetic-legacy"}}`,
		"anthropic/active_config":         "work\n",
		"anthropic/configs/work.json":     `{"authentication":{"type":"user_oauth"}}`,
		"anthropic/credentials/work.json": `{"type":"oauth_token","access_token":"synthetic-private"}`,
	}
	for name, body := range files {
		nativeTestFile(t, leaf, name, body)
	}
	nativeTestLink(t, leaf, "active-credential", "/home/sandbox/.config/anthropic/credentials/work.json")
	state := stateFile(t)
	if err := WriteState(t.Context(), state, source, boot); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := Write(t.Context(), &archive, *boot, state); err != nil {
		t.Fatal(err)
	}
	restored := stateFile(t)
	manifest, _, err := Read(t.Context(), &archive, restored)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := os.Mkdir(filepath.Join(dest, "account-state"), 0700); err != nil {
		t.Fatal(err)
	}
	boot.UID, boot.GID = os.Getuid(), os.Getgid()
	if _, err := restored.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := RestoreState(t.Context(), restored, dest, boot, manifest.StateSHA256, manifest.StateSize); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dest, "account-state/trial/claude")
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || string(got) != want {
			t.Fatalf("archive changed %s", name)
		}
	}
	if got, err := os.ReadFile(filepath.Join(target, "active-credential")); err != nil || string(got) != files["anthropic/credentials/work.json"] {
		t.Fatal("restored additional-view alias is wrong", err)
	}
}
