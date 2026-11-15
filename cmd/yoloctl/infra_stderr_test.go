package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/leogr/yoloctl/pkg/prettylog/stepview"
	"github.com/spf13/cobra"
)

// A VM-side controller-internal command (export --stop's stop-workloads)
// writes its own plain records and raw tool text to stderr. While the steps
// block is on screen, its record lines are shown through the process Logger
// (pretty, above the block) and other lines unchanged above the block;
// nothing is glued to the block.
func TestInfraStderrGoesThroughTheStepsBlock(t *testing.T) {
	var screen bytes.Buffer
	l := prettylog.New(&screen, prettylog.Options{Format: prettylog.FormatPretty, Verbosity: prettylog.Normal})
	restore := logging.SetLogger(l)
	defer restore()
	view := stepview.Install(l, &screen, stepview.Options{Interval: time.Hour})
	defer view.Close()
	step := l.StartStep("Exporting space yolo", prettylog.Actor(logging.Host))
	w := newInfraLines(view.Writer(&screen))
	_, _ = w.Write([]byte("notice: host: stopping box argon\nnotice: host: stopp"))
	_, _ = w.Write([]byte("ing box devOpus2\nraw tool text\n2026-11-15T10:00:00Z warning: host: slow stop\ntrailing"))
	w.flush()
	step.Done()
	view.Close()
	out := ansi.Strip(screen.String())
	for _, want := range []string{"stopping box argon", "stopping box devOpus2", "raw tool text", "slow stop", "trailing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// Re-emitted through the pretty Logger, not copied as plain text.
	if strings.Contains(out, "notice: host:") || strings.Contains(out, "warning: host:") {
		t.Fatalf("record line copied raw:\n%s", out)
	}
	// No record is glued to the end of another line.
	// The block redraws with CR and erase-line, so a screen line is
	// whatever lies between CR or LF.
	for _, line := range strings.FieldsFunc(out, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if strings.Count(line, "stopping box") > 1 || strings.Contains(line, "yolo") && strings.Contains(line, "stopping box") {
			t.Fatalf("glued line %q in:\n%s", line, out)
		}
	}
}

// Under the steps block a successful operation leaves one final line: the
// root step's summary, not a second outcome line repeating it.
func TestCLIOperationLeavesOneFinalLineUnderTheStepsBlock(t *testing.T) {
	var screen bytes.Buffer
	l := prettylog.New(&screen, prettylog.Options{Format: prettylog.FormatPretty, Verbosity: prettylog.Normal})
	restore := logging.SetLogger(l)
	defer restore()
	view := stepview.Install(l, &screen, stepview.Options{Interval: time.Hour})
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := cliOperation(cmd, "Exporting space yolo", "Exported space yolo", "yolo", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	view.Close()
	out := ansi.Strip(screen.String())
	if !strings.Contains(out, "Exporting space yolo") || strings.Contains(out, "Exported space yolo") {
		t.Fatalf("want one final line:\n%s", out)
	}
}
