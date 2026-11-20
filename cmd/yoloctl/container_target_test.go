package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/spf13/cobra"
)

func maintenanceTargetFixture(t *testing.T, raw string) (*container.Client, func() [][]string) {
	t.Helper()
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("YOLOCTL_TEST_MAINTENANCE_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/usr/bin/env python3
import json, os, pathlib, sys
p=pathlib.Path(os.environ['YOLOCTL_TEST_MAINTENANCE_DIR'])
args=sys.argv[1:]
args=args[args.index('--')+1:]
with (p/'calls').open('a') as f: f.write(json.dumps(args)+'\n')
if args[2]=='inspect':
 print((p/'inspect.json').read_text())
 sys.exit(int(os.environ.get('YOLOCTL_TEST_MAINTENANCE_INSPECT_EXIT','0')))
if args[2]=='exec':
 if len(args)>8 and args[8]=='cat': print('{"version":"1.2.3"}')
 if len(args)>8 and args[8]=='test': sys.exit(int(os.environ.get('YOLOCTL_TEST_MAINTENANCE_PROBE_EXIT','1')))
 sys.exit(int(os.environ.get('YOLOCTL_TEST_MAINTENANCE_EXEC_EXIT','0')))
sys.exit(97)
`
	for name, body := range map[string]string{"limactl": script, "inspect.json": raw} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return container.NewWithOutput("fixture", io.Discard, io.Discard), func() [][]string {
		raw, err := os.ReadFile(filepath.Join(dir, "calls"))
		if err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
			var args []string
			if err := json.Unmarshal([]byte(line), &args); err != nil {
				t.Fatal(err)
			}
			calls = append(calls, args)
		}
		return calls
	}
}

// Spell out the system directories independently from the production argv.
const wantAgentMaintenancePath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func TestMaintenanceExecsUseSystemPathAndExactlySelectedContainer(t *testing.T) {
	id := strings.Repeat("f", 64)
	valid := `[{"Id":"c1` + strings.Repeat("0", 62) + `","Name":"peer"},{"Id":"` + id + `","Name":"c1"}]`
	operations := map[string]func(*container.Client, string) error{
		"npm-install": func(c *container.Client, target string) error {
			return npmInstallGlobalAtLatest(t.Context(), c, target, "c1", "@vendor/agent")
		},
		"native-install": func(c *container.Client, target string) error {
			return installAgentLatest(t.Context(), c, target, "c1", &agentprofile.Profile{NativeRelease: "antigravity"})
		},
		"npm-version": func(c *container.Client, target string) error {
			_, err := fetchInstalledNpmVersion(t.Context(), c, target, "@vendor/agent")
			return err
		},
		"native-version": func(c *container.Client, target string) error {
			_, err := fetchInstalledAgentVersion(t.Context(), c, target, &agentprofile.Profile{NativeRelease: "antigravity"})
			return err
		},
		"process-probe": func(c *container.Client, target string) error {
			_, err := agentProcessRunning(t.Context(), c.Env(), target, "claude")
			return err
		},
	}
	for op, run := range operations {
		for _, state := range []string{"name", "id", "absent", "short-id", "duplicate", "inspect-error"} {
			t.Run(op+"/"+state, func(t *testing.T) {
				raw := valid
				switch state {
				case "absent":
					raw = "[]"
				case "short-id":
					raw = `[{"Id":"bad","Name":"c1"}]`
				case "duplicate":
					raw = strings.Replace(valid, `"Name":"peer"`, `"Name":"c1"`, 1)
				case "inspect-error":
					t.Setenv("YOLOCTL_TEST_MAINTENANCE_INSPECT_EXIT", "17")
				}
				c, calls := maintenanceTargetFixture(t, raw)
				target := "c1"
				if state == "id" {
					target = id
				}
				err := run(c, target)
				got := calls()
				if state != "name" && state != "id" {
					if err == nil || len(got) != 1 || !slices.Equal(got[0], []string{"sudo", "nerdctl", "inspect", "c1"}) {
						t.Fatal("unresolved maintenance target reached exec", got, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if state == "name" {
					if len(got) != 2 || !slices.Equal(got[0], []string{"sudo", "nerdctl", "inspect", "c1"}) {
						t.Fatal("maintenance lookup missing or repeated", got)
					}
					got = got[1:]
				}
				prefix := []string{"sudo", "nerdctl", "exec", "--user", "sandbox", "--env", wantAgentMaintenancePath, id}
				if op == "npm-install" {
					// Only the npm install runs as the agent maintenance user.
					prefix = []string{"sudo", "nerdctl", "exec", "--user", "yoloctl-maint", "--workdir", "/var/lib/yoloctl-maint",
						"--env", wantAgentMaintenancePath, "--env", "HOME=/var/lib/yoloctl-maint", "--env", "TMPDIR=/var/lib/yoloctl-maint", id}
				}
				if len(got) != 1 || len(got[0]) <= len(prefix) || !slices.Equal(got[0][:len(prefix)], prefix) {
					t.Fatal("maintenance exec changed its user, PATH or exact container ID", got)
				}
			})
		}
	}
}

func TestAgentMaintenanceRetainsOneTargetAcrossProbeAndInstall(t *testing.T) {
	id := strings.Repeat("f", 64)
	c, calls := maintenanceTargetFixture(t, `[{"Id":"`+id+`","Name":"c1"}]`)
	selected, err := c.ResolveContainerID(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if ok, err := preflightAgentRunning(cmd, c.Env(), "c1", selected, "claude"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := installAgentLatest(t.Context(), c, selected, "c1", &agentprofile.Profile{NpmPackage: "@vendor/agent"}); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 3 || got[0][2] != "inspect" || got[1][7] != id || got[2][13] != id {
		t.Fatal("probe/install reselected the name", got)
	}
}

func TestNpmMaintenanceFailureExplainsSystemPathAndAutoupdateOptOut(t *testing.T) {
	id := strings.Repeat("f", 64)
	c, _ := maintenanceTargetFixture(t, "[]")
	t.Setenv("YOLOCTL_TEST_MAINTENANCE_EXEC_EXIT", "1")
	err := npmInstallGlobalAtLatest(t.Context(), c, id, "coder", "@vendor/agent")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("npm maintenance error = %v, want wrapped exit status 1", err)
	}
	if !strings.Contains(err.Error(), "npm is unavailable on that PATH") || !strings.Contains(err.Error(), "--no-autoupdate") {
		t.Fatalf("npm maintenance error lacks PATH and autoupdate guidance: %v", err)
	}
}

// A box built before the managed agent folder fails the install as the
// missing maintenance user; the error names the recreate command with the box
// name instead of the npm PATH hint (devOpUs live QA r1). Only a successful
// `test ! -d` gives that remedy: a folder that exists or a probe that cannot
// run keeps the install's own error (archyAstra r1).
func TestNpmMaintenanceOnABoxWithoutTheManagedFolderNamesRecreate(t *testing.T) {
	id := strings.Repeat("f", 64)
	for _, tc := range []struct {
		probeExit string
		remedy    bool
	}{{"0", true}, {"1", false}, {"7", false}} {
		t.Run("probe-exit-"+tc.probeExit, func(t *testing.T) {
			c, calls := maintenanceTargetFixture(t, "[]")
			t.Setenv("YOLOCTL_TEST_MAINTENANCE_EXEC_EXIT", "7")
			t.Setenv("YOLOCTL_TEST_MAINTENANCE_PROBE_EXIT", tc.probeExit)
			err := npmInstallGlobalAtLatest(t.Context(), c, id, "coder", "@vendor/agent")
			remedy := err != nil && strings.Contains(err.Error(), "predates the managed agent folder /opt/yoloctl/npm") &&
				strings.Contains(err.Error(), "yoloctl box recreate coder --space fixture")
			if remedy != tc.remedy || err == nil || remedy == strings.Contains(err.Error(), "npm is unavailable") {
				t.Fatalf("install error = %v, want remedy %v", err, tc.remedy)
			}
			probe := []string{"sudo", "nerdctl", "exec", "--user", "sandbox", "--env", wantAgentMaintenancePath, id, "test", "!", "-d", "/opt/yoloctl/npm"}
			got := calls()
			if len(got) != 2 || !slices.Equal(got[1], probe) {
				t.Fatalf("calls = %q, want the install then the folder probe on the selected container", got)
			}
		})
	}
}
