package linkview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/markdownview"
)

func TestAdmissionUsesOnlyExplicitMarkdownAndOneBoundedProbe(t *testing.T) {
	calls := 0
	n := &filelink.Navigator{Context: filelink.Context{Mode: "host", Base: "/p", Root: "/p"}, Probe: func(_ context.Context, ts []filelink.Target) map[filelink.Target]error {
		calls++
		if len(ts) > filelink.MaxTargets {
			t.Fatal(len(ts))
		}
		out := map[filelink.Target]error{}
		for _, v := range ts {
			out[v] = nil
			if strings.HasSuffix(v.Path, "bad") {
				out[v] = errors.New("not readable")
			}
		}
		return out
	}}
	var destinations []string
	markdownview.Parse("prose no.md ` [code](code.md) `\n[a](a.md) [again](a.md) [bad](bad)\n```\n[fence](fence.md)\n```", markdownview.ParseOptions{Links: func(s string) markdownview.Action {
		destinations = append(destinations, s)
		return markdownview.Action{}
	}})
	a := n.AdmitDestinations(context.Background(), destinations)
	if calls != 1 || len(a) != 1 || Policy(a)("a.md").Kind != markdownview.IntentLocalFile || Policy(a)("bad").Kind != markdownview.IntentNone {
		t.Fatal(calls, a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if len(n.AdmitDestinations(ctx, []string{"a.md"})) != 0 {
		t.Fatal("admitted after cancellation")
	}
	if Policy(nil)("https://example.com").Kind != markdownview.IntentURL {
		t.Fatal("HTTP depended on local admission")
	}
}

func TestRawControlsNeverBecomeSanitizedFileCandidates(t *testing.T) {
	target := filelink.Target{Mode: "host", Path: "/p/a.md", Root: "/p"}
	a := filelink.Admissions{"a.md": target}
	for _, control := range []string{"\x00", "\a", "\x1b"} {
		raw := "[label](a" + control + ".md)"
		policy := Policy(a, raw)
		if act := policy("a.md"); act.Kind != markdownview.IntentNone || act.Value != "" {
			t.Fatalf("raw control entered admission: %+v", act)
		}
		doc := markdownview.Parse(raw, markdownview.ParseOptions{MapSource: true, Links: policy})
		if got := markdownview.RenderText(doc); got != "label (a.md)" {
			t.Fatal(got)
		}
		src := markdownview.SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: markdownview.Plain(raw), Render: func(s string) string {
			return markdownview.RenderText(markdownview.Parse(s, markdownview.ParseOptions{Links: policy}))
		}}
		if quoted, ok := src.Exact(0, 0, 0, len(doc.Lines[0].Text), true, true); !ok || quoted != "[label](a.md)" {
			t.Fatal(quoted, ok)
		}
	}
	for _, raw := range []string{"`[code](a\a.md)` [safe](a.md)", "```\n[code](a\x00.md)\n```\n[safe](a.md)", "\\[escaped](a\a.md) [safe](a.md)"} {
		if act := Policy(a, raw)("a.md"); act.Kind != markdownview.IntentLocalFile {
			t.Fatalf("code/escape poisoned valid destination: %q", raw)
		}
	}
}
