package yoloctlcli

import (
	"slices"
	"testing"
)

// parseCompletions mirrors cobra's `__complete` stdout: "value" or
// "value\tdescription" lines, closed by a ":N" ShellCompDirective line
// that is surfaced to the palette (path completion needs its NoSpace bit).
func TestParseCompletions(t *testing.T) {
	out := []byte("attach\tAttach to the agent session\r\n" + // CRLF tolerated
		"create\tCreate a confined agentbox\n" +
		"coder\n" + // no description (dynamic value completion)
		"\n" + // blank line ignored
		":4\n") // ShellCompDirectiveNoFileComp
	got, directive := parseCompletions(out)
	want := []Candidate{
		{Value: "attach", Desc: "Attach to the agent session"},
		{Value: "create", Desc: "Create a confined agentbox"},
		{Value: "coder", Desc: ""},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseCompletions = %+v, want %+v", got, want)
	}
	if directive != 4 {
		t.Errorf("directive = %d, want 4", directive)
	}
	if directive.NoSpace() {
		t.Errorf("Directive(4).NoSpace() = true, want false")
	}
}

// The NoSpace bit (1<<1, per cobra's ShellCompDirective layout) drives the
// palette's insert-without-terminator behavior for directory prefixes.
func TestParseCompletionsNoSpaceDirective(t *testing.T) {
	got, directive := parseCompletions([]byte("/tmp/proj/\n:6\n")) // NoSpace|NoFileComp
	if want := []Candidate{{Value: "/tmp/proj/"}}; !slices.Equal(got, want) {
		t.Fatalf("parseCompletions = %+v, want %+v", got, want)
	}
	if !directive.NoSpace() {
		t.Errorf("Directive(6).NoSpace() = false, want true")
	}

	// A missing directive line (subprocess cut short) is the zero default.
	if _, directive := parseCompletions([]byte("attach\n")); directive.NoSpace() {
		t.Errorf("missing directive line: NoSpace() = true, want false")
	}
}

// Only cobra's final :N record is a directive. Colon-prefixed host filenames
// earlier in the response remain candidates, including a filename that itself
// looks like a directive.
func TestParseCompletionsKeepsColonPrefixedCandidates(t *testing.T) {
	got, directive := parseCompletions([]byte(":auth.json\n:6\n:4\n\n"))
	want := []Candidate{{Value: ":auth.json"}, {Value: ":6"}}
	if !slices.Equal(got, want) {
		t.Fatalf("parseCompletions = %+v, want %+v", got, want)
	}
	if directive != 4 {
		t.Fatalf("directive = %d, want 4", directive)
	}
}
