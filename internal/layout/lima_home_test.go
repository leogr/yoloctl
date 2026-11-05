package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSpaceNameCapPreservesExistingNamesAndOtherKinds(t *testing.T) {
	for _, n := range []int{1, 16, 17, 63, 64} {
		name := strings.Repeat("a", n)
		if err := ValidateName("space", name); (err == nil) != (n <= 16) {
			t.Fatalf("new %d: %v", n, err)
		}
		if err := ValidateExistingSpaceName(name); (err == nil) != (n <= 63) {
			t.Fatalf("stored %d: %v", n, err)
		}
		for _, kind := range []string{"account", "agentbox"} {
			if err := ValidateName(kind, name); (err == nil) != (n <= 63 && (kind != "agentbox" || n >= 2)) {
				t.Fatalf("%s %d: %v", kind, n, err)
			}
		}
	}
	for _, name := range []string{"", "../escape", ".hidden", "a/b", "é"} {
		if ValidateExistingSpaceName(name) == nil {
			t.Fatalf("unsafe existing name %q", name)
		}
	}
	setXDG(t)
	long := strings.Repeat("s", 63)
	if _, err := SpaceBundleDir(long); err != nil {
		t.Fatal(err)
	}
	if _, err := FirecrackerInstanceDir(long); err != nil {
		t.Fatal(err)
	}
	if _, err := SpaceLockPath(long); err != nil {
		t.Fatal(err)
	}
}

func TestNewLimaHomePlatformSelectionAndOverrides(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "long-user-home"))
	home, err := newLimaHome("darwin")
	if err != nil || home != filepath.Join(os.Getenv("HOME"), ".config", "yoloctl", "lima") {
		t.Fatalf("new Mac: %s %v", home, err)
	}
	legacy, err := LimaHome()
	if err != nil {
		t.Fatal(err)
	}
	linux, err := newLimaHome("linux")
	if err != nil || linux != legacy {
		t.Fatalf("Linux changed: %s %v", linux, err)
	}
	for _, env := range []string{"YOLOCTL_HOME", "XDG_CONFIG_HOME"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, t.TempDir())
			old, err := LimaHome()
			if err != nil {
				t.Fatal(err)
			}
			got, err := newLimaHome("darwin")
			if err != nil || got != old {
				t.Fatalf("override ignored: %s %v", got, err)
			}
		})
	}
	t.Setenv("YOLOCTL_HOME", "relative")
	if _, err := NewLimaHome(); err == nil {
		t.Fatal("invalid home accepted")
	}
}

func TestRecordedLimaHomeValidationAndLegacyFallback(t *testing.T) {
	setXDG(t)
	legacy, _ := LimaHome()
	if got, err := ResolveLimaHome(""); err != nil || got != legacy {
		t.Fatalf("fallback: %s %v", got, err)
	}
	recorded := filepath.Join(t.TempDir(), "persisted")
	if got, err := ResolveLimaHome(recorded); err != nil || got != recorded {
		t.Fatalf("recorded: %s %v", got, err)
	}
	for _, bad := range []string{"relative", "/a/../b", "/path/"} {
		if _, err := ResolveLimaHome(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
