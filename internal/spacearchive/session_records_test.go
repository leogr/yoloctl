package spacearchive

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
)

// Conversation records (config/state/boxes/<box>.session.yaml) are
// controller evidence about the exporting VM's launches. Export omits them,
// so an imported space starts fresh conversation tracking: no record exists
// on the new disk and the first managed attach assigns a new ID. Same-disk
// space recreate never goes through an archive and keeps them.
func TestExportOmitsConversationRecords(t *testing.T) {
	source, boot := fixture(t)
	records := filepath.Join(source, "config", "state", "boxes")
	if err := os.MkdirAll(records, 0o700); err != nil {
		t.Fatal(err)
	}
	record := "schema_version: 1\naccount: trial\nprovider: claude\n"
	if err := os.WriteFile(filepath.Join(records, "coder.session.yaml"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := readDeclarations(source, boot)
	if err != nil {
		t.Fatal("a record beside the declarations broke export:", err)
	}
	if len(bundle.Boxes) != 0 {
		t.Fatalf("the record was read as a box: %v", bundle.Boxes)
	}
	state := stateFile(t)
	if err := WriteState(t.Context(), state, source, boot); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(state)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, "session.yaml") || strings.HasPrefix(h.Name, "state/") {
			t.Fatalf("archive carries conversation state %q", h.Name)
		}
		if h.Name == "declarations.json" {
			raw, _ := io.ReadAll(tr)
			if strings.Contains(string(raw), "provider: claude") {
				t.Fatal("the declarations carry the record")
			}
		}
	}
	// Publishing the imported declarations on a fresh disk creates no record.
	target := t.TempDir()
	if _, err := controller.PublishStore(target, boot, *bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "config", "state")); !os.IsNotExist(err) {
		t.Fatalf("an import created conversation records: %v", err)
	}
}
