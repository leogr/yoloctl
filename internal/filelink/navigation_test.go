package filelink

import (
	"testing"
)

func TestRelativeDestinationsKeepOriginalRoot(t *testing.T) {
	c := Context{Mode: "vm", Space: "dev", Base: "/project/docs", Root: "/project"}
	for s, want := range map[string]string{"child.md": "/project/docs/child.md", "../README.md#intro": "/project/README.md", "a%20b.md": "/project/docs/a b.md", "%2e%2e/README.md": "/project/README.md"} {
		got, e := c.Resolve(s)
		if e != nil || got.Path != want {
			t.Fatalf("%q: %+v %v", s, got, e)
		}
	}
	for _, s := range []string{"../../out", "%2e%2e/%2e%2e/out", "%2Fetc/passwd", "//server/a", "https://x/a", "file:a", "/abs", "~/a", "a?b", "a?", "a\\b", "a%5cb", "a%00b", "#heading", ""} {
		if got, e := c.Resolve(s); e == nil {
			t.Errorf("accepted %q: %+v", s, got)
		}
	}
	child, e := ForFile("vm", "dev", "/project/docs/sub/a.md", "/unrelated", c.Root)
	if e != nil || child.Root != c.Root {
		t.Fatal(child, e)
	}
	outside, e := ForFile("host", "", "/other/a.md", "/project", "")
	if e != nil || outside.Root != "/other" {
		t.Fatal(outside, e)
	}
	if c.Accept(Target{Mode: "vm", Space: "other", Root: c.Root, Path: "/project/a"}) || c.Accept(Target{Mode: "host", Root: "/", Path: "/etc/passwd"}) {
		t.Fatal("accepted changed context")
	}
}
