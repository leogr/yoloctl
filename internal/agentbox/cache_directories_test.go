package agentbox

import (
	"context"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

func TestCacheCreationPreparesOnlyFeatureDeclaredDirectories(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveEnsure, saveDirs := ensureCacheDirectory, prepareFeatureCacheDirectories
	t.Cleanup(func() { ensureCacheDirectory = saveEnsure; prepareFeatureCacheDirectories = saveDirs })
	var order []string
	ensureCacheDirectory = func(string, int, int) error { order = append(order, "leaf"); return nil }
	prepareFeatureCacheDirectories = func(_ context.Context, name string, uid, gid int, dirs []string) error {
		if name != "coder" || uid != 1000 || gid != 1001 || strings.Join(dirs, ",") != "cargo/git,cargo/registry" {
			t.Fatal(name, uid, gid, dirs)
		}
		order = append(order, "dirs")
		return nil
	}
	c := validConfig()
	c.Features = []string{"cargo"}
	var opts container.RunOptions
	if err := addCacheOptions(c, &opts, 1000, 1001); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "leaf,dirs" || len(opts.Mounts) != 1 {
		t.Fatal(order, opts.Mounts)
	}
	c.Features = []string{"go"}
	order = nil
	opts = container.RunOptions{}
	if err := addCacheOptions(c, &opts, 1000, 1001); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "leaf" {
		t.Fatal("created unrequested cargo directories", order)
	}
	c.Features = []string{"no-such-feature"}
	order = nil
	if err := addCacheOptions(c, &container.RunOptions{}, 1000, 1001); err == nil || len(order) != 0 {
		t.Fatal("prepared cache before resolving feature paths", order, err)
	}
}
