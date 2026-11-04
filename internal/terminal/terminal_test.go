package terminal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestArtifactManifest(t *testing.T) {
	data, err := os.ReadFile("wasm/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Artifact string `json:"artifact_sha256"`
		Bridge   string `json:"bridge_sha256"`
		Patch    string `json:"patch_sha256"`
		ABI      int    `json:"abi"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	bridge, err := os.ReadFile("wasm/bridge.zig")
	if err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile("wasm/native.patch")
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]struct {
		data []byte
		want string
	}{"artifact": {artifact, manifest.Artifact}, "bridge": {bridge, manifest.Bridge}, "patch": {patch, manifest.Patch}} {
		sum := sha256.Sum256(check.data)
		if hex.EncodeToString(sum[:]) != check.want {
			t.Errorf("%s changed without rebuilding its pinned manifest", name)
		}
	}
	if manifest.ABI != bridgeABI {
		t.Fatal("unexpected bridge ABI")
	}
}

func newTestTerminal(t *testing.T, rows, cols, history int, out *bytes.Buffer) *Terminal {
	t.Helper()
	vt, err := New(rows, cols, history, func(p []byte) {
		if out != nil {
			_, _ = out.Write(p)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vt.Close() })
	return vt
}

func TestCoreRowsHistoryAndModes(t *testing.T) {
	var replies bytes.Buffer
	vt := newTestTerminal(t, 3, 12, 4, &replies)
	_, err := vt.Write([]byte("\x1b[31mold\r\nline\r\nnew\r\n\x1b[?2004h\x1b[>3u\x1b[?u"))
	if err != nil {
		t.Fatal(err)
	}
	if !vt.Mode(2004) || vt.Get(8) != 3 || replies.String() != "\x1b[?3u" {
		t.Fatalf("mode/keyboard state: %d, %q", vt.Get(8), replies.String())
	}
	lines, err := vt.Lines(false, true, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "line|new|" {
		t.Fatalf("grid = %q", got)
	}
	history, err := vt.Lines(true, true, 0, vt.Get(9))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(history, "|"); got != "old" {
		t.Fatalf("history = %q", got)
	}
	styled, err := vt.Lines(false, false, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(styled[0], "\x1b[38;5;1m") {
		t.Fatalf("styled frame = %q", styled)
	}
}

// Erased cells are unstyled. Renderers erase runs of blanks (ECH) after
// styled text; the exported row must not carry that style into them.
func TestErasedCellsDoNotInheritStyle(t *testing.T) {
	vt := newTestTerminal(t, 2, 24, 0, nil)
	_, err := vt.Write([]byte("\x1b[1;4;38;5;116m:8080\x1b[m\x1b[8X\x1b[1;14Hgrants\r\n" +
		"\x1b[44mbg\x1b[m\x1b[4X\x1b[2;7Hx"))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := vt.Lines(false, false, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i, styled := range []string{":8080", "bg"} {
		if _, rest, _ := strings.Cut(lines[i], styled); !strings.HasPrefix(rest, "\x1b[0m ") {
			t.Errorf("row %d = %q: erased cells inherit the style of %q", i, lines[i], styled)
		}
	}
}

func TestResetKeepsGraphemeDefaultAndInstancesIsolated(t *testing.T) {
	a := newTestTerminal(t, 3, 20, 2, nil)
	b := newTestTerminal(t, 3, 20, 2, nil)
	_, _ = a.Write([]byte("\x1b[?2027l\x1b[?2004h\x1bc👩‍💻X"))
	if a.Get(3) != 3 || !a.Mode(2027) || a.Mode(2004) {
		t.Fatal("RIS did not restore configured defaults")
	}
	if b.Get(3) != 0 || b.Mode(2004) {
		t.Fatal("state leaked across terminal instances")
	}
}

func TestCoreRejectsInvalidGeometryAndCloses(t *testing.T) {
	if _, err := New(65535, 65535, 100, nil); err == nil {
		t.Fatal("unbounded geometry accepted")
	}
	vt := newTestTerminal(t, 3, 20, 2, nil)
	if err := vt.Resize(0, 20); err == nil {
		t.Fatal("zero height accepted")
	}
	if err := vt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := vt.Write([]byte("closed")); err == nil {
		t.Fatal("write after close accepted")
	}
}
