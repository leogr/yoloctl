package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// A command's report or notice that would go to the process stderr is a log
// record; an explicit caller writer (a pipe, a buffer) still gets its line.
func TestCLIReportsToTheProcessStderrAreLogRecords(t *testing.T) {
	var logs bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &logs))
	cliReportTo(os.Stderr, slog.LevelError, logging.Chat, "%s: export failed: %v", "#ux", "boom")
	if got := logs.String(); got != "error: chat: #ux: export failed: boom\n" {
		t.Fatalf("record %q", got)
	}
	var w bytes.Buffer
	cliReportTo(&w, slog.LevelInfo, logging.Chat, "%s: %d messages", "#ux", 3)
	if w.String() != "#ux: 3 messages\n" || strings.Contains(logs.String(), "3 messages") {
		t.Fatalf("explicit writer %q, logs %q", w.String(), logs.String())
	}
	if cliNoticeSink(os.Stderr) != agentboximage.LogNotices || cliNoticeSink(&w) != &w {
		t.Fatal("image notices to the process stderr are not log records")
	}
}

// The post-autoupdate hint is one notice whose commands are its continuation
// lines, not raw text.
func TestPostAutoupdateHintIsALogRecord(t *testing.T) {
	var logs bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &logs))
	warnPostAutoupdateFailure("coder", "codex", 1)
	want := "notice: session: the agent in coder exited (status 1) right after an autoupdate."
	if got := logs.String(); !strings.HasPrefix(got, want) ||
		!strings.Contains(got, "\n  yoloctl box recreate coder\n  yoloctl box inspect coder   # shows") {
		t.Fatalf("record %q", got)
	}
}

// A listing longer than one record's message limit is split across
// records, never cut: every line arrives whole.
func TestCLILogLinesSplitsLongListingsWithoutCutting(t *testing.T) {
	var logs bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &logs))
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("yoloctl box restart box-with-a-long-name-%02d --space work --no-attach", i))
	}
	cliLogLines(prettylog.LevelNotice, logging.Host, "Nothing was restarted. To restart them:", lines)
	out := logs.String()
	if strings.Contains(out, "bytes)") || strings.Count(out, "notice: host: ") < 2 {
		t.Fatalf("listing cut or not split:\n%s", out)
	}
	for _, line := range lines {
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("missing %q in:\n%s", line, out)
		}
	}
}
