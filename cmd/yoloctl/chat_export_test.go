package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
)

var exportNow = time.Date(2026, 11, 13, 14, 2, 7, 0, time.UTC)

func exportPlan(t *testing.T, flags map[string]string, channels ...string) (chatExportPlan, error) {
	t.Helper()
	cmd := newChatExportCmd()
	for name, value := range flags {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, channel := range channels {
		if err := cmd.Flags().Set("channel", channel); err != nil {
			t.Fatal(err)
		}
	}
	return planChatExport(cmd, "dev", []string{"#space", "#ux"}, exportNow)
}

func TestChatExportTargets(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	p, err := exportPlan(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.format != "text" || len(p.channels) != 2 || p.targets["#space"] != "chat-dev-space-20261113T140207Z.txt" || p.targets["#ux"] != "chat-dev-ux-20261113T140207Z.txt" {
		t.Fatalf("default plan %+v", p)
	}
	p, err = exportPlan(t, map[string]string{"format": "jsonl", "output": "one.jsonl"}, "ux")
	if err != nil || len(p.channels) != 1 || p.targets["#ux"] != "one.jsonl" {
		t.Fatalf("one channel to a file: %+v %v", p, err)
	}
	sub := filepath.Join(dir, "out")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	p, err = exportPlan(t, map[string]string{"output": sub})
	if err != nil || p.targets["#ux"] != filepath.Join(sub, "chat-dev-ux-20261113T140207Z.txt") {
		t.Fatalf("several channels to a directory: %+v %v", p, err)
	}
	p, err = exportPlan(t, map[string]string{"output": "-"}, "#space")
	if err != nil || p.targets["#space"] != "-" {
		t.Fatalf("standard output: %+v %v", p, err)
	}
	for _, c := range []struct {
		flags    map[string]string
		channels []string
		want     string
	}{
		{map[string]string{"output": "-"}, nil, "-o - writes one channel"},
		{map[string]string{"output": "file.txt"}, nil, "must be an existing directory"},
		{nil, []string{"#nope"}, "unknown channel #nope"},
		{map[string]string{"format": "csv"}, nil, "use text or jsonl"},
		{map[string]string{"since": "yesterday"}, nil, "not an RFC 3339 time"},
		{map[string]string{"since": "2026-11-13", "until": "2026-11-13T00:00:00Z"}, nil, "--since must be before --until"},
	} {
		if _, err := exportPlan(t, c.flags, c.channels...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("flags %v channels %v: %v, want %q", c.flags, c.channels, err, c.want)
		}
	}
}

func TestChatExportRefusesAnExistingFileWithoutForce(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("kept.txt", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exportPlan(t, map[string]string{"output": "kept.txt"}, "#ux"); err == nil || !strings.Contains(err.Error(), "already exists; use --force") {
		t.Fatalf("existing target: %v", err)
	}
	if _, err := exportPlan(t, map[string]string{"output": "kept.txt", "force": "true"}, "#ux"); err != nil {
		t.Fatal(err)
	}
}

func TestChatExportWritesCompleteChannelsOnly(t *testing.T) {
	dir := t.TempDir()
	plan := chatExportPlan{space: "dev", channels: []string{"#ux"}, format: "text", force: true, exported: exportNow,
		targets: map[string]string{"#ux": filepath.Join(dir, "ux.txt")}}
	if err := os.WriteFile(plan.targets["#ux"], []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, report bytes.Buffer
	// A failed channel touches nothing, even with --force.
	if err := writeChatExport(plan, chatstream.ExportResult{Channel: "#ux", Err: errors.New("page refused")}, &stdout, &report); err == nil {
		t.Fatal("failed channel reported success")
	}
	if data, _ := os.ReadFile(plan.targets["#ux"]); string(data) != "old" {
		t.Fatalf("failed export changed the file: %q", data)
	}
	messages := []chatstream.Message{{Channel: "#ux", ID: "m1", Nick: "opus", Text: "hello", Time: exportNow.Add(-time.Hour)}, {Channel: "#ux", ID: "m2", Nick: "opus", Text: "later", Time: exportNow}}
	plan.since = exportNow.Add(-time.Minute)
	if err := writeChatExport(plan, chatstream.ExportResult{Channel: "#ux", Messages: messages}, &stdout, &report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(plan.targets["#ux"])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("written file %v %v", info, err)
	}
	data, _ := os.ReadFile(plan.targets["#ux"])
	if !strings.Contains(string(data), "opus: later") || strings.Contains(string(data), "opus: hello") || !strings.Contains(string(data), "# Received: 2 messages") {
		t.Fatalf("file:\n%s", data)
	}
	if !strings.Contains(report.String(), "#ux: 1 messages written to "+plan.targets["#ux"]+" (received 2,") || stdout.Len() != 0 {
		t.Fatalf("report %q stdout %q", report.String(), stdout.String())
	}
	// Standard output receives the transcript itself.
	plan.targets["#ux"] = "-"
	stdout.Reset()
	if err := writeChatExport(plan, chatstream.ExportResult{Channel: "#ux", Messages: messages}, &stdout, &report); err != nil || !strings.Contains(stdout.String(), "opus: later") {
		t.Fatalf("stdout %q err %v", stdout.String(), err)
	}
}

func TestChatExportNamesTheWaysOutWhenTheNicknameIsHeld(t *testing.T) {
	dir := t.TempDir()
	plan := chatExportPlan{space: "dev", channels: []string{"#space"}, format: "text", exported: exportNow,
		targets: map[string]string{"#space": filepath.Join(dir, "space.txt")}}
	var stdout, stderr bytes.Buffer
	err := exportChat(t.Context(), plan, "#space", func(context.Context, io.Reader, io.Writer) error { return chatstream.ErrNickHeld }, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "close it, or rerun with --take-over") || strings.Contains(err.Error(), "/export") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(plan.targets["#space"]); !os.IsNotExist(err) {
		t.Fatal("a file was written")
	}
}

func TestChatExportNeverReplacesAFileThatAppeared(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ux.txt")
	plan := chatExportPlan{space: "dev", channels: []string{"#ux"}, format: "text", exported: exportNow, targets: map[string]string{"#ux": target}}
	// Planned while absent; another writer creates it before publication.
	if err := os.WriteFile(target, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, report bytes.Buffer
	err := writeChatExport(plan, chatstream.ExportResult{Channel: "#ux", Messages: []chatstream.Message{{ID: "m1", Nick: "opus", Text: "hi", Time: exportNow}}}, &stdout, &report)
	if err == nil || !strings.Contains(err.Error(), "appeared during the export; it was not replaced") {
		t.Fatalf("err %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "theirs" {
		t.Fatalf("file replaced: %q", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary file left: %v", entries)
	}
}

func TestChatExportClassifiesConnectionErrorsAndInterrupts(t *testing.T) {
	plan := chatExportPlan{space: "dev", channels: []string{"#space"}, format: "text", exported: exportNow, targets: map[string]string{"#space": filepath.Join(t.TempDir(), "space.txt")}}
	var stdout, stderr bytes.Buffer
	refused := func(context.Context, io.Reader, io.Writer) error { return errors.New("controller unavailable") }
	if err := exportChat(t.Context(), plan, "#space", refused, &stdout, &stderr); err == nil || !strings.HasPrefix(err.Error(), "chat connection: ") {
		t.Fatalf("connection error: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	waits := func(ctx context.Context, _ io.Reader, _ io.Writer) error { <-ctx.Done(); return ctx.Err() }
	if err := exportChat(ctx, plan, "#space", waits, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "export interrupted") {
		t.Fatalf("interrupt: %v", err)
	}
}
