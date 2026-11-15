package agentbox

import (
	"bytes"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/boxcache"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func TestCacheBindingKeepsPolicyAndRejectsMountOverlap(t *testing.T) {
	saved := ensureCacheDirectory
	t.Cleanup(func() { ensureCacheDirectory = saved })
	calls := 0
	ensureCacheDirectory = func(name string, uid, gid int) error {
		calls++
		if name != "coder" || uid != 1000 || gid != 1001 {
			t.Fatal("wrong cache owner", name, uid, gid)
		}
		return nil
	}
	c := validConfig()
	before := mustCompose(t, c)
	opts := container.RunOptions{Mounts: buildMounts(c)}
	if err := addCacheOptions(c, &opts, 1000, 1001); err != nil {
		t.Fatal(err)
	}
	source, _ := boxcache.Path(c.Name)
	if calls != 1 || opts.Mounts[len(opts.Mounts)-1] != (container.Mount{Source: source, Destination: boxcache.Target}) {
		t.Fatal("missing read-write cache bind", opts.Mounts, calls)
	}
	if after := mustCompose(t, c); !bytes.Equal(before, after) {
		t.Fatal("controller cache changed policy")
	}
	for _, target := range []string{"/", "/home", "/home/sandbox", boxcache.Target, boxcache.Target + "/pip"} {
		c.Mounts = []Mount{{Source: "/host/project", Target: target}}
		if err := addCacheOptions(c, &container.RunOptions{}, 1000, 1001); err == nil {
			t.Fatal("accepted declared overlap", target)
		}
	}
	if calls != 1 {
		t.Fatal("prepared cache before refusing overlap", calls)
	}
	c.Mounts = nil
	c.Privileged = true
	if err := addCacheOptions(c, &container.RunOptions{}, 1000, 1001); err != nil || calls != 1 {
		t.Fatal("privileged box got cache bind", err, calls)
	}
	c.Privileged = false
	ensureCacheDirectory = func(string, int, int) error { return errors.New("foreign root") }
	opts = container.RunOptions{}
	if err := addCacheOptions(c, &opts, 1000, 1001); err == nil || len(opts.Mounts) != 0 {
		t.Fatal("bound unverified cache", err, opts)
	}
}

func TestCacheBindIsNotRecreateDrift(t *testing.T) {
	c := validConfig()
	source, _ := boxcache.Path(c.Name)
	command := sandbox.EntrypointArgs(c.Workdir.ContainerPath(), false, "sleep", "infinity")
	for _, bound := range []bool{false, true} {
		mounts := buildMounts(c)
		if bound {
			mounts = append(mounts, container.Mount{Source: source, Destination: boxcache.Target})
		}
		inspector := fixtureLiveInspector(t, c, command, mounts)
		state, err := inspector.Inspect(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Mounts) != 0 {
			t.Fatal("cache became a declared mount", state.Mounts)
		}
		applier := &fakeApplier{}
		result, err := Reconcile(t.Context(), c, inspector, applier)
		if err != nil || result.ContainerRecreated || len(applier.recreateCalls) != 0 {
			t.Fatal("new bind recreated existing container", result, err)
		}
	}
}

func TestCacheBindIdentityAndMissingLegacyBind(t *testing.T) {
	source, _ := boxcache.Path("coder")
	for _, tc := range []struct {
		mounts     []container.DetailMount
		bound, bad bool
	}{
		{nil, false, false},
		{[]container.DetailMount{{Source: source, Destination: boxcache.Target}}, true, false},
		{[]container.DetailMount{{Source: source, Destination: boxcache.Target, ReadOnly: true}}, false, true},
		{[]container.DetailMount{{Source: "/foreign", Destination: boxcache.Target}}, false, true},
		{[]container.DetailMount{{Source: source, Destination: boxcache.Target}, {Source: source, Destination: boxcache.Target}}, false, true},
	} {
		bound, err := CacheBound("coder", &container.Details{Mounts: tc.mounts})
		if bound != tc.bound || (err != nil) != tc.bad {
			t.Fatal("wrong bind classification", bound, err, tc)
		}
	}
}

func TestCacheOverlapIsDeclarationInvalidWithoutCreatingStorage(t *testing.T) {
	saved := ensureCacheDirectory
	t.Cleanup(func() { ensureCacheDirectory = saved })
	ensureCacheDirectory = func(string, int, int) error { t.Fatal("validation created cache state"); return nil }
	for _, target := range []string{"/", "/home", "/home/sandbox", boxcache.Target, boxcache.Target + "/pip"} {
		c := validConfig()
		c.Mounts = []Mount{{Source: "/host/project", Target: target}}
		if err := c.Validate(); err == nil {
			t.Fatal("overlapping declaration accepted", target)
		}
	}
	c := validConfig()
	c.Mounts = []Mount{{Source: "/host/project", Target: "/home/sandbox/.cache-other"}}
	if err := c.Validate(); err != nil {
		t.Fatal("sibling target refused", err)
	}
}

func TestCacheOverlapNormalizesGuestDestinations(t *testing.T) {
	saved := ensureCacheDirectory
	t.Cleanup(func() { ensureCacheDirectory = saved })
	ensureCacheDirectory = func(string, int, int) error {
		t.Fatal("cache prepared before rejecting normalized overlap")
		return nil
	}
	for _, target := range []string{"/home/sandbox/x/../.cache", "/home//sandbox/.cache", "/home/sandbox/./.cache/pip", "/home/sandbox/x/.."} {
		c := validConfig()
		c.Mounts = []Mount{{Source: "/host/project", Target: target}}
		if err := c.Validate(); err == nil {
			t.Error("accepted normalized declaration overlap", target)
		}
		opts := container.RunOptions{Mounts: []container.Mount{{Source: "/host/project", Destination: target}}}
		// Use a separate config to exercise the assembled-runtime check itself.
		if err := addCacheOptions(validConfig(), &opts, 1000, 1001); err == nil {
			t.Error("accepted normalized runtime overlap", target)
		}
	}
}
