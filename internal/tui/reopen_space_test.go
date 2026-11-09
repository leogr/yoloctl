package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestReopenSpaceMatchesCLIResolution(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	got, err := reopenSpace(nil)
	want, _, cliErr := config.ResolveSpace("", false)
	if err != nil || cliErr != nil || got != want || got != "default" {
		t.Fatalf("default resolution=%q/%v", got, err)
	}
	for _, space := range []string{"pinned", strings.Repeat("s", 63)} {
		ctx := &config.Context{Space: space}
		if err := ctx.Save(); err != nil {
			t.Fatal(err)
		}
		got, err = reopenSpace(nil)
		want, _, cliErr = config.ResolveSpace("", false)
		if err != nil || cliErr != nil || got != want || got != space {
			t.Fatalf("pin resolution=%q/%v", got, err)
		}
		got, err = reopenSpace(&spaceenv.Context{Space: "native"})
		if err != nil || got != "native" {
			t.Fatalf("trusted native priority=%q/%v", got, err)
		}
		args := []string{"chat", "--space", "explicit"}
		pinned := pinReopenArgs(args, func() (string, error) { t.Fatal("explicit target consulted native/context"); return "", nil })
		if !slices.Equal(args, pinned) {
			t.Fatal("explicit target changed")
		}
	}
	path, err := layout.ContextPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("space: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopenSpace(nil); err == nil {
		t.Fatal("malformed context was silently resolved")
	}
	args := []string{"chat"}
	pinned := pinReopenArgs(args, func() (string, error) { return reopenSpace(nil) })
	if !slices.Equal(pinned, args) {
		t.Fatal("malformed context changed original argv/error path")
	}
}
