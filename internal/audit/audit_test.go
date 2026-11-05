package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Writer, *time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	writer, err := OpenWriter(Store{Root: filepath.Join(t.TempDir(), "logs"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	return writer, &now
}
func testEvent(now time.Time, box string) Event {
	return Event{Time: now, Rule: "test", Severity: "Notice", Box: box, MetadataAvailable: true}
}

func TestEventPrivacyAndTerminalControls(t *testing.T) {
	raw := []byte(`{"time":"2026-11-04T22:00:00Z","rule":"test\u001b[31m","priority":"Notice","output":"SECRET_OUTPUT","output_fields":{"proc.exepath":"/bin/evil\nname","proc.args":"SECRET_ARGV","proc.env":"SECRET_ENV","evt.buffer":"SECRET_BUFFER","container.id":"0123456789abcdef","yolo.box":"worker","yolo.space":"test","yolo.agent":"none","yolo.account":"none","yolo.runtime_mode":"confined","yolo.metadata_available":true,"yolo.network_unconfined":true}}`)
	event, err := ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET_OUTPUT", "SECRET_ARGV", "SECRET_ENV", "SECRET_BUFFER"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("persisted unselected field", secret)
		}
	}
	if strings.ContainsAny(event.Rule+event.Executable, "\x1b\r\n") || !event.MetadataAvailable || event.NetworkUnconfined != "true" {
		t.Fatal(event)
	}
	for _, bad := range [][]byte{bytes.Repeat([]byte{'x'}, MaxEvent+1), []byte(`{"rule":"no time"}`), []byte(`{`)} {
		if _, err := ParseEvent(bad); err == nil {
			t.Fatal("accepted malformed event")
		}
	}
	raw = bytes.ReplaceAll(raw, []byte(`"yolo.metadata_available":true`), []byte(`"yolo.metadata_available":false`))
	event, err = ParseEvent(raw)
	if err != nil || event.Box != "" || event.Space != "" {
		t.Fatal("unknown metadata retained labels", event, err)
	}
}

func TestHistorySnapshotTailFilteringAndRestart(t *testing.T) {
	w, now := testStore(t)
	for _, box := range []string{"one", "two", "one"} {
		if err := w.Append(testEvent(*now, box)); err != nil {
			t.Fatal(err)
		}
	}
	var got []Event
	summary, err := w.Scan(Query{Limit: 1, Tail: true}, func(e Event) error { got = append(got, e); return w.Append(testEvent(*now, "later")) })
	if err != nil || summary.Upper != 3 || summary.Last != 3 || len(got) != 1 || got[0].Cursor != 3 {
		t.Fatal(summary, got, err)
	}
	got = nil
	summary, err = w.Scan(Query{After: 3}, func(e Event) error { got = append(got, e); return nil })
	if err != nil || len(got) != 1 || got[0].Box != "later" || summary.Last != 4 {
		t.Fatal(summary, got, err)
	}
	got = nil
	summary, err = w.Scan(Query{Box: "one", Limit: 1, Tail: true}, func(e Event) error { got = append(got, e); return nil })
	if err != nil || len(got) != 1 || got[0].Cursor != 3 || summary.Last != 4 {
		t.Fatal("filtered tail lost upper cursor", summary, got, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWriter(w.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.Append(testEvent(*now, "restart")); err != nil {
		t.Fatal(err)
	}
	stats, err := reopened.Counters()
	if err != nil || stats.Upper != 5 {
		t.Fatal(stats, err)
	}
	files, err := reopened.segments()
	if err != nil || len(files) != 1 {
		t.Fatal("restart fragmented the active segment", files, err)
	}
}

func TestHistoryReportsConcurrentRotationGap(t *testing.T) {
	w, now := testStore(t)
	if err := w.Append(testEvent(*now, "one")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(testEvent(*now, "two")); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Scan(Query{}, func(e Event) error {
		if e.Cursor == 1 {
			return os.Remove(filepath.Join(w.Root, fmt.Sprintf("%020d.jsonl", 2)))
		}
		return nil
	})
	if err != nil || !summary.Gap || summary.Count != 1 || summary.Last != 2 {
		t.Fatal(summary, err)
	}
}

func TestRetentionExpiresQuietAndMixedActiveRecords(t *testing.T) {
	w, now := testStore(t)
	if err := w.Append(testEvent(*now, "old")); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(RetainAge - time.Hour)
	if err := w.Append(testEvent(*now, "recent")); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	if err := w.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	var events []Event
	summary, err := w.Scan(Query{}, func(e Event) error { events = append(events, e); return nil })
	if err != nil || len(events) != 1 || events[0].Box != "recent" || !summary.Gap {
		t.Fatal(summary, events, err)
	}
	if err := w.Append(testEvent(*now, "after-prune")); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(RetainAge + time.Second)
	if err := w.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	files, err := w.segments()
	if err != nil || len(files) != 0 {
		t.Fatal("expired quiet records remain on disk", files, err)
	}
	summary, err = w.Scan(Query{}, func(Event) error { t.Fatal("exported expired data"); return nil })
	if err != nil || !summary.Gap || summary.Count != 0 {
		t.Fatal(summary, err)
	}
}

func TestRetentionTotalBytesAndDropCounters(t *testing.T) {
	w, now := testStore(t)
	w.state.Last = 1000
	for i := uint64(1); i <= 64; i++ {
		name := filepath.Join(w.Root, fmt.Sprintf("%020d.jsonl", i))
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(SegmentBytes); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Append(testEvent(*now, "new")); err != nil {
		t.Fatal(err)
	}
	files, err := w.segments()
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	if total > RetainBytes {
		t.Fatal("retention exceeds total budget", total)
	}
	if _, err := os.Stat(filepath.Join(w.Root, fmt.Sprintf("%020d.jsonl", 1))); !os.IsNotExist(err) {
		t.Fatal("oldest segment retained", err)
	}
	if err := w.Dropped(); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Counters()
	if err != nil || summary.Drops != 1 {
		t.Fatal(summary, err)
	}
}

func TestPinnedArchivesInstallVerifyAndRuleSyntax(t *testing.T) {
	archives := os.Getenv("FALCO_TEST_ARCHIVES")
	if archives == "" {
		t.Skip("set FALCO_TEST_ARCHIVES to the qualified official tarball directory")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			pin, err := Pinned(arch)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(archives, "falco-"+Version+"-"+pin.Name+".tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			tools, err := Install(root, arch, raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := pin.VerifyInstalled(tools); err != nil {
				t.Fatal(err)
			}
			if arch == runtime.GOARCH && runtime.GOOS == "linux" {
				config, err := Configuration(root, tools, filepath.Join(root, "metadata.tsv"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "falco.json"), config, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "rules.yaml"), []byte(Rules), 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(t.Context(), filepath.Join(tools, "bin", "falco"), "-c", filepath.Join(root, "falco.json"), "-V", filepath.Join(root, "rules.yaml"))
				if output, err := cmd.CombinedOutput(); err != nil || bytes.Contains(output, []byte("schema validation: failed")) {
					t.Fatalf("pinned-engine validation failed: %s %v", output, err)
				}
			}
			binary := filepath.Join(tools, "bin", "falco")
			if err := os.Chmod(binary, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binary, []byte("corrupt"), 0500); err != nil {
				t.Fatal(err)
			}
			if _, err := Install(root, arch, raw); err == nil {
				t.Fatal("reused corrupted immutable tools")
			}
		})
	}
}

func TestHistoryFixesItsTimeBoundaryBeforeStreaming(t *testing.T) {
	w, now := testStore(t)
	boundary := now.Add(-RetainAge + time.Second)
	for i := 0; i < 2; i++ {
		if err := w.Append(testEvent(boundary, "near-boundary")); err != nil {
			t.Fatal(err)
		}
	}
	original := *now
	summary, err := w.Scan(Query{}, func(Event) error { *now = now.Add(2 * time.Second); return nil })
	if err != nil || summary.Count != 2 || !summary.Until.Equal(original) {
		t.Fatal("history time boundary moved during export", summary, err)
	}
}
