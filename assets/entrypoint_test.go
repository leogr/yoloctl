package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntrypointPreparedIdentitySkipsOwnershipMutations(t *testing.T) {
	shim, err := AgentboxContexts.ReadFile("containers/agentbox/yoloctl-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mock := `#!/bin/sh
set -eu
case "${0##*/}" in
id) printf '%s\n' "$YOLOCTL_HOST_UID";;
getent) printf 'sandbox:x:%s:\n' "$YOLOCTL_HOST_GID";;
*) echo "unexpected ownership mutation: $0 $*" >> "$TEST_CALLS"; exit 1;;
esac
`
	for _, name := range []string{"id", "getent", "usermod", "groupmod", "chown", "chgrp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(mock), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("YOLOCTL_HOST_UID", "501")
	t.Setenv("YOLOCTL_HOST_GID", "20")
	cmd := exec.Command("sh", "-c", string(shim), "shim", "printf", "%s", "supervisor started")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "supervisor started" {
		t.Fatalf("prepared identity repeated home remapping: %v: %s", err, out)
	}
	if calls, err := os.ReadFile(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatalf("prepared identity attempted ownership mutation: %s, %v", calls, err)
	}
}

// Run the embedded shell shim with a simulated user/group database. Mutation
// commands are replaced so this test cannot change accounts on the test host.
func TestEntrypointGroupCollisionAndRepeatedStart(t *testing.T) {
	shim, err := AgentboxContexts.ReadFile("containers/agentbox/yoloctl-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range []string{"20", "1000", "2000"} {
		t.Run(gid, func(t *testing.T) {
			dir := t.TempDir()
			for name, contents := range map[string]string{"shim": string(shim), "uid": "2000", "gid": "2000"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			mock := `#!/bin/sh
set -eu
case "${0##*/}" in
id) cat "$TEST_STATE/uid";;
getent) printf 'sandbox:x:%s:\n' "$(cat "$TEST_STATE/gid")";;
usermod) printf '%s' "$2" > "$TEST_STATE/uid";;
groupmod)
    nonunique=0
    if [ "$1" = -o ]; then nonunique=1; shift; fi
    [ "$1" = -g ] && [ "$3" = sandbox ]
    if [ "$2" = 20 ] && [ "$nonunique" = 0 ]; then
        echo "groupmod: GID '20' already exists" >&2
        exit 4
    fi
    printf '%s' "$2" > "$TEST_STATE/gid"
    echo changed >> "$TEST_STATE/changes";;
chown|chgrp) exit 0;;
esac
`
			for _, name := range []string{"id", "getent", "usermod", "groupmod", "chown", "chgrp"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(mock), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_STATE", dir)
			t.Setenv("YOLOCTL_HOST_UID", "501")
			t.Setenv("YOLOCTL_HOST_GID", gid)
			for range 2 {
				cmd := exec.Command("sh", filepath.Join(dir, "shim"), "printf", "%s", "supervisor started")
				out, err := cmd.CombinedOutput()
				if err != nil || string(out) != "supervisor started" {
					t.Fatalf("entrypoint: %v: %s", err, out)
				}
			}
			for name, want := range map[string]string{"uid": "501", "gid": gid} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q, %v; want %s", name, data, err, want)
				}
			}
			changes, _ := os.ReadFile(filepath.Join(dir, "changes"))
			want := 1
			if gid == "2000" {
				want = 0
			}
			if strings.Count(string(changes), "changed") != want {
				t.Fatalf("unexpected repeated remap: %s", changes)
			}
		})
	}
}

func TestEntrypointRejectsRootGroupBeforeMutation(t *testing.T) {
	shim, err := AgentboxContexts.ReadFile("containers/agentbox/yoloctl-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range []string{"0", "00"} {
		t.Run(gid, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"id", "getent", "usermod", "groupmod", "chown", "chgrp"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho invoked >> \"$TEST_CALLS\"\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_CALLS", filepath.Join(dir, "calls"))
			t.Setenv("YOLOCTL_HOST_UID", "501")
			t.Setenv("YOLOCTL_HOST_GID", gid)
			cmd := exec.Command("sh", "-c", string(shim), "shim", "echo", "supervisor started")
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "refusing to map sandbox") || strings.Contains(string(out), "supervisor started") {
				t.Fatalf("root group was not rejected: %v: %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
				t.Fatalf("identity commands ran before rejection: %v", err)
			}
		})
	}
}
