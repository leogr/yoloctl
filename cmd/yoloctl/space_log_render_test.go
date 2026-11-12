package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func spaceLogSettings(t *testing.T, level string) logging.Settings {
	t.Helper()
	v, err := logging.ParseLogLevel(level)
	if err != nil {
		t.Fatal(err)
	}
	return logging.Settings{Verbosity: v, Color: prettylog.ColorNever}
}

func recordAttr(r slog.Record, key string) string {
	var value string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			value = a.Value.String()
		}
		return true
	})
	return value
}

func TestSpaceLogRecordPresentationOnly(t *testing.T) {
	for _, tc := range []struct {
		line, msg, actor, executor string
		level                      slog.Level
		kind                       prettylog.Kind
	}{
		{"2026-11-11T10:20:30.120Z warning: controller: Store degraded: denied", "Store degraded: denied", "controller", "", slog.LevelWarn, prettylog.KindWarn},
		{"controller: episode=1 abnormal_exits=2", "episode=1 abnormal_exits=2", "controller", "", slog.LevelInfo, prettylog.KindInfo},
		{"error: vm/firecracker: VM stopped", "VM stopped", "vm", "firecracker", slog.LevelError, prettylog.KindError},
		{`{"time":"2026-11-11","msg":"vendor output"}`, `{"time":"2026-11-11","msg":"vendor output"}`, "", "", slog.LevelInfo, prettylog.KindInfo},
		{"uncertain: controller: Outcome may be uncertain", "Outcome may be uncertain", "controller", "", slog.LevelWarn, prettylog.KindUnknown},
	} {
		t.Run(tc.line, func(t *testing.T) {
			r := spaceLogRecord(tc.line)
			if r.Message != tc.msg || r.Level != tc.level || prettylog.KindOf(r) != tc.kind || recordAttr(r, prettylog.KeyActor) != tc.actor || recordAttr(r, prettylog.KeyExecutor) != tc.executor {
				t.Fatalf("record %+v", r)
			}
		})
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-11-11T10:20:30.120Z")
	if got := spaceLogRecord("2026-11-11T10:20:30.120Z controller: Ready").Time; !got.Equal(want) {
		t.Fatal(got)
	}
}

func TestSpaceLogViewRendersAndFlushes(t *testing.T) {
	var out bytes.Buffer
	view := newSpaceLogOutput(&out, spaceLogSettings(t, "info"))
	for _, part := range []string{"2026-11-11T10:20:30.120Z warning: contr", "oller: Degraded\x1b[31m\n\npartial"} {
		if _, err := view.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "warning: controller: Degraded\\x1b[31m") || !strings.Contains(text, "\n\n") || !strings.Contains(text, "partial\n") || strings.ContainsRune(text, '\x1b') {
		t.Fatalf("rendered %q", text)
	}
}

func TestSpaceLogViewDebugKeepsBytes(t *testing.T) {
	var out bytes.Buffer
	settings := spaceLogSettings(t, "debug")
	settings.Color = prettylog.ColorAlways
	view := newSpaceLogOutput(&out, settings)
	raw := []byte("raw\x00\x1b[31m\r\npartial")
	if n, err := view.Write(raw); n != len(raw) || err != nil {
		t.Fatal(n, err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("debug bytes %q", out.Bytes())
	}
}

func TestSpaceLogViewFoldsAndBoundsPartialLines(t *testing.T) {
	var out bytes.Buffer
	view := newSpaceLogOutput(&out, spaceLogSettings(t, "info"))
	for range 3 {
		if _, err := view.Write([]byte("warning: vm: Repeated\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := view.Write(bytes.Repeat([]byte("x"), spaceLogLineLimit*3+7)); err != nil {
		t.Fatal(err)
	}
	if len(view.pending) > spaceLogLineLimit {
		t.Fatal("unbounded pending bytes")
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "repeated 2 more times") || strings.Count(out.String(), "Repeated") != 1 {
		t.Fatalf("fold output %q", out.String())
	}
}

func TestSpaceLogViewFiltersUserLevels(t *testing.T) {
	input := "debug: controller: debug_entry\n\nvendor_entry\n\ncontroller: info_entry\nwarning: controller: warn_entry\nuncertain: controller: unknown_entry\nerror: controller: error_entry\n\n"
	for _, tc := range []struct {
		level           string
		present, absent []string
		blank           bool
	}{
		{"info", []string{"vendor_entry", "info_entry", "warn_entry", "unknown_entry", "error_entry"}, []string{"debug_entry"}, true},
		{"warn", []string{"warn_entry", "unknown_entry", "error_entry"}, []string{"debug_entry", "vendor_entry", "info_entry"}, false},
		{"error", []string{"unknown_entry", "error_entry"}, []string{"debug_entry", "vendor_entry", "info_entry", "warn_entry"}, false},
	} {
		t.Run(tc.level, func(t *testing.T) {
			var out bytes.Buffer
			view := newSpaceLogOutput(&out, spaceLogSettings(t, tc.level))
			if n, err := view.Write([]byte(input)); err != nil || n != len(input) {
				t.Fatal(n, err)
			}
			if err := view.Close(); err != nil {
				t.Fatal(err)
			}
			for _, text := range tc.present {
				if !strings.Contains(out.String(), text) {
					t.Fatalf("missing %s: %q", text, out.String())
				}
			}
			for _, text := range tc.absent {
				if strings.Contains(out.String(), text) {
					t.Fatalf("unexpected %s: %q", text, out.String())
				}
			}
			if got := strings.Contains(out.String(), "\n\n"); got != tc.blank {
				t.Fatalf("blank line visibility=%v: %q", got, out.String())
			}
		})
	}
}
