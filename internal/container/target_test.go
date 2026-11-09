package container

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func nameTargetFixture(t *testing.T, raw string) (*Client, string) {
	t.Helper()
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("YOLOCTL_TEST_TARGET_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, body := range map[string]string{
		"inspect.json": raw,
		"limactl": `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_TEST_TARGET_DIR/calls"
case "$*" in
  *'nerdctl inspect '*) cat "$YOLOCTL_TEST_TARGET_DIR/inspect.json"; exit "${YOLOCTL_TEST_INSPECT_EXIT:-0}" ;;
  *) exit "${YOLOCTL_TEST_OPERATION_EXIT:-0}" ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return NewWithOutput("fixture", io.Discard, io.Discard), filepath.Join(dir, "calls")
}

func targetCalls(t *testing.T, log string) string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestNameOperationsUseExactContainerID(t *testing.T) {
	id := strings.Repeat("f", 64)
	other := "c1" + strings.Repeat("0", 62)
	operations := []struct {
		name, argv string
		run        func(*Client, string) error
	}{
		{"start", "start ", func(c *Client, name string) error { return c.Start(t.Context(), name) }},
		{"stop", "stop --time 30 ", func(c *Client, name string) error { return c.Stop(t.Context(), name) }},
		{"remove", "rm ", func(c *Client, name string) error { return c.Remove(t.Context(), name, false) }},
		{"force-remove", "rm -f ", func(c *Client, name string) error { return c.Remove(t.Context(), name, true) }},
	}
	for _, op := range operations {
		for _, direct := range []bool{false, true} {
			t.Run(op.name+"/direct="+strconv.FormatBool(direct), func(t *testing.T) {
				c, log := nameTargetFixture(t, string(inspectArray(nerdctlEntry(other, "peer", "", ""), nerdctlEntry(id, "c1", "", ""))))
				name := "c1"
				if direct {
					name = id
				}
				if err := op.run(c, name); err != nil {
					t.Fatal(err)
				}
				calls := targetCalls(t, log)
				want := "shell yolo-fixture -- sudo nerdctl " + op.argv + id + "\n"
				if !direct {
					want = "shell yolo-fixture -- sudo nerdctl inspect c1\n" + want
				}
				if calls != want {
					t.Fatalf("target selection changed or repeated: %q, want %q", calls, want)
				}
			})
		}
	}
}

func TestNameOperationsRefuseUnresolvedIDsWithoutEffects(t *testing.T) {
	id := strings.Repeat("f", 64)
	for name, raw := range map[string]string{
		"absent": "[]", "malformed-json": "[",
		"prefix-only":   string(inspectArray(nerdctlEntry("c1"+strings.Repeat("0", 62), "peer", "", ""))),
		"duplicate":     string(inspectArray(nerdctlEntry(id, "c1", "", ""), nerdctlEntry(strings.Repeat("a", 64), "c1", "", ""))),
		"empty-id":      string(inspectArray(nerdctlEntry("", "c1", "", ""))),
		"short-id":      string(inspectArray(nerdctlEntry("abc", "c1", "", ""))),
		"uppercase-id":  string(inspectArray(nerdctlEntry(strings.Repeat("F", 64), "c1", "", ""))),
		"inspect-error": string(inspectArray(nerdctlEntry(id, "c1", "", ""))),
	} {
		for _, op := range []string{"start", "stop", "remove", "force-remove"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				c, log := nameTargetFixture(t, raw)
				if name == "inspect-error" {
					t.Setenv("YOLOCTL_TEST_INSPECT_EXIT", "13")
				}
				var err error
				switch op {
				case "start":
					err = c.Start(t.Context(), "c1")
				case "stop":
					err = c.Stop(t.Context(), "c1")
				default:
					err = c.Remove(t.Context(), "c1", op == "force-remove")
				}
				if err == nil || targetCalls(t, log) != "shell yolo-fixture -- sudo nerdctl inspect c1\n" {
					t.Fatal("unresolved identity reached an effect", err, targetCalls(t, log))
				}
			})
		}
	}
}

func TestSelectedContainerDisappearanceDoesNotRetarget(t *testing.T) {
	id := strings.Repeat("f", 64)
	c, log := nameTargetFixture(t, string(inspectArray(nerdctlEntry(id, "c1", "", ""))))
	t.Setenv("YOLOCTL_TEST_OPERATION_EXIT", "19")
	var exit *exec.ExitError
	if err := c.Start(t.Context(), "c1"); !errors.As(err, &exit) || exit.ExitCode() != 19 {
		t.Fatal("actual start error lost", err)
	}
	calls := targetCalls(t, log)
	if strings.Count(calls, "nerdctl inspect") != 1 || strings.Count(calls, "nerdctl start "+id+"\n") != 1 || strings.Count(calls, "\n") != 2 {
		t.Fatal("selected resource was retried or retargeted", calls)
	}
}

func TestInvalidFixedIDAndCancellationIssueNoOperation(t *testing.T) {
	c, log := nameTargetFixture(t, "[]")
	for _, id := range []string{strings.Repeat("A", 64), strings.Repeat("f", 63) + "-"} {
		if err := c.Start(t.Context(), id); err == nil {
			t.Fatal("invalid full ID accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, target := range []string{"c1", strings.Repeat("f", 64)} {
		if err := c.Start(ctx, target); !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
	}
	if calls := targetCalls(t, log); calls != "" {
		t.Fatal("invalid or canceled target reached the adapter", calls)
	}
}
