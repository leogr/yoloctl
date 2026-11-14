package main

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// The fixture is real Firecracker v1.17.0 console output (an arm64 step-0
// boot): logger lines of the main and vCPU threads and kernel console lines,
// with the serial console's carriage returns.
func TestFirecrackerSpaceLogRecords(t *testing.T) {
	raw, err := os.ReadFile("testdata/firecracker-vm-console.log")
	if err != nil {
		t.Fatal(err)
	}
	parse := newFirecrackerSpaceLogRecord()
	var records []slog.Record
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		records = append(records, parse(line))
	}
	first := records[0]
	want := time.Date(2026, 11, 13, 16, 46, 51, 417349108, time.Local)
	if first.Message != "Running Firecracker v1.17.0" || !first.Time.Equal(want) || first.Level != slog.LevelInfo ||
		recordAttr(first, prettylog.KeyActor) != logging.VM || recordAttr(first, prettylog.KeyExecutor) != logging.Firecracker {
		t.Fatalf("first record %v %q %v", first.Time, first.Message, first.Level)
	}
	if got := records[10].Message; got != "[Block:rootfs] notifying queues" {
		t.Fatalf("vCPU thread record %q", got)
	}
	kernel := records[6]
	if kernel.Message != "[0.000000] Booting Linux on physical CPU 0x0000000000 [0x610f0000]" ||
		recordAttr(kernel, prettylog.KeyExecutor) != "kernel" || !kernel.Time.Equal(records[5].Time) {
		t.Fatalf("kernel record %v %q", kernel.Time, kernel.Message)
	}
	for i, r := range records {
		if strings.Contains(r.Message, "\r") {
			t.Fatalf("record %d keeps a carriage return: %q", i, r.Message)
		}
	}
}

// Only the identified formats are labelled: a logger line with a level takes
// it, yoloctl's own records keep their grammar, and anything else, a marker
// included, is the generic line at the time of the last stamped line.
func TestFirecrackerSpaceLogOtherLines(t *testing.T) {
	parse := newFirecrackerSpaceLogRecord()
	if r := parse("2026-11-13T16:46:51.000000001 [vm:fc_api:WARN:src/vmm/src/rpc.rs:12] slow request"); r.Level != slog.LevelWarn || r.Message != "slow request" {
		t.Fatalf("leveled line %v %q", r.Level, r.Message)
	}
	if r := parse("2026-11-13T16:46:51.000000001 [vm:main:LOUD] text"); recordAttr(r, prettylog.KeyExecutor) == logging.Firecracker {
		t.Fatalf("an unknown level was read as a logger line: %q", r.Message)
	}
	own := parse("2026-11-13T15:46:52.5Z warning: vm/firecracker: Required host share exited")
	if own.Level != slog.LevelWarn || own.Message != "Required host share exited" || recordAttr(own, prettylog.KeyActor) != logging.VM {
		t.Fatalf("own record %v %q", own.Level, own.Message)
	}
	marker := parse("YOLOCTL_VM_READY landlock_abi=6\r")
	if marker.Message != "YOLOCTL_VM_READY landlock_abi=6" || !marker.Time.Equal(own.Time) || recordAttr(marker, prettylog.KeyActor) != "" {
		t.Fatalf("marker %v %q", marker.Time, marker.Message)
	}
}

// The vm.log view renders through the Firecracker adapter; debug log level
// still shows the original bytes.
func TestFirecrackerSpaceLogView(t *testing.T) {
	raw, err := os.ReadFile("testdata/firecracker-vm-console.log")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	view := newSpaceLogOutput(&out, spaceLogSettings(t, "info"), "/state/vm.log")
	if _, err := view.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "vm/firecracker: Running Firecracker v1.17.0") || !strings.Contains(text, "vm/kernel: [0.000000] random: crng init done") ||
		strings.Contains(text, "anonymous-instance") || strings.Contains(text, `\x0d`) {
		t.Fatalf("view:\n%s", text)
	}
	out.Reset()
	view = newSpaceLogOutput(&out, spaceLogSettings(t, "debug"), "/state/vm.log")
	if _, err := view.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatal("debug changed the bytes")
	}
}
