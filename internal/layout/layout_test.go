package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/buildinfo"
)

// setXDG points both XDG roots at temp dirs so every path helper is
// deterministic and assertable.
func setXDG(t *testing.T) (cfg, cache string) {
	t.Helper()
	t.Setenv("YOLOCTL_HOME", "")
	cfg = t.TempDir()
	cache = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)
	return cfg, cache
}

// pather returns a t-bound helper whose (string, error) signature matches
// the path helpers, so `must(ConfigRoot())` reads cleanly and fails the
// test on error.
func pather(t *testing.T) func(string, error) string {
	return func(p string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("path helper: %v", err)
		}
		return p
	}
}

func TestRoots(t *testing.T) {
	cfg, cache := setXDG(t)
	must := pather(t)
	if got := must(ConfigRoot()); got != filepath.Join(cfg, buildinfo.HomeLeaf()) {
		t.Errorf("ConfigRoot = %q", got)
	}
	if got := must(CacheRoot()); got != filepath.Join(cache, buildinfo.HomeLeaf()) {
		t.Errorf("CacheRoot = %q", got)
	}
}

// TestRootsFallbackWhenXDGUnset pins xdgRoot's OS-native fallback: when
// XDG_*_HOME is unset (or relative — IsAbs rejects both), ConfigRoot/CacheRoot
// defer to os.UserConfigDir/os.UserCacheDir. TestRoots covers the
// absolute-override path that the rest of the suite relies on for isolation —
// the path that, before the fix, silently no-opped on macOS because
// os.UserConfigDir ignores $XDG_CONFIG_HOME there.
func TestRootsFallbackWhenXDGUnset(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", "")
	must := pather(t)
	t.Setenv("XDG_CONFIG_HOME", "") // IsAbs("") == false → fall back
	t.Setenv("XDG_CACHE_HOME", "")

	osCfg, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("os.UserConfigDir unavailable: %v", err)
	}
	if got, want := must(ConfigRoot()), filepath.Join(osCfg, buildinfo.HomeLeaf()); got != want {
		t.Errorf("ConfigRoot fallback = %q, want %q", got, want)
	}

	osCache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("os.UserCacheDir unavailable: %v", err)
	}
	if got, want := must(CacheRoot()), filepath.Join(osCache, buildinfo.HomeLeaf()); got != want {
		t.Errorf("CacheRoot fallback = %q, want %q", got, want)
	}
}

func TestScopeFirstLayout(t *testing.T) {
	cfg, cache := setXDG(t)
	must := pather(t)
	root := filepath.Join(cfg, buildinfo.HomeLeaf())
	cacheRoot := filepath.Join(cache, buildinfo.HomeLeaf())

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"context", must(ContextPath()), filepath.Join(root, "context.yaml")},
		{"tui", must(TUIPath()), filepath.Join(root, "tui.yaml")},
		{"agent-profiles dir", must(AgentsDir()), filepath.Join(root, "agents")},
		{"agent-profile", must(AgentPath("claude")), filepath.Join(root, "agents", "claude.yaml")},
		{"features dir", must(FeaturesDir()), filepath.Join(root, "features")},
		{"lima home", must(LimaHome()), filepath.Join(root, "lima")},
		{"spaces dir", must(SpacesDir()), filepath.Join(root, "spaces")},
		{"space bundle", must(SpaceBundleDir("default")), filepath.Join(root, "spaces", "default")},
		{"space config", must(SpaceConfigPath("default")), filepath.Join(root, "spaces", "default", "space.yaml")},
		{"agentboxes dir", must(BoxesDir("default")), filepath.Join(root, "spaces", "default", "boxes")},
		{"agentbox", must(BoxPath("default", "coder")), filepath.Join(root, "spaces", "default", "boxes", "coder.yaml")},
		{"agentbox lock", must(BoxLockPath("default", "coder")), filepath.Join(root, "spaces", "default", "boxes", "coder.lock")},
		{"accounts dir", must(AccountsDir("default")), filepath.Join(root, "spaces", "default", "accounts")},
		{"account", must(AccountPath("default", "personal")), filepath.Join(root, "spaces", "default", "accounts", "personal.yaml")},
		{"account lock", must(AccountLockPath("default", "personal")), filepath.Join(root, "spaces", "default", "accounts", "personal.lock")},
		{"account-state root", must(AccountStateRoot()), filepath.Join("/var/lib/yoloctl", "account-state")},
		{"account state home", must(AccountStateHome("personal")), filepath.Join("/var/lib/yoloctl", "account-state", "personal")},
		{"account state leaf", must(AccountStatePath("personal", "claude")), filepath.Join("/var/lib/yoloctl", "account-state", "personal", "claude")},
		{"npm cache", must(NpmLatestCachePath("@anthropic-ai/claude-code")), filepath.Join(cacheRoot, "npm-latest", "@anthropic-ai%2Fclaude-code.json")},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// Host account manifests belong inside the space bundle; native paths are guest paths.
func TestBundleContainment(t *testing.T) {
	setXDG(t)
	must := pather(t)
	sep := string(filepath.Separator)
	bundle := must(SpaceBundleDir("default"))
	if box := must(BoxPath("default", "coder")); !strings.HasPrefix(box, bundle+sep) {
		t.Errorf("agentbox %q not inside bundle %q", box, bundle)
	}
	if acct := must(AccountPath("default", "personal")); !strings.HasPrefix(acct, bundle+sep) {
		t.Errorf("account manifest %q must be inside its space bundle %q", acct, bundle)
	}
	if state := must(AccountStateRoot()); strings.HasPrefix(state, bundle+sep) {
		t.Errorf("account-state root %q must be inside its space bundle %q", state, bundle)
	}
	// The per-agent leaf sits under the identity home, which sits under the
	// VM-native root.
	rootDir := must(AccountStateRoot())
	if home := must(AccountStateHome("personal")); filepath.Dir(home) != rootDir {
		t.Errorf("identity home %q not directly under the account-state root %q", home, rootDir)
	}
}

func TestValidateName(t *testing.T) {
	good := []struct{ kind, name string }{
		{"space", "default"}, {"agentbox", "coder"}, {"agentbox", "my-box_1"},
		{"template", "dev"}, {"agent", "claude"}, {"agent-profile", "my-claude"},
		{"template", "a"}, {"template", "myrepo"}, {"template", "my-repo"},
		{"template", "my_repo"}, {"template", "my.repo"}, {"template", "Repo123"},
		{"space", "sessions"}, {"template", "sessions"},
	}
	for _, g := range good {
		if err := ValidateName(g.kind, g.name); err != nil {
			t.Errorf("ValidateName(%q,%q) = %v, want nil", g.kind, g.name, err)
		}
	}
	bad := []struct{ kind, name string }{
		{"space", ""}, {"space", ".."}, {"agentbox", "../escape"},
		{"agentbox", "a/b"}, {"space", "has space"}, {"agentbox", "x;rm -rf"},
		{"agentbox", ".hidden"}, {"agentbox", "-leading-dash"},
		{"agentbox", "with\x00null"}, {"agentbox", "with$dollar"},
		{"agentbox", "with`backtick"}, {"agentbox", "with'quote"},
		{"agentbox", "sessions"}, // reserved
	}
	for _, b := range bad {
		if err := ValidateName(b.kind, b.name); err == nil {
			t.Errorf("ValidateName(%q,%q) = nil, want error", b.kind, b.name)
		}
	}
}

func TestValidateAgentboxNameBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"", false}, {"a", false}, {"7", false},
		{"aa", true}, {"77", true}, {"a.", true}, {"a_", true}, {"a-", true}, {"A0", true},
		{strings.Repeat("a", 63), true}, {strings.Repeat("a", 64), false},
		{".a", false}, {"_a", false}, {"-a", false}, {"a/b", false},
		{"a b", false}, {"a\t", false}, {"aa\n", false}, {"éa", false}, {"aé", false},
		{"sessions", false}, {"controller", false}, {"operators", false}, {"operator", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName("agentbox", tc.name)
			if (err == nil) != tc.valid {
				t.Fatalf("ValidateName(agentbox, %q) = %v, want valid=%v", tc.name, err, tc.valid)
			}
			if len(tc.name) == 1 && err != nil && !strings.Contains(err.Error(), "{1,62}") {
				t.Fatalf("error must describe effective box rule: %v", err)
			}
		})
	}
}

func TestValidateNonBoxNameBoundaries(t *testing.T) {
	for _, kind := range []string{"space", "account", "agent", "agent-profile", "template", "feature", "VM backend"} {
		for _, name := range []string{"a", "7", strings.Repeat("a", 63)} {
			if err := ValidateName(kind, name); err != nil {
				t.Errorf("ValidateName(%q, %q) = %v, want nil", kind, name, err)
			}
		}
		if err := ValidateName(kind, strings.Repeat("a", 64)); err == nil {
			t.Errorf("ValidateName(%q, 64 chars) = nil, want error", kind)
		}
	}
}

// TestPathHelpersValidateNames proves no helper joins an unvalidated name
// into a host path — the traversal guard at the boundary.
func TestPathHelpersValidateNames(t *testing.T) {
	setXDG(t)
	bad := "../escape"
	helpers := map[string]func() (string, error){
		"AgentPath":             func() (string, error) { return AgentPath(bad) },
		"SpaceBundleDir":        func() (string, error) { return SpaceBundleDir(bad) },
		"SpaceConfigPath":       func() (string, error) { return SpaceConfigPath(bad) },
		"BoxesDir":              func() (string, error) { return BoxesDir(bad) },
		"AgentboxPathSpace":     func() (string, error) { return BoxPath(bad, "coder") },
		"AgentboxPathName":      func() (string, error) { return BoxPath("default", bad) },
		"BoxLockPath":           func() (string, error) { return BoxLockPath("default", bad) },
		"AccountPath":           func() (string, error) { return AccountPath("default", bad) },
		"AccountLockPath":       func() (string, error) { return AccountLockPath("default", bad) },
		"AccountStateHome":      func() (string, error) { return AccountStateHome(bad) },
		"AccountStatePathName":  func() (string, error) { return AccountStatePath(bad, "claude") },
		"AccountStatePathAgent": func() (string, error) { return AccountStatePath("personal", bad) },
	}
	for name, fn := range helpers {
		if _, err := fn(); err == nil {
			t.Errorf("%s(%q) = nil error, want validation rejection", name, bad)
		}
	}
}

func TestAccountDiskNameFitsExt4Label(t *testing.T) {
	setXDG(t)
	id := "00000000000000000000000000000001"
	name := AccountDiskName(id)
	if len("lima-"+name) > 16 {
		t.Fatalf("Lima ext4 label would truncate: %s", name)
	}
	if err := ValidateName("disk", name); err != nil {
		t.Fatal(err)
	}
	if name != AccountDiskName(id) {
		t.Fatal("disk name is unstable")
	}
	if _, err := AccountDiskDir("../escape"); err == nil {
		t.Fatal("invalid disk identity accepted")
	}
}
