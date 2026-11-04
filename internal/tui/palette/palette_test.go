package palette

import (
	"slices"
	"strings"
	"testing"
)

func TestSingleLineInput(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"trailing newline", "box ls\n", "box ls "},
		{"multiline whitespace", "box\tcreate\r\nexample\v\f\u0085\u2028\u2029", "box create  example     "},
		{"terminal controls", "a\x00\a\b\x1b[2J\x7f\u009bb", "a[2Jb"},
		{"unicode text", "é e\u0301 界 👩\u200d💻", "é e\u0301 界 👩\u200d💻"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m Model
			m.Insert("az")
			m.Left()
			m.Insert(tc.input)
			if line, cur := m.Line(); line != "a"+tc.want+"z" || cur != 1+len([]rune(tc.want)) {
				t.Fatalf("insert = %q at %d, want %q at %d", line, cur, "a"+tc.want+"z", 1+len([]rune(tc.want)))
			}
			// Completion and history replacement use the same invariant.
			m.set("")
			m.ApplyCompletion(tc.input)
			if line, _ := m.Line(); line != tc.want {
				t.Fatalf("completion = %q, want %q", line, tc.want)
			}
			if got := m.Submit(); !slices.Equal(got, strings.Fields(tc.want)) {
				t.Fatalf("submit = %q, want whitespace-split %q", got, tc.want)
			}
			m.HistPrev()
			if line, _ := m.Line(); line != strings.TrimSpace(tc.want) {
				t.Fatalf("history = %q, want %q", line, strings.TrimSpace(tc.want))
			}
		})
	}
}

func TestEditSubmitVerbatim(t *testing.T) {
	var m Model
	m.Insert("run  --agent claude")
	m.Left()
	m.Left() // cursor inside "claude"
	m.Insert("X")
	line, cur := m.Line()
	if line != "run  --agent clauXde" || cur != len([]rune(line))-2 {
		t.Fatalf("edit state = %q cur=%d", line, cur)
	}
	m.Backspace()
	got := m.Submit()
	// Whitespace-split only — no quoting/expansion, input not rewritten.
	if !slices.Equal(got, []string{"run", "--agent", "claude"}) {
		t.Fatalf("Submit = %v", got)
	}
	if line, _ := m.Line(); line != "" {
		t.Fatalf("buffer not cleared: %q", line)
	}
}

func TestSubmitEmptyIsNil(t *testing.T) {
	var m Model
	m.Insert("   ")
	if got := m.Submit(); got != nil {
		t.Fatalf("Submit(blank) = %v, want nil", got)
	}
}

func TestCompletionRoundTrip(t *testing.T) {
	var m Model
	m.Insert("box att")
	words, last, ok := m.CompletionInput()
	if !ok || !slices.Equal(words, []string{"box"}) || last != "att" {
		t.Fatalf("CompletionInput = %v %q %v", words, last, ok)
	}
	m.ApplyCompletion("attach ")
	if line, _ := m.Line(); line != "box attach " {
		t.Fatalf("after apply: %q", line)
	}
	// Trailing space → completing a fresh empty token.
	words, last, ok = m.CompletionInput()
	if !ok || len(words) != 2 || last != "" {
		t.Fatalf("fresh-token input = %v %q %v", words, last, ok)
	}
	// Mid-line cursor: completion declines.
	m.Left()
	if _, _, ok := m.CompletionInput(); ok {
		t.Fatal("mid-line completion should be refused")
	}
}

func TestDelete(t *testing.T) {
	var m Model
	m.Insert("abc")
	m.Home() // cursor at 0
	m.Delete()
	if line, cur := m.Line(); line != "bc" || cur != 0 {
		t.Fatalf("Delete at start = %q cur=%d, want \"bc\" cur=0", line, cur)
	}
	m.Right() // cursor between b and c
	m.Delete()
	if line, cur := m.Line(); line != "b" || cur != 1 {
		t.Fatalf("Delete mid = %q cur=%d, want \"b\" cur=1", line, cur)
	}
	m.End() // cursor at end
	m.Delete()
	if line, _ := m.Line(); line != "b" {
		t.Fatalf("Delete at end = %q, want \"b\" (no-op)", line)
	}
}

func TestHistory(t *testing.T) {
	var m Model
	m.Insert("space ls")
	m.Submit()
	m.Insert("agentbox ls")
	m.Submit()
	m.HistPrev()
	if line, _ := m.Line(); line != "agentbox ls" {
		t.Fatalf("HistPrev 1 = %q", line)
	}
	m.HistPrev()
	if line, _ := m.Line(); line != "space ls" {
		t.Fatalf("HistPrev 2 = %q", line)
	}
	m.HistNext()
	m.HistNext()
	if line, _ := m.Line(); line != "" {
		t.Fatalf("HistNext past end = %q, want empty", line)
	}
}
