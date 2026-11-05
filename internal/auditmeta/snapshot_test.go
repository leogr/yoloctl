package auditmeta

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSnapshotProtocolAndBounds(t *testing.T) {
	record := Record{ID: strings.Repeat("a", 64), Box: "worker", Space: "work", Account: "none", Agent: "codex", RuntimeMode: "confined"}
	raw, err := Encode(strings.Repeat("b", 64), time.Unix(100, 0), []Record{record})
	if err != nil {
		t.Fatal(err)
	}
	_, here, _, _ := runtime.Caller(0)
	golden, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "third_party", "audit-plugin", "testdata", "snapshot.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, golden) {
		t.Fatal("Go snapshot does not match the C++ parser's shared fixture", string(raw))
	}
	if _, err := Encode(strings.Repeat("b", 64), time.Unix(100, 0), []Record{record, record}); err == nil {
		t.Fatal("duplicate container identity")
	}
	record.ID = strings.Repeat("a", 12)
	if _, err := Encode(strings.Repeat("b", 64), time.Unix(100, 0), []Record{record}); err == nil {
		t.Fatal("accepted truncated runtime identity")
	}
	record.ID = strings.Repeat("a", 64)
	record.Account = "SECRET\nVALUE"
	if _, err := Encode(strings.Repeat("b", 64), time.Unix(100, 0), []Record{record}); err == nil {
		t.Fatal("accepted invalid account identity")
	}
	if _, err := Encode(strings.Repeat("b", 64), time.Unix(100, 0), make([]Record, MaxRecords+1)); err == nil {
		t.Fatal("unbounded record count")
	}
}
