package main

import (
	"encoding/json"
	"io"
	"os"
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
 if len(args)>6 and args[6]=='cat': print('{"version":"1.2.3"}')
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

func TestMaintenanceExecsUseExactlySelectedContainer(t *testing.T) {
	id := strings.Repeat("f", 64)
	valid := `[{"Id":"c1` + strings.Repeat("0", 62) + `","Name":"peer"},{"Id":"` + id + `","Name":"c1"}]`
	operations := map[string]func(*container.Client, string) error{
		"npm-install": func(c *container.Client, target string) error {
			return npmInstallGlobalAtLatest(t.Context(), c, target, "@vendor/agent")
		},
		"native-install": func(c *container.Client, target string) error {
			return installAgentLatest(t.Context(), c, target, &agentprofile.Profile{NativeRelease: "antigravity"})
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
				prefix := []string{"sudo", "nerdctl", "exec", "--user", "sandbox", id}
				if len(got) != 1 || len(got[0]) <= len(prefix) || !slices.Equal(got[0][:len(prefix)], prefix) {
					t.Fatal("maintenance exec used a name or changed user", got)
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
	if err := installAgentLatest(t.Context(), c, selected, &agentprofile.Profile{NpmPackage: "@vendor/agent"}); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 3 || got[0][2] != "inspect" || got[1][5] != id || got[2][5] != id {
		t.Fatal("probe/install reselected the name", got)
	}
}
