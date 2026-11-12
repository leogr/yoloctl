package mermaid

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/leogr/yoloctl/internal/mermaid/admit"
)

var updateGoldens = flag.Bool("update", false, "rewrite the fidelity goldens (they then need a human review)")

// fidelityWidths are the card widths the reviewed corpus is drawn at.
var fidelityWidths = []int{40, 60, 80}

// golden is what a fixture yields at one width: the drawing, or its status.
func golden(r Result) string {
	switch r.Status {
	case OK:
		return strings.Join(r.Lines, "\n") + "\n"
	case TooWide:
		return fmt.Sprintf("status: too_wide needs %d\n", r.NeedCols)
	default:
		return "status: " + string(r.Status) + "\n"
	}
}

// Every reviewed fixture is admitted and draws exactly its reviewed golden
// with the embedded artifact, at each corpus width. A crate or patch change
// that alters a drawing fails here and needs a new human review.
func TestFidelityCorpusMatchesReviewedGoldens(t *testing.T) {
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
	files, err := filepath.Glob("testdata/fixtures/*.mmd")
	if err != nil || len(files) < 20 {
		t.Fatalf("corpus: %d fixtures, %v", len(files), err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		if d := admit.Check(source); !d.Admitted() {
			t.Errorf("%s not admitted: %s", f, d.Reason)
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".mmd")
		for _, width := range fidelityWidths {
			r := render(ctx, rt, compiled, request{protocol, moduleHash(), width, "grapheme", source}, ansi.GraphemeWidth, workerLimits{1024, 2 * time.Second, MaxDrawing})
			got := golden(r)
			path := filepath.Join("testdata", "golden", fmt.Sprintf("%s.w%d.txt", name, width))
			if *updateGoldens {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: no reviewed golden: %v", path, err)
				continue
			}
			if got != string(want) {
				t.Errorf("%s changed:\n--- reviewed\n%s--- now\n%s", path, want, got)
			}
		}
	}
}
