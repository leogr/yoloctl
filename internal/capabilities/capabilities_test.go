package capabilities

import (
	"testing"
)

// isolate points the user-capability override dir at an empty temp dir so
// Lookup/Names see only the built-ins (no dependency on the dev's real config).
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestLookupAndNames(t *testing.T) {
	isolate(t)
	for _, n := range []string{
		"network.anthropic", "network.openai", "network.github", "network.go-modules",
		"network.npm", "network.pypi", "network.cargo",
		WorkdirWriteCapability, PTYAllocateCapability,
	} {
		if _, err := Lookup(n); err != nil {
			t.Errorf("Lookup(%q): %v", n, err)
		}
	}
	if _, err := Lookup("does-not-exist"); err == nil {
		t.Error("expected error for unknown capability")
	}
	if got := len(Names()); got < 9 {
		t.Errorf("expected >=9 built-in capabilities, got %d", got)
	}
}

// TestPTYAllocateGrantsAreExact pins the PTY capability's path set. It is a
// security-relevant list: /dev/pts (the box's private devpts instance) and
// /dev/tty (the caller's own controlling terminal) — never /dev, never
// /dev/ptmx (a symlink in the box container; openshell-sandbox refuses to start
// on a symlinked read_write path), never read-only.
func TestPTYAllocateGrantsAreExact(t *testing.T) {
	isolate(t)
	c, err := Lookup(PTYAllocateCapability)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := []FSGrant{{Path: "/dev/pts", Access: ReadWrite}, {Path: "/dev/tty", Access: ReadWrite}}
	if len(c.Filesystem) != len(want) {
		t.Fatalf("filesystem grants = %+v, want %+v", c.Filesystem, want)
	}
	for i := range want {
		if c.Filesystem[i] != want[i] {
			t.Errorf("filesystem[%d] = %+v, want %+v", i, c.Filesystem[i], want[i])
		}
	}
	if len(c.Network) != 0 {
		t.Errorf("a PTY capability must not carry network grants: %+v", c.Network)
	}
	// PTYGrants hands out a copy: mutating it must not corrupt the registry.
	g := PTYGrants()
	g[0].Path = "/"
	if again, _ := Lookup(PTYAllocateCapability); again.Filesystem[0].Path != "/dev/pts" {
		t.Errorf("registry grant mutated through PTYGrants(): %+v", again.Filesystem)
	}
}

// TestEffectiveSetAdditiveUnion: the same capability added from two origins is
// granted ONCE (set union) with both origins recorded — positive, monotonic
// composition.
func TestEffectiveSetAdditiveUnion(t *testing.T) {
	isolate(t)
	s := NewEffectiveSet()
	mustAdd := func(name string, o Origin) {
		t.Helper()
		if err := s.Add(name, o); err != nil {
			t.Fatalf("Add(%q): %v", name, err)
		}
	}
	mustAdd("network.github", OriginExplicit)
	mustAdd("network.github", OriginFeature) // same cap, different origin
	mustAdd("network.anthropic", OriginFeature)

	if !s.Has("network.github") || !s.Has("network.anthropic") {
		t.Fatal("Has() did not report added capabilities")
	}
	count := 0
	for _, c := range s.Capabilities() {
		if c.Name == "network.github" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("network.github granted %d times, want 1 (union)", count)
	}
	for _, e := range s.Effective() {
		if e.Capability.Name == "network.github" && len(e.Origins) != 2 {
			t.Errorf("network.github origins = %v, want both explicit+feature", e.Origins)
		}
	}
}

// TestNoDenySemantics: there is no deny field, and a filesystem access value
// that is not a positive grant is rejected by validation.
func TestNoDenySemantics(t *testing.T) {
	bad := Capability{Name: "x", Filesystem: []FSGrant{{Path: "/x", Access: "deny"}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected validation error for non-positive access value")
	}
	good := Capability{Name: "x", Filesystem: []FSGrant{{Path: "/x", Access: ReadOnly}}}
	if err := good.Validate(); err != nil {
		t.Errorf("positive grant rejected: %v", err)
	}
}

// TestBuiltinsAllValidPositive: every built-in is a valid positive grant —
// network grants have endpoints, filesystem grants use read_only/read_write.
func TestBuiltinsAllValidPositive(t *testing.T) {
	isolate(t)
	for _, n := range Names() {
		c, err := Lookup(n)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", n, err)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("built-in %q invalid: %v", n, err)
		}
		for _, g := range c.Filesystem {
			if g.Access != ReadOnly && g.Access != ReadWrite {
				t.Errorf("built-in %q has non-positive fs access %q", n, g.Access)
			}
		}
		for _, g := range c.Network {
			if len(g.Endpoints) == 0 {
				t.Errorf("built-in %q network grant %q has no endpoints", n, g.PolicyName)
			}
		}
	}
}
