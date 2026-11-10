package config

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestContext_LoadEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := LoadContext()
	if err != nil {
		t.Fatalf("LoadContext on missing file: %v", err)
	}
	if c.Space != "" {
		t.Fatalf("expected empty Context, got %+v", c)
	}
}

func TestContext_SaveLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, _ := LoadContext()
	c.Space = "prod-untrusted"
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadContext()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.Space != "prod-untrusted" {
		t.Fatalf("roundtrip: got %+v", loaded)
	}
}

func TestContext_SaveBothEmptyRemovesFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, _ := LoadContext()
	c.Space = "foo"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path()); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
	c.Space = ""
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected file gone after empty save, stat err=%v", err)
	}
}

func TestContext_SaveRejectsInvalidNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, _ := LoadContext()
	c.Space = "bad/name"
	if err := c.Save(); err == nil {
		t.Fatal("expected error from invalid space name")
	}
}

func TestResolveSpace_FlagWins(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, _ := LoadContext()
	c.Space = "pinned"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, src, err := ResolveSpace("explicit", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit" || src != SourceFlag {
		t.Errorf("got (%q, %q), want (explicit, flag)", got, src)
	}
}

func TestResolveSpace_ContextPin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, _ := LoadContext()
	c.Space = "pinned"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, src, err := ResolveSpace("", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "pinned" || src != SourceContext {
		t.Errorf("got (%q, %q), want (pinned, context)", got, src)
	}
}

func TestResolveSpace_FallbackToDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, src, err := ResolveSpace("", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultSpaceName || src != SourceDefault {
		t.Errorf("got (%q, %q), want (default, default)", got, src)
	}
}

func TestResolveSpace_FlagValidates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := ResolveSpace("bad;name", true); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLegacyLongSpaceContextRemainsUsable(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	c, err := LoadContext()
	if err != nil {
		t.Fatal(err)
	}
	c.Space = strings.Repeat("s", 63)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		value := ""
		if explicit {
			value = c.Space
		}
		got, _, err := ResolveSpace(value, explicit)
		if err != nil || got != c.Space {
			t.Fatalf("existing context %q %v", got, err)
		}
	}
}

func TestResolveSpaceEnvironment(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	c, _ := LoadContext()
	c.Space = "global"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pin, flag string
		explicit  bool
		want      string
		source    ResolutionSource
		bad       bool
	}{
		{"local", "ignored", false, "local", SourceEnvironment, false},
		{"local", "explicit", true, "explicit", SourceFlag, false},
		{"bad/name", "explicit", true, "explicit", SourceFlag, false},
		{"bad/name", "", false, "", "", true},
		{strings.Repeat("s", 63), "", false, strings.Repeat("s", 63), SourceEnvironment, false},
		{"", "", false, "global", SourceContext, false},
	} {
		t.Run(tc.pin+tc.flag, func(t *testing.T) {
			t.Setenv(SpaceEnv, tc.pin)
			got, source, err := ResolveSpace(tc.flag, tc.explicit)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid environment pin accepted")
				}
				return
			}
			if err != nil || got != tc.want || source != tc.source {
				t.Fatalf("%q %q %v", got, source, err)
			}
		})
	}
}
