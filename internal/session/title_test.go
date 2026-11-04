package session

import (
	"strings"
	"testing"
)

func TestTitleRewriter(t *testing.T) {
	const (
		esc = "\x1b"
		bel = "\x07"
		st  = "\x1b\\" // String Terminator: ESC \
	)
	for _, c := range []struct {
		name   string
		chunks []string // fed through Rewrite in order, then Flush
		want   string
	}{
		{"plain passthrough", []string{"hello world"}, "hello world"},
		{"osc2 window title prefixed", []string{esc + "]2;mytitle" + bel}, esc + "]2;box: mytitle" + bel},
		{"osc0 icon+title prefixed", []string{esc + "]0;t" + bel}, esc + "]0;box: t" + bel},
		{"osc1 icon prefixed", []string{esc + "]1;t" + bel}, esc + "]1;box: t" + bel},
		{"osc2 with ST terminator", []string{esc + "]2;t" + st}, esc + "]2;box: t" + st},
		{"osc52 clipboard untouched", []string{esc + "]52;c;Zm9v" + bel}, esc + "]52;c;Zm9v" + bel},
		{"osc7 cwd untouched", []string{esc + "]7;file:///x" + bel}, esc + "]7;file:///x" + bel},
		{"csi untouched", []string{esc + "[31mred" + esc + "[0m"}, esc + "[31mred" + esc + "[0m"},
		{"title then output", []string{esc + "]2;t" + bel + "after"}, esc + "]2;box: t" + bel + "after"},
		{"empty title", []string{esc + "]2;" + bel}, esc + "]2;box: " + bel},
		{"split mid-introducer", []string{esc + "]2", ";t" + bel}, esc + "]2;box: t" + bel},
		{"split after esc", []string{"a" + esc, "]2;t" + bel}, "a" + esc + "]2;box: t" + bel},
		{"split esc, not osc", []string{esc, "[1mx"}, esc + "[1mx"},
		{"two titles", []string{esc + "]2;a" + bel + esc + "]0;b" + bel}, esc + "]2;box: a" + bel + esc + "]0;box: b" + bel},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := newTitleRewriter("box")
			var got strings.Builder
			for _, ch := range c.chunks {
				got.Write(tr.Rewrite([]byte(ch)))
			}
			got.Write(tr.Flush())
			if got.String() != c.want {
				t.Errorf("Rewrite(%q) = %q, want %q", c.chunks, got.String(), c.want)
			}
		})
	}
}

// A stream that ends mid-introducer must not silently drop the held bytes.
func TestTitleRewriterFlushLeftover(t *testing.T) {
	tr := newTitleRewriter("box")
	got := append(tr.Rewrite([]byte("x\x1b]2")), tr.Flush()...)
	if string(got) != "x\x1b]2" {
		t.Errorf("truncated introducer = %q, want %q", got, "x\x1b]2")
	}
}

// Template shapes beyond the default prefix: identity, suffix placement,
// and full replacement (no {title}).
func TestTemplateTitleRewriter(t *testing.T) {
	const (
		esc = "\x1b"
		bel = "\x07"
		st  = "\x1b\\"
	)
	for _, c := range []struct {
		name   string
		tpl    string
		chunks []string
		want   string
	}{
		{"identity passes titles through", "{title}",
			[]string{esc + "]2;mytitle" + bel + "rest"}, esc + "]2;mytitle" + bel + "rest"},
		{"prefix template equals default machinery", "box: {title}",
			[]string{esc + "]2;t" + bel}, esc + "]2;box: t" + bel},
		{"literal backslash separator", "personal\\mybox: {title}",
			[]string{esc + "]2;t" + bel}, esc + "]2;personal\\mybox: t" + bel},
		{"suffix with BEL", "{title} · box",
			[]string{esc + "]2;my" + bel}, esc + "]2;my · box" + bel},
		{"suffix with ST", "{title} · box",
			[]string{esc + "]2;my" + st}, esc + "]2;my · box" + st},
		{"prefix and suffix", "[box] {title}!",
			[]string{esc + "]0;t" + bel}, esc + "]0;[box] t!" + bel},
		{"suffix, empty title", "{title}@box",
			[]string{esc + "]2;" + bel}, esc + "]2;@box" + bel},
		{"replacement swallows the agent text", "just-box",
			[]string{esc + "]2;secret" + bel}, esc + "]2;just-box" + bel},
		{"replacement with ST", "just-box",
			[]string{esc + "]1;secret" + st}, esc + "]1;just-box" + st},
		{"buffered title split across reads", "{title})",
			[]string{esc + "]2;ab", "cd" + bel + "after"}, esc + "]2;abcd)" + bel + "after"},
		{"two buffered titles back to back", "{title}#",
			[]string{esc + "]2;a" + bel + esc + "]0;b" + bel}, esc + "]2;a#" + bel + esc + "]0;b#" + bel},
		{"osc52 untouched in buffered mode", "{title}#",
			[]string{esc + "]52;c;Zm9v" + bel}, esc + "]52;c;Zm9v" + bel},
		{"csi untouched in buffered mode", "{title}#",
			[]string{esc + "[31mred" + esc + "[0m"}, esc + "[31mred" + esc + "[0m"},
		{"malformed ESC inside title flushes raw", "{title}#",
			[]string{esc + "]2;ab" + esc + "[1mx" + bel}, esc + "]2;ab" + esc + "[1mx" + bel},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := newTemplateTitleRewriter(c.tpl, "")
			var got strings.Builder
			for _, ch := range c.chunks {
				got.Write(tr.Rewrite([]byte(ch)))
			}
			got.Write(tr.Flush())
			if got.String() != c.want {
				t.Errorf("Rewrite(%q) = %q, want %q", c.chunks, got.String(), c.want)
			}
		})
	}
}

// An oversized buffered title abandons the rewrite at the cap and streams
// raw from there — bounded memory, no lost bytes, terminator intact.
func TestTemplateTitleRewriterOverflow(t *testing.T) {
	tr := newTemplateTitleRewriter("{title}#", "")
	long := strings.Repeat("x", trMaxTitle+10)
	in := "\x1b]2;" + long + "\x07after"
	got := string(tr.Rewrite([]byte(in)))
	got += string(tr.Flush())
	if got != in {
		t.Errorf("overflowed title must stream raw without the suffix:\ngot  %q\nwant %q", got, in)
	}
}

// A buffered title the stream never terminates is flushed raw, not dropped.
func TestTemplateTitleRewriterFlushBufferedBody(t *testing.T) {
	tr := newTemplateTitleRewriter("{title}#", "")
	got := string(tr.Rewrite([]byte("\x1b]2;abc")))
	got += string(tr.Flush())
	if got != "\x1b]2;abc" {
		t.Errorf("unterminated title = %q, want %q", got, "\x1b]2;abc")
	}
	// Same with a trailing ESC (a torn ST): the ESC must survive too.
	tr = newTemplateTitleRewriter("{title}#", "")
	got = string(tr.Rewrite([]byte("\x1b]2;abc\x1b")))
	got += string(tr.Flush())
	if got != "\x1b]2;abc\x1b" {
		t.Errorf("unterminated title with torn ST = %q, want %q", got, "\x1b]2;abc\x1b")
	}
}

// With an agent icon to strip, every title update that starts with the
// icon loses it and the spaces after it; other titles pass unchanged.
func TestTitleRewriterStripsAgentIcon(t *testing.T) {
	const bel = "\x07"
	tr := newTemplateTitleRewriter("✳ me's box: {title}", "✳")
	for _, c := range []struct{ in, want string }{
		{"✳ fix-auth", "✳ me's box: fix-auth"},
		{"✳fix-auth", "✳ me's box: fix-auth"},
		{"⠂ fix-auth", "✳ me's box: ⠂ fix-auth"}, // dynamic: only titles that start with it
		{"fix ✳ auth", "✳ me's box: fix ✳ auth"},
		{"✳", "✳ me's box: "},
	} {
		got := string(tr.Rewrite([]byte("\x1b]2;" + c.in + bel)))
		if want := "\x1b]2;" + c.want + bel; got != want {
			t.Errorf("title %q = %q, want %q", c.in, got, want)
		}
	}
	// A multi-character icon strips only as a whole.
	tr = newTemplateTitleRewriter(">_ {title}", ">_")
	for in, want := range map[string]string{">_  codex": ">_ codex", ">x": ">_ >x"} {
		if got := string(tr.Rewrite([]byte("\x1b]0;" + in + bel))); got != "\x1b]0;"+want+bel {
			t.Errorf("title %q = %q, want %q", in, got, "\x1b]0;"+want+bel)
		}
	}
}

func TestExpandBoxTitleTemplate(t *testing.T) {
	for _, c := range []struct {
		name      string
		tpl       string
		icon      string
		want      string
		wantStrip string
		wantErr   bool
	}{
		{name: "default", tpl: "{agent-icon} {account}\\{box}: {title}", icon: "✳",
			want: "✳ personal\\mybox: {title}", wantStrip: "✳"},
		{name: "explicit previous format", tpl: "{agent-icon} {account}'s {box}: {title}", icon: "✳",
			want: "✳ personal's mybox: {title}", wantStrip: "✳"},
		{name: "plain box prefix", tpl: "{box}: {title}", icon: "✳", want: "mybox: {title}"},
		{name: "all placeholders", tpl: "{box}/{space}/{agent}/{account} {title}",
			want: "mybox/dev/claude/personal {title}"},
		{name: "empty icon drops its spaces", tpl: "{agent-icon}  {box}: {title}", want: "mybox: {title}"},
		{name: "no title placeholder", tpl: "{box} ({agent})", want: "mybox (claude)"},
		{name: "title only", tpl: "{title}", want: "{title}"},
		{name: "literal closing brace", tpl: "a} {box}", want: "a} mybox"},
		{name: "unknown placeholder", tpl: "{nope} {title}", wantErr: true},
		{name: "double title", tpl: "{title}{title}", wantErr: true},
		{name: "unclosed placeholder", tpl: "{box", wantErr: true},
		{name: "control character", tpl: "a\x07{title}", wantErr: true},
		{name: "control character in icon", tpl: "{agent-icon} {title}", icon: "\x1b", wantErr: true},
		{name: "title token in icon", tpl: "{agent-icon} {box}: {title}", icon: "{title}", wantErr: true},
		{name: "icon cannot add a title to replacement template", tpl: "{agent-icon} {box}", icon: "{title}", wantErr: true},
		{name: "title token across icon boundary", tpl: "{agent-icon}le} {box}: {title}", icon: "{tit", wantErr: true},
		{name: "other braces in icon stay literal", tpl: "{agent-icon} {title}", icon: "{box}", want: "{box} {title}", wantStrip: "{box}"},
		{name: "unused icon cannot affect title", tpl: "{title}", icon: "{title}", want: "{title}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, strip, err := ExpandBoxTitleTemplate(c.tpl, "mybox", "dev", "claude", "personal", c.icon)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Expand(%q) succeeded with %q, want error", c.tpl, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Expand(%q): %v", c.tpl, err)
			}
			if got != c.want || strip != c.wantStrip {
				t.Errorf("Expand(%q) = %q strip %q, want %q strip %q", c.tpl, got, strip, c.want, c.wantStrip)
			}
		})
	}
}

// A generic box has no agent/account; the placeholders render the "none"
// sentinel rather than vanishing, and it has no icon.
func TestExpandBoxTitleTemplateGenericBox(t *testing.T) {
	got, strip, err := ExpandBoxTitleTemplate("{box} {agent} {account}", "shellbox", "default", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "shellbox none none" || strip != "" {
		t.Errorf("generic box expansion = %q strip %q, want %q", got, strip, "shellbox none none")
	}
	got, _, err = ExpandBoxTitleTemplate("{agent-icon} {account}\\{box}: {title}", "shellbox", "default", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "none\\shellbox: {title}"; got != want {
		t.Errorf("generic box default expansion = %q, want %q", got, want)
	}
}
