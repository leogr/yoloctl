package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// lineWith returns the first output line containing sub (for asserting a
// specific row's columns).
func lineWith(out, sub string) string {
	for ln := range strings.SplitSeq(out, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
}

func TestAgentboxDestroyUncertainRuntimePreservesDeclaration(t *testing.T) {
	for _, failure := range []string{"stopped space", "missing space", "space probe", "container inventory"} {
		t.Run(failure, func(t *testing.T) {
			for _, flags := range [][]string{nil, {"--force"}, {"--keep-manifest"}, {"--force", "--keep-manifest"}} {
				t.Run(strings.Join(flags, ","), func(t *testing.T) {
					t.Setenv("XDG_CONFIG_HOME", t.TempDir())
					saveSpaceForTest(t, defaultSpaceName, "/tmp/repo")
					cfg := &agentbox.Config{
						SchemaVersion: agentbox.SchemaVersion,
						Name:          "coder",
						Space:         defaultSpaceName,
						Image:         "img",
						Features:      []string{"codex"},
						Workdir: agentbox.Workdir{
							Mode:      agentbox.WorkdirModeHost,
							Source:    "/tmp/repo",
							Canonical: "/tmp/repo",
						},
					}
					if err := cfg.Save(); err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(cfg.Path())
					if err != nil {
						t.Fatal(err)
					}
					probeErr := errors.New("runtime probe failed")
					old := newBoxStateResolver
					newBoxStateResolver = func() boxstate.Resolver {
						return boxstate.Resolver{
							ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
								switch failure {
								case "stopped space":
									return []vmhost.Instance{{Name: defaultSpaceName, Status: "Stopped"}}, nil
								case "missing space":
									return nil, nil
								case "space probe":
									return nil, probeErr
								default:
									return []vmhost.Instance{{Name: defaultSpaceName, Status: "Running"}}, nil
								}
							},
							ListContainers: func(context.Context, string) ([]container.Container, error) {
								if failure != "container inventory" {
									t.Fatal("queried containers despite unavailable space")
								}
								// Even partial inventory data cannot authorize teardown.
								return []container.Container{{Name: "coder", Status: "Up 1 minute"}}, probeErr
							},
							ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
								t.Fatal("destroy must not probe sessions before an authoritative inventory")
								return boxstate.SessionInfo{}
							},
							InspectActual: func(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
								t.Fatal("destroy must not inspect drift")
								return agentbox.ActualState{}, nil
							},
							ComposePolicy: func(*agentbox.Config) ([]byte, error) {
								t.Fatal("destroy must not compose policy")
								return nil, nil
							},
						}
					}
					t.Cleanup(func() { newBoxStateResolver = old })

					cmd := newAgentboxDestroyCmd()
					var errbuf bytes.Buffer
					cmd.SetErr(&errbuf)
					cmd.SetOut(io.Discard)
					cmd.SetArgs(append([]string{"coder"}, flags...))
					err = cmd.Execute()
					if err == nil {
						t.Error("destroy reported success with an uncertain runtime")
					} else if strings.HasSuffix(failure, "probe") || failure == "container inventory" {
						if !errors.Is(err, probeErr) {
							t.Errorf("destroy lost inventory error: %v", err)
						}
					} else if !strings.Contains(err.Error(), "space runtime unavailable") {
						t.Errorf("destroy error = %v", err)
					}
					after, err := os.ReadFile(cfg.Path())
					if err != nil || !bytes.Equal(before, after) {
						t.Errorf("destroy altered the declaration with uncertain runtime: %v", err)
					}
					if strings.Contains(errbuf.String(), "removed declaration") {
						t.Errorf("destroy advertised declaration-only removal: %s", &errbuf)
					}
				})
			}
		})
	}
}

func TestWriteBoxStateJSONIsSingleObject(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBoxStateJSON(&buf, boxstate.Box{
		Space:       "default",
		Name:        "coder",
		State:       boxstate.DisplayConfigured,
		Declaration: boxstate.DeclarationDeclared,
		Runtime:     boxstate.RuntimeMissing,
		Session:     boxstate.SessionInfo{State: boxstate.SessionNone},
		Drift:       boxstate.DriftNeedsCreate,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.TrimSpace(buf.String()), "[") {
		t.Fatalf("show JSON must be an object, got array: %s", buf.String())
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out["name"] != "coder" || out["state"] != string(boxstate.DisplayConfigured) {
		t.Fatalf("unexpected json object: %v", out)
	}
}

func TestAgentboxListDefaultShowsDeclaredState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveCompletionBox(t, defaultSpaceName, "coder")
	withCompletionResolver(t, nil)

	out := runAgentboxListForTest(t)
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "STATE") || !strings.Contains(out, "SESSION") {
		t.Fatalf("missing state columns:\n%s", out)
	}
	line := lineWith(out, "coder")
	if !strings.Contains(line, "configured") || !strings.Contains(line, "/tmp/def") {
		t.Fatalf("declared box row = %q, want configured with workdir", line)
	}
}

func TestAgentboxListStateFilters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveCompletionBox(t, defaultSpaceName, "running")
	saveCompletionBox(t, defaultSpaceName, "stopped")
	saveCompletionBox(t, defaultSpaceName, "configured")
	withCompletionResolver(t, []container.Container{
		{Name: "running", Status: "Up 1 minute", Image: "img"},
		{Name: "stopped", Status: "Exited (0)", Image: "img"},
	})

	if out := runAgentboxListForTest(t, "--running"); !strings.Contains(out, "running") ||
		strings.Contains(out, "stopped") || strings.Contains(out, "configured") {
		t.Fatalf("--running output =\n%s", out)
	}
	if out := runAgentboxListForTest(t, "--runtime"); !strings.Contains(out, "running") ||
		!strings.Contains(out, "stopped") || strings.Contains(out, "configured") {
		t.Fatalf("--runtime output =\n%s", out)
	}
	if out := runAgentboxListForTest(t, "--state", "stopped"); strings.Contains(out, "running") ||
		!strings.Contains(out, "stopped") || strings.Contains(out, "configured") {
		t.Fatalf("--state stopped output =\n%s", out)
	}
}

func TestAgentboxListJSONIncludesDimensions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveCompletionBox(t, defaultSpaceName, "coder")
	withCompletionResolver(t, nil)

	out := runAgentboxListForTest(t, "--json")
	var rows []struct {
		Name       string `json:"name"`
		State      string `json:"state"`
		Dimensions struct {
			Declaration string `json:"declaration"`
			Runtime     string `json:"runtime"`
			Session     string `json:"session"`
			Drift       string `json:"drift"`
		} `json:"dimensions"`
		Reasons []struct {
			Code string `json:"code"`
		} `json:"reasons"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %v", len(rows), rows)
	}
	row := rows[0]
	if row.Name != "coder" || row.State != string(boxstate.DisplayConfigured) ||
		row.Dimensions.Declaration != string(boxstate.DeclarationDeclared) ||
		row.Dimensions.Runtime != string(boxstate.RuntimeMissing) ||
		row.Dimensions.Session != string(boxstate.SessionNone) {
		t.Fatalf("unexpected row: %+v", row)
	}
	if len(row.Reasons) != 1 || row.Reasons[0].Code != string(boxstate.ReasonContainerMissing) {
		t.Fatalf("unexpected reasons: %+v", row.Reasons)
	}
}

func TestAgentboxListShowsUnavailableDeclarations(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveSpaceForTest(t, defaultSpaceName, "/tmp/def")
	saveCompletionBox(t, defaultSpaceName, "coder")

	old := newBoxStateResolver
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: defaultSpaceName, Status: "Stopped"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				t.Fatal("list should not query containers when the space runtime is unavailable")
				return nil, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				return boxstate.SessionInfo{State: boxstate.SessionUnknown}
			},
			InspectActual: func(context.Context, *agentbox.Config) (agentbox.ActualState, error) {
				t.Fatal("list should not inspect actual state")
				return agentbox.ActualState{}, nil
			},
			ComposePolicy: func(*agentbox.Config) ([]byte, error) {
				t.Fatal("list should not compose policy")
				return nil, nil
			},
			ResolvedImage: func(*agentbox.Config) (string, bool, error) {
				t.Fatal("list should not resolve image")
				return "", false, nil
			},
		}
	}
	t.Cleanup(func() { newBoxStateResolver = old })

	out := runAgentboxListForTest(t)
	line := lineWith(out, "coder")
	if !strings.Contains(line, "unavailable") || !strings.Contains(line, "Stopped") {
		t.Fatalf("unavailable row = %q\nfull output:\n%s", line, out)
	}
}

func runAgentboxListForTest(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newAgentboxListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("box list %v: %v", args, err)
	}
	return out.String()
}
