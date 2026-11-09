package markdownview

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

var hlThemes = []spaceview.Theme{spaceview.NewTheme(true, nil, nil), spaceview.NewTheme(false, nil, nil)}

// highlightBlock highlights lines as one block of lang.
func highlightBlock(t *testing.T, lang string, theme spaceview.Theme, lines ...string) [][]Piece {
	t.Helper()
	sx := syntaxFor(lang)
	base := spaceview.Style{FG: theme.Text, BG: CodeCard(theme)}
	var state hlState
	var out [][]Piece
	for _, line := range lines {
		var p []Piece
		p, state = highlightLine(sx, line, state, base, theme)
		checkCover(t, line, p)
		out = append(out, p)
	}
	return out
}

// checkCover requires pieces to tile text exactly, in order, all code on the
// card's background: the highlighter only styles bytes, never drops any.
func checkCover(t *testing.T, text string, pieces []Piece) {
	t.Helper()
	at := 0
	for _, p := range pieces {
		if p.Start != at || p.End < p.Start || !p.Code {
			t.Fatalf("%q: pieces do not tile: %+v", text, pieces)
		}
		at = p.End
	}
	if at != len(text) {
		t.Fatalf("%q: pieces end at %d of %d", text, at, len(text))
	}
}

// classOf is the class whose style colors the first occurrence of sub.
func classOf(t *testing.T, theme spaceview.Theme, text string, pieces []Piece, sub string) tokenClass {
	t.Helper()
	at := strings.Index(text, sub)
	if at < 0 {
		t.Fatalf("%q not in %q", sub, text)
	}
	base := spaceview.Style{FG: theme.Text, BG: CodeCard(theme)}
	for _, p := range pieces {
		if at >= p.Start && at < p.End {
			if p.End < at+len(sub) {
				t.Fatalf("%q is split in %q: %+v", sub, text, pieces)
			}
			for c := tokPlain; c <= tokMeta; c++ {
				if tokenStyle(c, base, theme) == p.Style {
					return c
				}
			}
			t.Fatalf("%q has an unknown style", sub)
		}
	}
	t.Fatalf("%q not covered", sub)
	return tokPlain
}

func TestHighlightClassesForCommonLanguages(t *testing.T) {
	cases := []struct {
		lang, line string
		want       map[string]tokenClass
	}{
		{"go", `func main() { x := "hi" // say 42`, map[string]tokenClass{"func": tokKeyword, `"hi"`: tokString, "// say 42": tokComment, "main": tokPlain}},
		{"go", "var n int = 0x1F + len(s)", map[string]tokenClass{"var": tokKeyword, "int": tokType, "0x1F": tokNumber, "len": tokType}},
		{"Python", `def f(x): return None  # done`, map[string]tokenClass{"def": tokKeyword, "None": tokType, "# done": tokComment}},
		{"js", "const s = `a ${b}` + 'c'", map[string]tokenClass{"const": tokKeyword, "`a ${b}`": tokString, "'c'": tokString}},
		{"ts", "interface A { n: number }", map[string]tokenClass{"interface": tokKeyword, "number": tokType}},
		{"rust", "fn main() -> Result<(), String> { let mut v = 3; }", map[string]tokenClass{"fn": tokKeyword, "Result": tokType, "mut": tokKeyword, "3": tokNumber}},
		{"bash", `echo "$HOME" ${PATH} a#b # note`, map[string]tokenClass{"echo": tokType, `"$HOME"`: tokString, "${PATH}": tokType, "a#b": tokPlain, "# note": tokComment}},
		{"sql", "SELECT id FROM users WHERE name = 'x' -- why", map[string]tokenClass{"SELECT": tokKeyword, "FROM": tokKeyword, "'x'": tokString, "-- why": tokComment}},
		{"json", `{"name": "box", "n": 2, "ok": true}`, map[string]tokenClass{`"name"`: tokKey, `"box"`: tokString, "2": tokNumber, "true": tokType}},
		{"yaml", "  image: ubuntu:24.04 # base", map[string]tokenClass{"image": tokKey, "# base": tokComment}},
		{"yaml", "- name: web", map[string]tokenClass{"name": tokKey, "web": tokPlain}},
		{"toml", `[tool.lint]`, map[string]tokenClass{"[tool.lint]": tokKey}},
		{"toml", `version = "1.2"`, map[string]tokenClass{"version": tokKey, `"1.2"`: tokString}},
		{"c", `#include <stdio.h>`, map[string]tokenClass{"#include": tokKeyword}},
		{"dockerfile", "FROM golang:1.27 AS build", map[string]tokenClass{"FROM": tokKeyword, "AS": tokKeyword}},
		{"diff", "+added line", map[string]tokenClass{"+added line": tokAdded}},
		{"diff", "-removed", map[string]tokenClass{"-removed": tokRemoved}},
		{"diff", "@@ -1,2 +1,3 @@", map[string]tokenClass{"@@ -1,2 +1,3 @@": tokMeta}},
	}
	for _, theme := range hlThemes {
		for _, c := range cases {
			p := highlightBlock(t, c.lang, theme, c.line)[0]
			base := spaceview.Style{FG: theme.Text, BG: CodeCard(theme)}
			for sub, want := range c.want {
				// Added lines and strings share green: compare the styles.
				if got := classOf(t, theme, c.line, p, sub); tokenStyle(got, base, theme) != tokenStyle(want, base, theme) {
					t.Errorf("%s %q: %q is class %d, want %d", c.lang, c.line, sub, got, want)
				}
			}
		}
	}
}

// Block comments and multi-line strings continue onto the block's next
// lines and end where they close.
func TestHighlightCarriesStateAcrossLines(t *testing.T) {
	theme := hlThemes[0]
	lines := []string{"x := 1 /* start", "still comment", "end */ y := 2"}
	p := highlightBlock(t, "go", theme, lines...)
	if classOf(t, theme, lines[1], p[1], "still") != tokComment || classOf(t, theme, lines[2], p[2], "end */") != tokComment || classOf(t, theme, lines[2], p[2], "2") != tokNumber {
		t.Fatal("go block comment state")
	}
	lines = []string{`s = """doc`, "if inside", `""" if x`}
	p = highlightBlock(t, "py", theme, lines...)
	if classOf(t, theme, lines[1], p[1], "if") != tokString || classOf(t, theme, lines[2], p[2], "if") != tokKeyword {
		t.Fatal("python triple-quote state")
	}
	lines = []string{"q := `raw", "\\` + \"x\""}
	p = highlightBlock(t, "go", theme, lines...)
	if classOf(t, theme, lines[1], p[1], "\\`") != tokString || classOf(t, theme, lines[1], p[1], `"x"`) != tokString {
		t.Fatal("go raw string state (no escapes in backticks)")
	}
}

// Without a known language a block stays exactly as before: one plain piece.
func TestHighlightUnknownLanguageIsPlain(t *testing.T) {
	for _, lang := range []string{"", "text", "plaintext", "nosuchlang", "  "} {
		if syntaxFor(lang) != nil {
			t.Fatalf("%q selected a lexer", lang)
		}
		p := highlightBlock(t, lang, hlThemes[0], `if x { "y" } // z`)[0]
		if len(p) != 1 || p[0].Style.FG != hlThemes[0].Text {
			t.Fatalf("%q: %+v", lang, p)
		}
	}
	for _, info := range []string{"Go", "go title=main.go", "{.python}", "Dockerfile"} {
		if syntaxFor(info) == nil {
			t.Fatalf("%q selected no lexer", info)
		}
	}
}

// Arbitrary bytes, including unclosed quotes, stray delimiters and UTF-8,
// never panic and always tile the line, whatever the language.
func TestHighlightAlwaysTilesArbitraryLines(t *testing.T) {
	alphabet := []string{"\"", "'", "`", "/*", "*/", "//", "#", "--", "$", "${", "{", "}", "(", ")", "[", "]", ":", "=", "\\", " ", "\t", "a", "Z", "_", "9", ".", "-", "+", "@@", "é", "界", "func", "if", "true"}
	r := rand.New(rand.NewSource(1))
	langs := make([]string, 0, len(syntaxes))
	for lang := range syntaxes {
		langs = append(langs, lang)
	}
	for _, lang := range langs {
		var state hlState
		sx := syntaxes[lang]
		base := spaceview.Style{FG: hlThemes[0].Text, BG: CodeCard(hlThemes[0])}
		for n := 0; n < 300; n++ {
			var b strings.Builder
			for k := r.Intn(24); k > 0; k-- {
				b.WriteString(alphabet[r.Intn(len(alphabet))])
			}
			var p []Piece
			p, state = highlightLine(sx, b.String(), state, base, hlThemes[0])
			checkCover(t, b.String(), p)
		}
	}
}

// In the chat view a highlighted block keeps its card, its exact copy and
// its drag selection; only the colors of its code runs change.
