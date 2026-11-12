package chatui

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// This golden was captured from d9aec4ac before moving production code, then
// deliberately updated once for feat-markdown-improvements (a pipe inside a
// code span stays in its table cell, --- draws a rule, list lines record
// their item geometry). There is deliberately no regeneration switch: a
// refactor must match it, and only an intended rendering change updates it.
// STEP1 wide-table fallback intentionally updates only narrow table layouts.
func TestMarkdownFrozenParity(t *testing.T) {
	got := markdownParityBytes(t)
	want, err := os.ReadFile("testdata/markdown-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		path := filepath.Join(t.TempDir(), "actual.json")
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("frozen Markdown parity differs: actual %x expected %x; actual=%s", sha256.Sum256(got), sha256.Sum256(want), path)
	}
}

// mermaidFields are the DisplayLine/LineSource fields added for mermaid
// drawings after the golden was frozen. The frozen inputs draw no diagram, so
// they are left out while zero and the rendering stays compared byte for byte.
var mermaidFields = map[string]bool{"Diagram": true, "DiagramFirst": true, "DiagramLast": true}

// Snapshot field spelling is case-insensitive so exporting the same pure data
// does not count as a rendering change. Numeric enums, nil/empty distinctions,
// all geometry, styles, actions and source maps remain observable.
func markdownParityValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return markdownParityValue(v.Elem())
	case reflect.Struct:
		out := map[string]any{}
		for i := range v.NumField() {
			// Newly added refusal flags have no observable effect when false; retain
			// the old snapshot shape for all unaffected Markdown cases.
			name := v.Type().Field(i).Name
			if (name == "Unpadded" || name == "TableLimit" || name == "Tail" || name == "Closed" || mermaidFields[name]) && v.Field(i).IsZero() {
				continue
			}
			out[strings.ToLower(v.Type().Field(i).Name)] = markdownParityValue(v.Field(i))
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range v.Len() {
			out[i] = markdownParityValue(v.Index(i))
		}
		return out
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	default:
		panic("unsupported Markdown parity field: " + v.Kind().String())
	}
}

func markdownParityBytes(t *testing.T) []byte {
	t.Helper()
	ref, ok := compactRef("11111111-2222-4333-8444-555555555555")
	if !ok {
		t.Fatal("invalid fixed reference fixture")
	}
	inputs := []struct{ name, text string }{
		{"prose", "# Heading\n> quote **bold** and *italic*\n- bullet\n  + nested\n9. literal ordered\n~~strike~~ ***both*** snake_case a * b\n[docs](https://example.org/a) and https://example.org/b.\n\\*escaped\\* `@me code` @Me @other @123 " + ref},
		{"tables", "before\n| Left | Center | Right |\n| :--- | :---: | ---: |\n| **wide column** | 👩‍💻\t🇫🇷 | [link](https://example.org) |\n| @Me | `a|b` | " + ref + " |\nafter"},
		{"ragged", "| a | b |\n| --- | --- |\n| one |\n| two | three | four |\n\n| not | a table |\ntext"},
		{"fences", "```go\n// comment\nfunc main() {\n\tprintln(\"@Me https://example.org\")\n}\n```\nafter\n~~~python\nx = '''multi\nline'''\n~~~\n```unknown\nliteral **text**\n"},
		{"unicode", "a\t▶️ flag 🇫🇷 👩‍💻 é\n> words wrap at narrow widths with hanging spaces and aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n\t@me"},
		{"inert-input", "bad\xffUTF8\r\n\x1b[31mred\x1b[0m\x00\a\x1b]52;c;YQ==\a\n<html>&amp;</html>\n---\n[^note]: literal\n![image](https://example.org/i.png)"},
		{"empty", "\n\n"},
		{"compact-closed", "|Name|Detail|\n|---|---|\n|alpha|" + strings.Repeat("long ▶️ words ", 20) + "|"},
		{"compact-open", "|Name|Detail|\n|---|---|\n|alpha|" + strings.Repeat("long ▶️ words ", 20) + "|"},
		{"preview", "intro " + ref + " end\n| reference |\n| --- |\n| " + ref + " |"},
	}
	var cases []any
	for _, in := range inputs {
		for _, dark := range []bool{false, true} {
			theme := spaceview.NewTheme(dark, nil, nil)
			for _, method := range []struct {
				name string
				wm   ansi.Method
			}{{"wc", ansi.WcWidth}, {"grapheme", ansi.GraphemeWidth}} {
				for _, source := range []bool{false, true} {
					var srcs []lineSrc
					lines := displayLinesMap(method.wm, in.text, source, theme, "me", &srcs)
					if in.name == "preview" && !source {
						lines = substitute(lines, []string{"↗ author: a loaded message"}, theme)
					}
					for _, width := range []int{20, 80, 120} {
						rows := bodyRows(method.wm, lines, width, theme)
						if strings.HasPrefix(in.name, "compact-") {
							opts := markdownview.LayoutOptions{Method: method.wm, Theme: theme, CompactTables: true}
							if in.name == "compact-open" {
								opts.OpenTableRows = map[markdownview.TableRowID]bool{{Table: 0, Row: 0}: true}
							}
							rows = markdownview.Layout(markdownview.Document{Lines: lines}, width, opts).Rows
						}
						var painted []string
						var hits []any
						for _, row := range rows {
							painted = append(painted, paint(method.wm, row.Runs, width, theme.Surface, nil, spaceview.Style{}))
							for _, col := range []int{0, 1, 2, width / 2, width - 1} {
								lo, hi := hitRun(method.wm, row.Runs, col, row.End)
								hits = append(hits, []any{col, lo, hi, markdownParityValue(reflect.ValueOf(actionAt(method.wm, row.Runs, col)))})
							}
						}
						var copies []string
						var quotes []any
						m := New(Options{})
						m.theme, m.widthMethod, m.source, m.self = theme, method.wm, source, "me"
						q := quoteSpans{lines: lines, srcs: srcs, text: plain(in.text), m: m}
						for n, dl := range lines {
							copies = append(copies, dl.CopyText(0, len(dl.Text)))
							if dl.Decor || len(dl.Subs) != 0 {
								continue
							}
							if dl.Table != nil {
								quotes = append(quotes, markdownParityValue(reflect.ValueOf(tableText(lines, n, n, 0, len(dl.Text)))))
								continue
							}
							for _, start := range []int{0, min(1, len(dl.Text))} {
								quoted, why := q.exact(n, start, n, len(dl.Text), true, true)
								quotes = append(quotes, []any{n, start, quoted, why})
							}
						}
						cases = append(cases, map[string]any{
							"case":  fmt.Sprintf("%s/dark=%v/%s/source=%v/width=%d", in.name, dark, method.name, source, width),
							"lines": markdownParityValue(reflect.ValueOf(lines)), "rows": markdownParityValue(reflect.ValueOf(rows)),
							"sources": markdownParityValue(reflect.ValueOf(srcs)), "copy": copies, "quote": quotes, "paint": painted, "hit": hits,
						})
					}
				}
			}
		}
	}
	raw, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}
