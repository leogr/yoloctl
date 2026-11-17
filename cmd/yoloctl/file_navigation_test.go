package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/termio"
)

func TestFileNavigationWritesTypedPinnedContextAndTarget(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "a.md")
	if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	c := filelink.Context{Mode: "host", Base: root, Root: root}
	n, open, err := fileNavigation(context.Background(), c, write, fileNavigationSource{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(read)
	next := func() termio.TabRequest {
		t.Helper()
		if !scanner.Scan() {
			t.Fatal(scanner.Err())
		}
		var r termio.TabRequest
		if e := json.Unmarshal(scanner.Bytes(), &r); e != nil {
			t.Fatal(e)
		}
		return r
	}
	registration := next()
	got, e := registration.NavigationContext()
	if e != nil || got != c || registration.FollowPath != path {
		t.Fatal(registration, e)
	}
	target, e := c.Resolve("a.md")
	if e != nil {
		t.Fatal(e)
	}
	if e := n.Check(context.Background(), target); e != nil {
		t.Fatal(e)
	}
	bad := target
	bad.Root = "/"
	if e := open(bad); e == nil {
		t.Fatal("opener widened boundary")
	}
	if e := open(target); e != nil {
		t.Fatal(e)
	}
	r := next()
	if got, e := r.FollowTarget(); e != nil || got != target {
		t.Fatal(r, e)
	}
	_, standalone, e := fileNavigation(context.Background(), c, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e := standalone(target); e == nil || !strings.Contains(e.Error(), "TUI") {
		t.Fatal(e)
	}
}
