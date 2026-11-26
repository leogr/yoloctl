package mermaid

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/mermaid/admit"
)

// renderAll draws each source at width with the real worker and method.
func renderAll(t *testing.T, width int, method ansi.Method, sources ...string) []Result {
	t.Helper()
	var in, out bytes.Buffer
	for _, source := range sources {
		if err := writeFrame(&in, request{protocol, moduleHash(), width, methodName(method), source}, maxRequest); err != nil {
			t.Fatal(err)
		}
	}
	if err := RunWorker(t.Context(), &in, &out); err != nil {
		t.Fatal(err)
	}
	results := make([]Result, len(sources))
	for i := range sources {
		data, err := readFrame(&out, maxResponse)
		if err != nil {
			t.Fatal(err)
		}
		var reply response
		if err := strictJSON(data, &reply); err != nil {
			t.Fatal(err)
		}
		results[i] = reply.Result
	}
	return results
}

// cells maps each starting display column of row to its cluster, decoded as
// the chat and follow painters decode it.
func cells(method ansi.Method, row string) map[int]string {
	at := map[int]string{}
	col := 0
	var state byte
	for row != "" {
		seq, width, n, next := markdownview.DisplaySequence(method, row, state)
		at[col] = seq
		col += width
		row, state = row[n:], next
	}
	return at
}

// checkBoxes requires every box corner on the top border row to sit above a
// border on each label row and a corner on the bottom row, as the view draws
// them.
func checkBoxes(t *testing.T, method ansi.Method, top string, labels []string, bottom string) {
	t.Helper()
	topCells, bottomCells := cells(method, top), cells(method, bottom)
	corners := 0
	for col, c := range topCells {
		if c != "┌" && c != "┐" {
			continue
		}
		corners++
		for _, label := range labels {
			if got := cells(method, label)[col]; !strings.Contains("│├┤┼", got) || got == "" {
				t.Errorf("column %d: corner %s above %q in %q", col, c, got, label)
			}
		}
		if got := bottomCells[col]; got != "└" && got != "┘" {
			t.Errorf("column %d: corner %s above %q on the bottom row %q", col, c, got, bottom)
		}
	}
	if corners == 0 {
		t.Fatalf("no box corners in %q", top)
	}
}

// Labels with emoji sequences of several characters keep a tight box whose
// borders line up in both width methods: base + VS16, a keycap, ZWJ and skin
// tone sequences, beside plain wide emoji, flags, CJK and a combining accent.
func TestClusterLabelsKeepTheirBoxes(t *testing.T) {
	labels := []string{
		"🛠️ Fix", "✔️ done", "❤️ ok", "1️⃣ one", "👩‍💻 dev", "👍🏽 yes",
		"🐛 bug", "🚀 go", "🇮🇹 it", "中文 cjk", "café", "plain",
	}
	var sources []string
	for _, label := range labels {
		sources = append(sources, "flowchart LR\n  A["+label+"] --> B[end]\n")
	}
	for _, method := range []ansi.Method{ansi.GraphemeWidth, ansi.WcWidth} {
		for i, r := range renderAll(t, 80, method, sources...) {
			t.Run(methodName(method)+"/"+labels[i], func(t *testing.T) {
				if r.Status != OK || len(r.Lines) != 3 {
					t.Fatalf("%+v", r)
				}
				if want := "│ " + labels[i] + " ├────→│ end │"; r.Lines[1] != want {
					t.Errorf("label row %q, want %q", r.Lines[1], want)
				}
				checkBoxes(t, method, r.Lines[0], r.Lines[1:2], r.Lines[2])
			})
		}
	}
}

// The release-path flowchart from the screenshot: every box after 🛠️ Fix was
// one column off, and the offset carried along its row.
func TestScreenshotFlowchartBoxesLineUp(t *testing.T) {
	const source = "flowchart LR\n" +
		"    A[🐛 Issue #29] -->|\"5 s sudo\"| B[🔍 Diagnose]\n" +
		"    B -->|\"missing hostname\"| C[🛠️ Fix]\n" +
		"    C --> D{✅ Reviews?}\n" +
		"    D -->|\"both accept\"| E[🧪 Mac test]\n" +
		"    D -->|\"changes\"| C\n" +
		"    E --> F[🚀 Release]\n"
	for _, method := range []ansi.Method{ansi.GraphemeWidth, ansi.WcWidth} {
		t.Run(methodName(method), func(t *testing.T) {
			r := renderAll(t, 200, method, source)[0]
			if r.Status != OK || len(r.Lines) < 4 {
				t.Fatalf("%+v", r)
			}
			top, label, bottom := r.Lines[1], r.Lines[2], r.Lines[3]
			if !strings.Contains(label, "│ 🛠️ Fix ├") {
				t.Errorf("label row %q", label)
			}
			if w := method.StringWidth(top); method.StringWidth(label) != w || method.StringWidth(bottom) != w {
				t.Errorf("row widths %d, %d, %d", w, method.StringWidth(label), method.StringWidth(bottom))
			}
			checkBoxes(t, method, top, []string{label}, bottom)
		})
	}
}

func TestStandInsSkipSourceCharactersAndRestoreWholeRuns(t *testing.T) {
	wide, narrow := standInPools[2].first, standInPools[1].first
	source := "A[" + string(wide) + " 🛠\ufe0f 🛠\ufe0f 👍🏽]"
	got, standIns := withStandIns(source, ansi.WcWidth)
	// In wcwidth 🛠️ is one cell and 👍🏽 four. The source's own pool
	// character is skipped, and a repeated cluster reuses its run.
	thumb := string([]rune{wide + 1, wide + 2})
	if want := "A[" + string(wide) + " " + string(narrow) + " " + string(narrow) + " " + thumb + "]"; got != want {
		t.Fatalf("stand-ins %+q, want %+q", got, want)
	}
	if back, whole := restoreStandIns(got, standIns); !whole || back != source {
		t.Fatalf("restored %q (whole %v), want %q", back, whole, source)
	}
	// A run the renderer splits or cuts is not restored at all.
	for _, broken := range []string{string(wide+1) + "\n" + string(wide+2), "x " + string(wide+1), string(wide + 2)} {
		if back, whole := restoreStandIns(broken, standIns); whole {
			t.Errorf("broken run %+q restored as %q", broken, back)
		}
	}
}

// A combining or prepended mark next to Mermaid syntax forms one cluster with
// it; such clusters reach the parser unchanged, while keycaps (a leading
// digit) still stand in.
func TestStandInsLeaveSyntaxToTheParser(t *testing.T) {
	syntax := []string{
		"sequenceDiagram\n    A->>B:\u0301x\n",
		"flowchart LR\n    A[\u0301x] --> B[y]\n",
		"flowchart LR\n    A[x\u0600] --> B[y]\n",
		"flowchart LR\n    A -->|\u0301x| B\n",
	}
	for _, method := range []ansi.Method{ansi.GraphemeWidth, ansi.WcWidth} {
		for _, source := range syntax {
			if d := admit.Check(source); !d.Admitted() {
				t.Fatalf("%q not admitted: %s", source, d.Reason)
			}
			if got, standIns := withStandIns(source, method); got != source || len(standIns) != 0 {
				t.Errorf("%s: %+q became %+q", methodName(method), source, got)
			}
		}
		for cluster, want := range map[string]bool{
			":\u0301": false, "\u0600]": false, "\u0600x": false, "\u06001": false, "#\ufe0f\u20e3": false,
			"e\u0301": false, "x\u0301": false, "o\u0301": false,
			"1\ufe0f\u20e3": true, "🛠\ufe0f": true, "👩\u200d💻": true,
		} {
			if standsIn(cluster) != want {
				t.Errorf("standsIn(%+q) = %v", cluster, !want)
			}
		}
		for _, text := range []string{"1\ufe0f\u20e3", "🛠\ufe0f"} {
			if got, _ := withStandIns("A["+text+"]", method); got == "A["+text+"]" {
				t.Errorf("%s: %+q did not stand in", methodName(method), text)
			}
		}
	}
}

// rawDrawing is what the embedded renderer draws for source itself.
func rawDrawing(t *testing.T, source string, width int) []string {
	t.Helper()
	ctx := t.Context()
	rt := wazero.NewRuntime(ctx)
	defer func() { _ = rt.Close(ctx) }()
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	var out bytes.Buffer
	if _, err := rt.InstantiateWithConfig(ctx, artifact, wazero.NewModuleConfig().
		WithArgs("mermaid", strconv.Itoa(width)).WithStdin(strings.NewReader(source)).WithStdout(&out)); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
}

// Syntax joined by a mark draws exactly as the renderer draws the source, in
// both methods: the sequence message after "A->>B:" starts with U+0301
// (participants A and B, message x), and the x and o link ends carry one.
// Admission refuses the link ends; the worker still passes them unchanged.
func TestSyntaxBesideAMarkDrawsAsTheSource(t *testing.T) {
	controls := []struct{ source, row, has string }{
		{"sequenceDiagram\n    A->>B:\u0301x\n", "│  B   │", "x"},
		{"flowchart LR\n    A --x\u0301 B\n", "├────×│", "B"},
		{"flowchart LR\n    A --o\u0301 B\n", "├────○│", "B"},
	}
	for _, c := range controls {
		want := rawDrawing(t, c.source, 80)
		drawing := strings.Join(want, "\n")
		if !strings.Contains(drawing, c.row) || !strings.Contains(drawing, c.has) {
			t.Fatalf("renderer control for %+q: %q", c.source, want)
		}
		for _, method := range []ansi.Method{ansi.GraphemeWidth, ansi.WcWidth} {
			r := renderAll(t, 80, method, c.source)[0]
			if r.Status != OK || strings.Join(r.Lines, "\n") != drawing {
				t.Errorf("%s %+q: %+v, want %q", methodName(method), c.source, r, want)
			}
		}
	}
}

// At 17 columns the renderer wraps the message inside 👍🏽, which wcwidth
// stands in as two characters: the view shows the source instead of a drawing
// that lost the emoji. Grapheme stands it in as one character, so it stays
// whole, and a wider card draws it in both methods.
func TestWrappedEmojiRunShowsTheSource(t *testing.T) {
	const source = "sequenceDiagram\n    A->>B: XXXXX👍🏽YYYYYYYYYYYY\n"
	if r := renderAll(t, 17, ansi.WcWidth, source)[0]; r.Status != Failed || r.Lines != nil {
		t.Errorf("wcwidth at 17: %+v", r)
	}
	if r := renderAll(t, 17, ansi.GraphemeWidth, source)[0]; r.Status != OK || !strings.Contains(strings.Join(r.Lines, "\n"), "👍🏽") {
		t.Errorf("grapheme at 17: %+v", r)
	}
	for _, method := range []ansi.Method{ansi.GraphemeWidth, ansi.WcWidth} {
		if r := renderAll(t, 80, method, source)[0]; r.Status != OK || !strings.Contains(strings.Join(r.Lines, "\n"), "XXXXX👍🏽YYYYYYYYYYYY") {
			t.Errorf("%s at 80: %+v", methodName(method), r)
		}
	}
}

func TestStandInsKeepTheClusterWhenAPoolRunsOut(t *testing.T) {
	var b strings.Builder
	for r := standInPools[1].first; r <= standInPools[1].last; r++ {
		b.WriteRune(r)
	}
	source := b.String() + "🛠️"
	got, standIns := withStandIns(source, ansi.WcWidth)
	if got != source || len(standIns) != 0 {
		t.Fatalf("an exhausted pool changed the source (%d stand-ins)", len(standIns))
	}
}
