package admit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestReviewedFixturesAreAdmittedAndOthersRejected(t *testing.T) {
	for dir, want := range map[string]bool{"fixtures": true, "rejected": false} {
		files, err := filepath.Glob("../testdata/" + dir + "/*.mmd")
		if err != nil || len(files) == 0 {
			t.Fatal(dir, err)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if d := Check(string(b)); d.Admitted() != want || d.Kind == Unknown && want {
				t.Errorf("%s: %+v", f, d)
			}
		}
	}
}

func TestAcceptAndRejectTable(t *testing.T) {
	for _, tc := range []struct {
		source string
		reason string // "" = admitted
	}{
		// note placement (deep QA D1/D2): a note goes beside the message before it;
		// a block's frame spans its messages' participants
		{"sequenceDiagram\nA->>B: m\nalt x\nA->>B: n\nend\nNote right of B: after", "line 6: a note right after a block's start, else, and or end is drawn in the wrong place; put a message before it"},
		{"sequenceDiagram\nA->>B: m\nalt x\nA->>B: n\nend\nNote over A,B: after", "line 6: a note right after a block's start, else, and or end is drawn in the wrong place; put a message before it"},
		{"sequenceDiagram\nA->>B: m\nalt x\nNote over A,B: first\nA->>B: n\nend", "line 4: a note right after a block's start, else, and or end is drawn in the wrong place; put a message before it"},
		{"sequenceDiagram\nalt x\nA->>B: n\nelse y\nNote over A,B: n\nA->>B: o\nend", "line 5: a note right after a block's start, else, and or end is drawn in the wrong place; put a message before it"},
		{"sequenceDiagram\nloop x\nalt y\nA->>B: n\nend\nA->>B: o\nend\nNote over A: after", "line 8: a note right after a block's start, else, and or end is drawn in the wrong place; put a message before it"},
		{"sequenceDiagram\nalt x\nA->>B: n\nend\nB->>A: o\nNote right of B: fine", ""},
		{"sequenceDiagram\nNote over A: first\nA->>B: m", ""},
		{"sequenceDiagram\nA->>B: m\nNote right of B: one\nNote left of A: two", ""},
		{"sequenceDiagram\nalt x\nA->>B: n\nNote left of A: l\nend", "line 4: a note left of a block's first participant crosses the block's frame"},
		{"sequenceDiagram\nalt x\nA->>B: n\nNote right of B: r\nend", "line 4: a note right of a block's last participant crosses the block's frame"},
		// interior side notes stay: the frame is A–D
		{"sequenceDiagram\nparticipant A\nparticipant B\nparticipant C\nparticipant D\nalt x\nA->>D: n\nB->>C: m\nNote right of B: hi\nNote left of C: ho\nend", ""},
		{"sequenceDiagram\nparticipant A\nparticipant B\nparticipant C\nalt x\nB->>C: n\nNote left of A: l\nend", "line 7: a note left of a block's first participant crosses the block's frame"},
		{"sequenceDiagram\nparticipant A\nparticipant B\nparticipant C\nalt x\nB->>C: n\nNote over A: o\nend", "line 7: a note inside a block over a participant without messages in that block is drawn outside it"},
		{"sequenceDiagram\nparticipant A\nparticipant B\nparticipant C\nalt x\nB->>C: n\nNote over B,C: o\nend", ""},
		// a block's span includes its nested blocks' messages, before and after the note
		{"sequenceDiagram\nparticipant A\nparticipant B\nparticipant C\nloop x\nA->>B: n\nNote over C: o\nalt y\nB->>C: m\nend\nend", ""},
		{"sequenceDiagram\nparticipant A\nparticipant B\nparticipant C\nloop x\nA->>C: n\nalt y\nA->>B: m\nNote over C: o\nend\nend", "line 9: a note inside a block over a participant without messages in that block is drawn outside it"},
		// flowchart
		{"graph TD\nA-->B", ""},
		{"flowchart TB\n  A[x] --> B{y} --> C(z)", ""},
		{"flowchart LR\nA[\"quoted ] [ ok\"] --> B", ""},
		{"flowchart LR\nA -- label text --> B", ""},
		{"flowchart LR\nA -->|why| B\nA -.-> C\nA ==> D\nA --- E", ""},
		{"flowchart LR\n%% comment\nA & B --> C & D;", ""},
		{"flowchart LR\nsubgraph one\nA\nend\nsubgraph two words\nB\nend\nsubgraph t[Three]\nC\nend", ""},
		{"flowchart LR\nsubgraph \"Two\"\nB\nend", "line 2: quoted subgraph titles are not supported"},
		{"flowchart LR\nsubgraph t[\"Three\"]\nB\nend", "line 2: quoted subgraph titles are not supported"},
		{"flowchart LR\n\tA --> B", "contains control characters"}, // TAB: the renderer's protocol refuses it
		{"flowchart RL\nA-->B", "direction RL is not supported"},
		{"flowchart BT\nA-->B", "direction BT is not supported"},
		{"flowchart\nA-->B", "flowchart needs one direction (TD, TB or LR)"},
		{"flowchart LR\nA([stadium])", "line 2: unsupported node shape"},
		{"flowchart LR\nA[(db)]", "line 2: unsupported node shape"},
		{"flowchart LR\nA((c))", "line 2: unsupported node shape"},
		{"flowchart LR\nA[[sub]]", "line 2: unsupported node shape"},
		{"flowchart LR\nA{{hex}}", "line 2: unsupported node shape"},
		{"flowchart LR\nA>flag]", "line 2: unsupported node style or shape"},
		{"flowchart LR\nA:::hot --> B", "line 2: unsupported node style or shape"},
		{"flowchart LR\nA --o B", "line 2: unsupported edge"},
		{"flowchart LR\nA --x B", "line 2: unsupported edge"},
		{"flowchart LR\nA <--> B", "line 2: unsupported edge"},
		{"flowchart LR\nA ---> B", "line 2: unsupported edge"},
		{"flowchart LR\nstyle A fill:#f00", "line 2: style is not supported"},
		{"flowchart LR\nclick A call()", "line 2: click is not supported"},
		{"flowchart LR\nsubgraph x\nA", "subgraph without end"},
		{"flowchart LR\nend", "line 2: end without subgraph"},
		// sequence
		{"sequenceDiagram\nA->>B: hi\nB-->>A: ok\nA->B: x\nB-->A: y", ""},
		{"sequenceDiagram\nparticipant A as Alice\nactor B\nA->>+B: go\nB-->>-A: done", ""},
		{"sequenceDiagram\nNote left of A: x\nNote right of A: y\nNote over A,B: z", ""},
		{"sequenceDiagram\nalt a\nA->>B: x\nelse b\nA->>B: y\nend\nloop l\nA->>B: z\nend", ""},
		{"sequenceDiagram\nactivate A\ndeactivate A\nautonumber", ""},
		{"sequenceDiagram\nA-xB: lost", "line 2: unsupported arrow"},
		{"sequenceDiagram\nA--xB: lost", "line 2: unsupported arrow"},
		{"sequenceDiagram\nA-)B: async", "line 2: unsupported arrow"},
		{"sequenceDiagram\nA->>B", "line 2: message needs a text"},
		{"sequenceDiagram\nNote over A,B,C: x", "line 2: unsupported note"},
		{"sequenceDiagram\nNote left of A,B: x", "line 2: unsupported note"},
		{"sequenceDiagram\nelse x", "line 2: else outside a block"},
		{"sequenceDiagram\nloop x\nA->>B: y", "block without end"},
		{"sequenceDiagram\nrect rgb(0,0,0)\nend", "line 2: unsupported statement"},
		{"sequenceDiagram\nbox Aqua\nend", "line 2: unsupported statement"},
		// general
		{"classDiagram\nclass A", "only flowchart and sequence diagrams are drawn"},
		{"%% only a comment", "empty diagram"},
		{";", "empty diagram"},
		{"  ;  \n%% x\n ; ", "empty diagram"},
		{"", "empty diagram"},
		{"flowchart TD;\nA-->B;", ""},

		{"graph TD\nA[\x1b[31mred] --> B", "contains control characters"},
		{"graph TD\r\nA --> B", "contains control characters"},
		{"graph TD\nA[\xff] --> B", "not valid UTF-8"},
	} {
		d := Check(tc.source)
		if d.Reason != tc.reason {
			t.Errorf("%q: got %q, want %q", tc.source, d.Reason, tc.reason)
		}
	}
}

// Admission sets no size, count, label-length or nesting limit: the
// worker's memory and time limits bound what a diagram may cost.
func TestAdmissionSetsNoSizeLimits(t *testing.T) {
	var big strings.Builder
	big.WriteString("flowchart TD\n")
	for i := range 3000 { // well over 8 KiB
		fmt.Fprintf(&big, "N%d --> N%d\n", i, i+1)
	}
	var seq strings.Builder
	seq.WriteString("sequenceDiagram\n")
	for i := range 300 {
		fmt.Fprintf(&seq, "P%d->>P%d: %s\n", i%40, (i+1)%40, strings.Repeat("x", 3))
	}
	for _, source := range []string{
		big.String(),
		seq.String(),
		"flowchart TD\n" + strings.Repeat("a", 500) + " --> B[" + strings.Repeat("long ", 100) + "]",
		"sequenceDiagram\nparticipant " + strings.Repeat("p", 500) + "\n" + strings.Repeat("loop x\n", 8) + strings.Repeat("end\n", 8),
	} {
		if d := Check(source); !d.Admitted() {
			t.Errorf("size or count refused: %q", d.Reason)
		}
	}
}

// Validation never rewrites: shapes and actors stay in the source the
// renderer receives (Check has no output besides its decision).
func TestValidationKeepsTheSourceUnchanged(t *testing.T) {
	source := "sequenceDiagram\n    actor U as User\n    U->>A: hi\n"
	before := strings.Clone(source)
	if d := Check(source); !d.Admitted() || source != before {
		t.Fatal(d)
	}
}

// Admission never panics, is deterministic, and admits only a known kind of
// valid UTF-8 source without control characters other than LF.
func FuzzAdmission(f *testing.F) {
	for _, seed := range []string{
		"flowchart LR\n A --> B\n", "graph TD\n A{x} -- y --> B(z)\n subgraph s[t]\n C\n end\n",
		"sequenceDiagram\n actor A as U\n A->>+B: hi\n alt x\n Note over A,B: n\n else y\n end\n",
		";", "%%\n", "flowchart LR\n A[\"a\"] -.->|l| B & C ==> D\n", "sequenceDiagram\n A-xB: no\n", "\xff",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		d := Check(source)
		if again := Check(source); again != d {
			t.Fatalf("not deterministic: %+v then %+v", d, again)
		}
		if !d.Admitted() {
			return
		}
		if d.Kind == Unknown || !utf8.ValidString(source) {
			t.Fatalf("admitted %+v for %q", d, source)
		}
		for _, r := range source {
			if r != '\n' && unicode.IsControl(r) {
				t.Fatalf("admitted a control character in %q", source)
			}
		}
	})
}

// Admission is one pass over the statements, also for deep nesting: a
// large nested sequence is checked in time linear in its size.
func BenchmarkCheckNestedSequence(b *testing.B) {
	var src strings.Builder
	src.WriteString("sequenceDiagram\nparticipant A\nparticipant B\n")
	for range 2000 {
		src.WriteString("loop x\nA->>B: m\n")
	}
	for range 20000 {
		src.WriteString("A->>B: m\n")
	}
	for range 2000 {
		src.WriteString("end\n")
	}
	source := src.String()
	b.SetBytes(int64(len(source)))
	for b.Loop() {
		if d := Check(source); !d.Admitted() {
			b.Fatal(d.Reason)
		}
	}
}
