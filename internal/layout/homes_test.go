package layout

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestHomeMatrix(t *testing.T) {
	old := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old })
	for _, stamp := range []string{"dev", "v1.2.3", "invalid"} {
		buildinfo.Version = stamp
		leaf := buildinfo.HomeLeaf()
		if leaf != "yoloctl" {
			t.Fatalf("%s build leaf = %q; development and release share the default home", stamp, leaf)
		}
		for _, home := range []string{"", "/owned/home", "relative", "/unclean/../home"} {
			t.Run(stamp+home, func(t *testing.T) {
				t.Setenv("YOLOCTL_HOME", home)
				for _, xdg := range []string{"", "/xdg"} {
					t.Setenv("XDG_CONFIG_HOME", xdg)
					t.Setenv("XDG_CACHE_HOME", xdg)
					cfg, err := ConfigRoot()
					cache, cacheErr := CacheRoot()
					if home != "" && (home[0] != '/' || filepath.Clean(home) != home) {
						if err == nil || cacheErr == nil {
							t.Fatal("invalid home accepted")
						}
						continue
					}
					if err != nil || cacheErr != nil {
						t.Fatalf("roots: %v %v", err, cacheErr)
					}
					if home != "" && (cfg != home || cache != filepath.Join(home, "cache")) {
						t.Fatal(cfg, cache)
					}
					if home == "" && xdg != "" && (cfg != filepath.Join(xdg, leaf) || cfg != cache) {
						t.Fatal(cfg, cache)
					}
				}
			})
		}
		t.Setenv("YOLOCTL_HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		for _, base := range []string{"/linux/.config", "/mac/Library/Application Support"} {
			got, err := xdgRoot("XDG_CONFIG_HOME", func() (string, error) { return base, nil })
			if err != nil || got != filepath.Join(base, leaf) {
				t.Fatal(got, err)
			}
		}
	}
}

func TestHomeAliasIsLexical(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", filepath.Join(t.TempDir(), "alias"))
	got, err := ConfigRoot()
	if err != nil || got != os.Getenv("YOLOCTL_HOME") {
		t.Fatal(got, err)
	}
	// Root selection deliberately does not create or resolve links; aliases are
	// not an isolation boundary and may address the same host state.
}

func TestNativeHomePrecedence(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_NATIVE_HOME") == "1" {
		err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "uuid", DiskMount: "/mnt/native", ConfigDir: "/mnt/native/config", CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: "/host", UID: 1000, GID: 1000, Bundle: "bundle", Generation: "generation"})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ConfigRoot()
		if err != nil || cfg != spaceenv.CatalogRoot {
			t.Fatal(cfg, err)
		}
		cache, err := CacheRoot()
		if err != nil || cache != "/var/cache/yoloctl" {
			t.Fatal(cache, err)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeHomePrecedence$")
	cmd.Env = append(os.Environ(), "YOLOCTL_TEST_NATIVE_HOME=1", "YOLOCTL_HOME=relative")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native child: %s: %v", out, err)
	}
}
