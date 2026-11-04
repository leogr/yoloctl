package term

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// This strict acceptance corpus runs in the ordinary regression gate. No
// failures are skipped or blessed as goldens. Run independently with:
// go test -run TestTerminalFidelity ./internal/tui/term
// Sources and the limits of this corpus are in testdata/README.md.
type fidelityTerminal interface {
	io.Writer
	Resize(rows, cols int)
	GridText() string
	CursorPos() (int, int, bool)
	Rows() int
	IsAlt() bool
}

// Candidate engines can replace this factory in a Go overlay. They receive the
// same hand-authored oracle and chunk schedules as the production adapter.
var fidelityFactory = func(rows, cols int, replies io.Writer) fidelityTerminal {
	return newEmulator(rows, cols, replies, 100)
}

type fidelityCase struct {
	ID, Profile, Source string
	Rows, Cols          int
	Steps               []struct {
		Write  string
		Resize []int // rows, columns
	}
	Want struct {
		Cursor  []int // column, row
		Lines   map[string]string
		Alt     *bool
		Replies *string
	}
}

func TestTerminalFidelity(t *testing.T) {
	data, err := os.ReadFile("testdata/fidelity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []fidelityCase
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if tc.ID == "" || seen[tc.ID] || tc.Profile == "" || tc.Source == "" || tc.Rows < 1 || tc.Cols < 1 || len(tc.Steps) == 0 {
			t.Fatalf("invalid or duplicate corpus case: %+v", tc)
		}
		seen[tc.ID] = true
		t.Run(tc.ID, func(t *testing.T) {
			// The same assertion must survive arbitrary PTY read boundaries.
			// Individual byte writes also split UTF-8, CSI parameters and ST.
			for _, chunks := range []int{0, 1, 2, 3, 7} {
				t.Run(fmt.Sprintf("chunk-%d", chunks), func(t *testing.T) {
					defer func() {
						if p := recover(); p != nil {
							t.Fatalf("terminal panicked: %v", p)
						}
					}()
					var replies bytes.Buffer
					e := fidelityFactory(tc.Rows, tc.Cols, &replies)
					if closer, ok := e.(io.Closer); ok {
						t.Cleanup(func() { _ = closer.Close() })
					}
					rows, cols := tc.Rows, tc.Cols
					for _, step := range tc.Steps {
						if step.Resize != nil {
							if len(step.Resize) != 2 || step.Resize[0] < 1 || step.Resize[1] < 1 {
								t.Fatal("invalid resize in corpus")
							}
							rows, cols = step.Resize[0], step.Resize[1]
							e.Resize(rows, cols)
						}
						p := []byte(step.Write)
						for len(p) > 0 {
							n := len(p)
							if chunks > 0 {
								n = min(n, chunks)
							}
							if written, err := e.Write(p[:n]); err != nil || written != n {
								t.Fatalf("Write: %d/%d, %v", written, n, err)
							}
							p = p[n:]
						}
					}
					x, y, _ := e.CursorPos()
					if x < 0 || x >= cols || y < 0 || y >= rows || e.Rows() != rows {
						t.Errorf("geometry: cursor=(%d,%d), height=%d, bounds=%dx%d", x, y, e.Rows(), cols, rows)
					}
					if tc.Want.Cursor != nil && (len(tc.Want.Cursor) != 2 || tc.Want.Cursor[0] != x || tc.Want.Cursor[1] != y) {
						t.Errorf("cursor=(%d,%d), want %v", x, y, tc.Want.Cursor)
					}
					lines := strings.Split(strings.TrimSuffix(e.GridText(), "\n"), "\n")
					for row, want := range tc.Want.Lines {
						i, err := strconv.Atoi(row)
						if err != nil || i < 0 || i >= rows {
							t.Fatalf("invalid expected row %q", row)
						}
						if i >= len(lines) || lines[i] != want {
							t.Errorf("row %d: grid=%q, want %q", i, lines, want)
						}
					}
					if tc.Want.Alt != nil && e.IsAlt() != *tc.Want.Alt {
						t.Errorf("alternate screen=%v, want %v", e.IsAlt(), *tc.Want.Alt)
					}
					if tc.Want.Replies != nil && replies.String() != *tc.Want.Replies {
						t.Errorf("replies=%q, want %q", replies.String(), *tc.Want.Replies)
					}
				})
			}
		})
	}
}
