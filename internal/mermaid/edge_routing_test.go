package mermaid

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Exercise the shipped WASI bytes with the reported cyclic flowchart, not a
// substitute Go renderer. Border cells need a leg into the adjacent route cell;
// flat borders and triangle-only arrowheads leave half-cell gaps on terminals.
func runtimeFlowDiagram(t *testing.T, width int) Result {
	t.Helper()
	skipUnderRace(t)
	ctx := t.Context()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	defer func() { _ = rt.Close(ctx) }()
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		t.Fatal(err)
	}
	compiled, err := rt.CompileModule(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/fixtures/runtime-edges.mmd")
	if err != nil {
		t.Fatal(err)
	}
	result := render(ctx, rt, compiled, request{protocol, moduleHash(), width, "grapheme", string(raw)}, ansi.GraphemeWidth, workerLimits{1024, 2 * time.Second, MaxDrawing})
	if result.Status != OK {
		t.Fatalf("runtime fixture: %+v", result)
	}
	return result
}

func TestRuntimeFlowEdgesJoin(t *testing.T) {
	result := runtimeFlowDiagram(t, 160)
	drawing := strings.Join(result.Lines, "\n")
	labelRow := func(label string) int {
		t.Helper()
		for i, line := range result.Lines {
			if strings.Contains(line, label) {
				return i
			}
		}
		t.Fatalf("missing %q:\n%s", label, drawing)
		return -1
	}
	sup := labelRow("OpenShell supervisor")
	if !strings.ContainsRune(result.Lines[sup+2], '┬') {
		t.Fatalf("supervisor outgoing edge lacks its border leg:\n%s", drawing)
	}
	agent := labelRow("Agent = main process")
	if !strings.ContainsRune(result.Lines[agent-1], '↓') {
		t.Fatalf("agent's incoming arrow lacks a shaft:\n%s", drawing)
	}
	row := []rune(result.Lines[agent])
	exit := -1
	for i := 0; i+1 < len(row); i++ {
		if row[i] == '├' && row[i+1] == '┐' {
			exit = i + 1
			break
		}
	}
	if exit < 0 {
		t.Fatalf("agent's back edge has no connected downward corner:\n%s", drawing)
	}
	next := []rune(result.Lines[agent+1])
	if exit >= len(next) || next[exit] != '│' {
		t.Fatalf("agent's side edge breaks below its corner:\n%s", drawing)
	}
	ports := []int{}
	for c, ch := range []rune(result.Lines[sup-1]) {
		if ch == '↓' {
			ports = append(ports, c)
		}
	}
	if len(ports) != 2 || ports[1]-ports[0] < 4 {
		t.Fatalf("incoming edges have adjacent ports: %v\n%s", ports, drawing)
	}
	for r := sup - 3; r < sup-1; r++ {
		chars := []rune(result.Lines[r])
		for c := ports[0]; c <= ports[1] && c < len(chars); c++ {
			if chars[c] == '┼' || chars[c] == '┘' {
				t.Fatalf("incoming edges cross/jog above their ports:\n%s", drawing)
			}
		}
	}
}

// The outgoing return edge must not share a protected incoming arrow's port.
func TestRuntimeFlowSideSourceJoin(t *testing.T) {
	for _, width := range []int{100, 120, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			result := runtimeFlowDiagram(t, width)
			for _, line := range result.Lines {
				if _, suffix, found := strings.Cut(line, "Box: PID 1 helper"); found {
					if !strings.HasPrefix(strings.TrimSpace(suffix), "├") {
						t.Fatalf("PID 1 side source stays detached:\n%s", strings.Join(result.Lines, "\n"))
					}
					return
				}
			}
			t.Fatal("PID 1 label missing")
		})
	}
}

func TestRuntimeFlowLabelPreservesSiblingEdge(t *testing.T) {
	for _, width := range []int{100, 120, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			result := runtimeFlowDiagram(t, width)
			for _, line := range result.Lines {
				if prefix, _, found := strings.Cut(line, "versioned RPC"); found {
					if !strings.Contains(prefix, "│ │") {
						t.Fatalf("RPC label overwrites sibling box-attach line:\n%s", strings.Join(result.Lines, "\n"))
					}
					return
				}
			}
			t.Fatal("versioned RPC label missing")
		})
	}
}
