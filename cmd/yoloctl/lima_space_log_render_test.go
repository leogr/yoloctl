package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

const limaEventTime = "2026-11-13T09:03:50.602885-07:00"

func limaEvent(status string) string {
	return `{"time":"` + limaEventTime + `","status":` + status + `}`
}

func TestLimaSpaceLogRecords(t *testing.T) {
	for _, tc := range []struct {
		name, status, message string
		level                 slog.Level
	}{
		{"screenshot-forward", `{"sshLocalPort":53564,"portForward":{"type":"forwarding","protocol":"tcp","guestAddr":"127.0.0.1:36805","hostAddr":"127.0.0.1:36805"}}`, "Forwarding tcp 127.0.0.1:36805 → 127.0.0.1:36805", slog.LevelInfo},
		{"not-forwarded", `{"portForward":{"type":"not-forwarding","protocol":"tcp","guestAddr":"[::]:22"}}`, "Not forwarding tcp [::]:22", slog.LevelInfo},
		{"stopping", `{"running":true,"portForward":{"type":"stopping","protocol":"udp","guestAddr":"0.0.0.0:39033","hostAddr":"127.0.0.1:39033"}}`, "Stopping udp forward 127.0.0.1:39033 → 0.0.0.0:39033", slog.LevelInfo},
		{"port-failed", `{"portForward":{"type":"failed","protocol":"tcp","guestAddr":"127.0.0.1:80","hostAddr":"127.0.0.1:80","error":"bind denied"}}`, "Port forward failed tcp 127.0.0.1:80 → 127.0.0.1:80: bind denied", slog.LevelWarn},
		{"screenshot-vsock", `{"vsock":{"type":"failed","reason":"SSH server does not seem to be running on vsock port"}}`, "Vsock forwarding failed: SSH server does not seem to be running on vsock port", slog.LevelWarn},
		{"vsock-started", `{"vsock":{"type":"started","hostAddr":"/tmp/ssh.sock","vsockPort":22}}`, "Vsock forwarding started /tmp/ssh.sock → port 22", slog.LevelInfo},
		{"vsock-skipped", `{"vsock":{"type":"skipped","reason":"not configured"}}`, "Vsock forwarding skipped: not configured", slog.LevelInfo},
		{"ssh", `{"sshLocalPort":53564}`, "SSH local port 53564", slog.LevelInfo},
		{"running", `{"running":true,"sshLocalPort":53564}`, "VM running; SSH local port 53564", slog.LevelInfo},
		{"degraded", `{"running":true,"degraded":true,"errors":["first failure","second failure"]}`, "VM running (degraded); Errors: first failure; second failure", slog.LevelError},
		{"exiting", `{"exiting":true}`, "VM exiting", slog.LevelInfo},
		{"cloud-line", `{"cloudInitProgress":{"active":true,"logLine":"installing tools"}}`, "Cloud-init: installing tools", slog.LevelInfo},
		{"cloud-active", `{"cloudInitProgress":{"active":true}}`, "Cloud-init monitoring active", slog.LevelInfo},
		{"cloud-done", `{"cloudInitProgress":{"completed":true,"logLine":"done"}}`, "Cloud-init completed: done", slog.LevelInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := limaSpaceLogRecord(limaEvent(tc.status))
			stamp, _ := time.Parse(time.RFC3339Nano, limaEventTime)
			if r.Message != tc.message || r.Level != tc.level || !r.Time.Equal(stamp) || recordAttr(r, prettylog.KeyActor) != "vm" || recordAttr(r, prettylog.KeyExecutor) != "lima" || prettylog.KindOf(r) == prettylog.KindUnknown {
				t.Fatalf("record=%+v actor=%q executor=%q", r, recordAttr(r, prettylog.KeyActor), recordAttr(r, prettylog.KeyExecutor))
			}
		})
	}
}

func TestLimaSpaceLogUnknownRecordsStayVisible(t *testing.T) {
	for _, raw := range []string{
		`{"time":"bad","status":{"running":true}}`,
		`{"time":"` + limaEventTime + `","status":null}`,
		limaEvent(`{}`),
		limaEvent(`{"running":"true"}`),
		limaEvent(`{"running":true,"future":"retained"}`),
		limaEvent(`{"portForward":{"type":"new-kind","error":"retained"}}`),
		limaEvent(`{"portForward":{"type":"forwarding","future":"retained"}}`),
		limaEvent(`{"vsock":{"type":"new-kind","reason":"retained"}}`),
		limaEvent(`{"cloudInitProgress":{}}`),
		limaEvent(`{"running":true}`) + ` {"extra":1}`,
		`{"time":"` + limaEventTime + `","status":{"running":true},"future":1}`,
		`{"time":"2026-11-11","msg":"vendor output"}`,
		`YOLOCTL_VM_READY:unchanged`,
		strings.Replace(limaEvent(`{"cloudInitProgress":{"logLine":"invalid"}}`), "invalid", "\xff", 1),
	} {
		if got := limaSpaceLogRecord(raw); got.Message != raw {
			t.Errorf("fallback %q became %q", raw, got.Message)
		}
	}
}

func TestLimaSpaceLogViewSourceAndRawIsolation(t *testing.T) {
	raw := limaEvent(`{"running":true}`) + "\n"
	for _, path := range []string{"/tmp/vm.log", "/tmp/ha.stdout.log"} {
		var out bytes.Buffer
		view := newSpaceLogOutput(&out, spaceLogSettings(t, "info"), path)
		for _, part := range []string{raw[:11], raw[11:]} {
			if _, err := view.Write([]byte(part)); err != nil {
				t.Fatal(err)
			}
		}
		if err := view.Close(); err != nil {
			t.Fatal(err)
		}
		text := out.String()
		if path == "/tmp/ha.stdout.log" {
			if !strings.Contains(text, "host>vm: lima: VM running") || strings.Contains(text, `"status"`) {
				t.Fatalf("Lima view %q", text)
			}
		} else if !strings.Contains(text, strings.TrimSuffix(raw, "\n")) {
			t.Fatalf("non-Lima bytes interpreted: %q", text)
		}
	}
	var out bytes.Buffer
	view := newSpaceLogOutput(&out, spaceLogSettings(t, "debug"), "/tmp/ha.stdout.log")
	original := []byte(raw + "raw\x00\x1b[31m\r\npartial")
	if _, err := view.Write(original); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Fatalf("debug changed bytes: %q", out.Bytes())
	}
}

func TestLimaSpaceLogViewFiltersAndFolds(t *testing.T) {
	info := limaEvent(`{"running":true}`) + "\n"
	warn := limaEvent(`{"vsock":{"type":"failed","reason":"vsock unavailable"}}`) + "\n"
	failure := limaEvent(`{"errors":["boot failure"]}`) + "\n"
	var out bytes.Buffer
	view := newSpaceLogOutput(&out, spaceLogSettings(t, "warn"), "ha.stdout.log")
	if _, err := view.Write([]byte(info + warn + warn + warn + failure)); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "VM running") || strings.Count(text, "Vsock forwarding failed") != 1 || !strings.Contains(text, "repeated 2 more times") || !strings.Contains(text, "boot failure") {
		t.Fatalf("filtered/folded %q", text)
	}
}

func TestLimaSpaceLogViewEscapesDecodedControls(t *testing.T) {
	var out bytes.Buffer
	view := newSpaceLogOutput(&out, spaceLogSettings(t, "info"), "ha.stdout.log")
	raw := limaEvent(`{"cloudInitProgress":{"logLine":"install\u001b[31m\nnext"}}`) + "\n"
	if _, err := view.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out.String(), '\x1b') || !strings.Contains(out.String(), `\x1b[31m`) {
		t.Fatalf("control escaped incorrectly: %q", out.String())
	}
}
