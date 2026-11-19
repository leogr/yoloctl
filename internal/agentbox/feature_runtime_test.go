package agentbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/boxcache"
	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func grantsByPath(t *testing.T, c *Config) map[string]FeatureGrant {
	t.Helper()
	rt, err := FeatureRuntimeOf(c)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]FeatureGrant{}
	for _, g := range rt.Grants {
		out[g.Path] = g
	}
	return out
}

func TestFeatureGrantsGoRustUnderNetworkGrants(t *testing.T) {
	c := &Config{Name: "coder", Space: "default", Features: []string{ociGoRef, ociRustRef}, FeatureLocks: ociGoRustLocks(), Capabilities: []string{"fs.workdir.write", "network.go-modules", "network.cargo"}}
	got := grantsByPath(t, c)
	for path, want := range map[string]bool{"/go": true, "/usr/local/cargo": true, "/usr/local/go": false, "/usr/local/rustup": false, "/go/bin": false, "/usr/local/cargo/bin": false, "/usr/local/go/bin": false} {
		g, ok := got[path]
		if !ok || g.Skip != "" || g.ReadWrite != want {
			t.Errorf("%s: grant %+v (present %v), want read-write=%v", path, g, ok, want)
		}
	}
	if !strings.Contains(got["/usr/local/go"].Note, "/usr/local/go/bin/go") {
		t.Errorf("Go toolchain read-only without naming the network binary: %+v", got["/usr/local/go"])
	}
	// Also without the go-modules grant: the catalog names /usr/local/go/bin/go,
	// so a program written over it now cannot hold the grant once added.
	c.Capabilities = []string{"fs.workdir.write"}
	got = grantsByPath(t, c)
	if g := got["/usr/local/go"]; g.ReadWrite || g.Skip != "" {
		t.Errorf("GOROOT writable while the box lacks network.go-modules: %+v", g)
	}
	if !got["/go"].ReadWrite || !got["/usr/local/cargo"].ReadWrite {
		t.Errorf("package homes not writable: %+v", got)
	}
}

func TestFeatureGrantNetworkPatternConflictsBothWays(t *testing.T) {
	ref := "registry.example/features/toolchain:1"
	meta := `{"id":"toolchain","version":"1","containerEnv":{"TOOLCHAIN":"/usr/local/rustup/toolchains/x","PATH":"/usr/local/rustup/toolchains/x/bin:${PATH}"}}`
	c := &Config{Name: "coder", Space: "default", Features: []string{ref}, FeatureLocks: []ocifeature.Lock{ociLock(ref, "c", meta)}, Capabilities: []string{"network.cargo"}}
	if g := grantsByPath(t, c)["/usr/local/rustup/toolchains/x"]; g.ReadWrite || g.Skip != "" {
		t.Fatalf("folder inside a network binary pattern became writable: %+v", g)
	}
}

func TestFeatureGrantsSkipProtectedMountsAndOrdinaryPaths(t *testing.T) {
	ref := "registry.example/features/odd:1"
	meta := `{"id":"odd","version":"1","containerEnv":{"ETC_HOME":"/etc","DATA":"/data/tool","CONFIG_FILE":"/opt/odd/config.json","PATH":"/opt/odd/bin:/data/tool/bin:${PATH}"}}`
	c := &Config{Name: "coder", Space: "default", Features: []string{ref}, FeatureLocks: []ocifeature.Lock{ociLock(ref, "d", meta)}, Mounts: []Mount{{Source: "/projects/data", Target: "/data"}}}
	got := grantsByPath(t, c)
	if g := got["/etc"]; g.Skip != "protected" {
		t.Errorf("/etc: %+v", g)
	}
	if g := got["/data/tool"]; !strings.Contains(g.Skip, "/data") {
		t.Errorf("folder under a declared mount granted: %+v", g)
	}
	if g := got["/opt/odd/config.json"]; g.ReadWrite || g.Skip != "" {
		t.Errorf("a path-shaped ordinary variable got write access: %+v", g)
	}
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policy), "- /opt/odd/bin") || strings.Contains(string(policy), "- /data/tool") {
		t.Fatalf("unexpected Feature folders in policy:\n%s", policy)
	}
}

// Every container mount destination yoloctl adds must be in a protected
// place, so a Feature folder (or a link the build guard accepts) never lands
// on one.
func TestYoloctlMountDestinationsAreProtected(t *testing.T) {
	for _, dest := range []string{
		sandbox.BinaryDirContainerPath, sandbox.PolicyContainerYAMLPath, sandbox.PolicyContainerRegoPath, sandbox.SocketDirContainer,
		boxendpoint.BinaryDir, openshell.HelperDirectory, openshell.NativeProjection, openshell.ArtifactProjection,
		operator.Mount, operator.HelperMount, operator.CodexSkill, operator.ClaudeSkill, operator.GeminiSkill,
		observe.ContainerDir, boxcache.Target,
		managedchat.EndpointMount, managedchat.ControlMount, managedchat.ServerConfigMount, managedchat.ServerDataMount,
		managedchat.IdentityMount, managedchat.HumanMount,
		NewEphemeralWorkdir("x").ContainerPath(), "/home/sandbox/.claude",
	} {
		if !ocifeature.ProtectedPath(dest) {
			t.Errorf("yoloctl mount destination %s is not protected", dest)
		}
	}
}

func TestFeatureEnvironmentPrecedenceAndProfileNames(t *testing.T) {
	ref := "registry.example/features/env:1"
	meta := `{"id":"env","version":"1","containerEnv":{"GOPATH":"/go","TOOL":"feature","ANTHROPIC_API_KEY":"feature-key","LD_LIBRARY_PATH":"/opt/lib"}}`
	c := &Config{Name: "coder", Space: "default", Agent: "claude", Features: []string{ref}, FeatureLocks: []ocifeature.Lock{ociLock(ref, "e", meta)}, Env: map[string]EnvVar{"TOOL": {Literal: "box"}}}
	env := FeatureEnvironment(c)
	if env["GOPATH"] != "/go" {
		t.Fatalf("Feature env not delivered: %v", env)
	}
	if _, ok := env["TOOL"]; ok {
		t.Fatalf("Feature value kept although box env sets it: %v", env)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "LD_LIBRARY_PATH", "PATH"} {
		if _, ok := env[key]; ok {
			t.Fatalf("%s delivered from a Feature: %v", key, env)
		}
	}
	rt, err := FeatureRuntimeOf(c)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, e := range rt.Env {
		reasons[e.Key] = e.Skip
	}
	if reasons["ANTHROPIC_API_KEY"] != "set by the agent profile" || reasons["LD_LIBRARY_PATH"] != "reserved" {
		t.Fatalf("skip reasons: %v", reasons)
	}
	// Native child bootstrap literals: Feature first, box replaces it.
	lit := workloadLiterals(c)
	if lit["GOPATH"] != "/go" || lit["TOOL"] != "box" {
		t.Fatalf("workload literals: %v", lit)
	}
	// Privileged children: base wins over Feature, account map over both.
	raw, _ := json.Marshal(env)
	child, err := boxendpoint.ChildEnvironmentWithFeatures("/home/sandbox", "sandbox", "/usr/bin", string(raw), `{"GOPATH":"/account"}`)
	if err != nil || !slices.Contains(child, "GOPATH=/account") || !slices.Contains(child, "HOME=/home/sandbox") {
		t.Fatalf("privileged child env: %v %v", child, err)
	}
}

func TestNoOCIFeaturesKeepOfflineReads(t *testing.T) {
	// A missing profile must not make policy composition strict.
	c := &Config{Name: "coder", Space: "default", Agent: "no-such-profile", Capabilities: []string{"fs.workdir.write"}}
	if _, ok := featurePlanLenient(c); ok {
		t.Fatal("a box without OCI Features resolved a Feature plan")
	}
	if rt, err := FeatureRuntimeOf(c); err != nil || len(rt.Grants)+len(rt.Env) != 0 {
		t.Fatalf("Feature runtime for a box without OCI Features: %+v %v", rt, err)
	}
}

func TestPrivilegedRunOptionsCarryFeatureEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "coder", Space: "default", Image: "ubuntu:24.04", Features: []string{ociGoRef, ociRustRef}, FeatureLocks: ociGoRustLocks(), Privileged: true}
	for _, endpoint := range []*EndpointBinary{nil, {SHA256: strings.Repeat("a", 64)}} {
		opts, err := buildRunOptionsWithEnv(c, "image", endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := ""
		for _, e := range opts.Env {
			if v, ok := strings.CutPrefix(e, boxendpoint.FeatureEnvironmentVariable+"="); ok {
				found = v
			}
		}
		if (found != "") != (endpoint != nil) {
			t.Fatalf("endpoint=%v: feature environment variable %q", endpoint != nil, found)
		}
		if endpoint != nil && !strings.Contains(found, `"RUSTUP_HOME":"/usr/local/rustup"`) {
			t.Fatalf("privileged feature environment: %s", found)
		}
	}
}

// Path scope of the toolchain cargo pattern with OPA's glob.match, like
// TestCodexEgressGlobScope; OPA is an optional development tool.
func TestCargoEgressGlobScope(t *testing.T) {
	opa, err := exec.LookPath("opa")
	if err != nil {
		t.Skip("opa not installed; run with OPA on PATH for policy evaluation")
	}
	data, err := ComposePolicy(&Config{Name: "rust", Space: "test", Features: []string{"cargo"}})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	const toolchains = "/usr/local/rustup/toolchains/"
	for binary, allow := range map[string]bool{
		"/usr/bin/cargo": true,
		toolchains + "1.100.0-x86_64-unknown-linux-gnu/bin/cargo":       true,
		toolchains + "stable-aarch64-unknown-linux-gnu/bin/cargo":       true,
		toolchains + "extra/1.100.0-x86_64-unknown-linux-gnu/bin/cargo": false,
		toolchains + "1.100.0-x86_64-unknown-linux-gnu/bin/rustc":       false,
		toolchains + "1.100.0-x86_64-unknown-linux-gnu/bin/extra/cargo": false,
		"/usr/local/cargo/bin/cargo":                                    false,
	} {
		encoded, _ := json.Marshal(map[string]any{"exec": map[string]any{"path": binary, "ancestors": []string{}}})
		query := `count([b | some b in data.network_policies.cargo.binaries; glob.match(b.path, ["/"], input.exec.path)]) > 0`
		cmd := exec.CommandContext(t.Context(), opa, "eval", "--format=raw", "--data", config, "--stdin-input", query)
		cmd.Stdin = bytes.NewReader(encoded)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("OPA evaluation: %v\n%s", err, output)
		}
		if got := strings.TrimSpace(string(output)); got != fmt.Sprint(allow) {
			t.Errorf("%s: match=%s, want %t", binary, got, allow)
		}
	}
}

func TestPrivilegedChildGetsBoxOverrideOfFeatureKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "coder", Space: "default", Image: "ubuntu:24.04", Features: []string{ociGoRef}, FeatureLocks: ociGoRustLocks()[:1], Privileged: true,
		Env: map[string]EnvVar{"GOPATH": {Literal: "/my-go"}, "UNRELATED": {Literal: "box-only"}}}
	opts, err := buildRunOptionsWithEnv(c, "image", &EndpointBinary{SHA256: strings.Repeat("a", 64)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	featureEnv := ""
	for _, e := range opts.Env {
		if v, ok := strings.CutPrefix(e, boxendpoint.FeatureEnvironmentVariable+"="); ok {
			featureEnv = v
		}
	}
	child, err := boxendpoint.ChildEnvironmentWithFeatures("/home/sandbox", "sandbox", "/usr/bin", featureEnv, `{"SECRET":"from-account"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GOPATH=/my-go", "GOROOT=/usr/local/go", "SECRET=from-account", "HOME=/home/sandbox"} {
		if !slices.Contains(child, want) {
			t.Fatalf("privileged child lacks %s: %v", want, child)
		}
	}
	for _, e := range child {
		if strings.HasPrefix(e, "UNRELATED=") {
			t.Fatalf("a box literal the Feature doesn't declare joined the Feature channel: %v", child)
		}
	}
	// The account map still wins over a box override.
	child, _ = boxendpoint.ChildEnvironmentWithFeatures("/home/sandbox", "sandbox", "/usr/bin", featureEnv, `{"GOPATH":"/account"}`)
	if !slices.Contains(child, "GOPATH=/account") {
		t.Fatalf("account map lost precedence: %v", child)
	}
}

// The privileged endpoint accepts the largest Feature map yoloctl delivers.
func TestPrivilegedEndpointAcceptsTheFeatureEntryCap(t *testing.T) {
	features := map[string]string{}
	for i := 0; i < 1024; i++ {
		features[fmt.Sprintf("K%04d", i)] = "v"
	}
	raw, _ := json.Marshal(features)
	if _, err := boxendpoint.ChildEnvironmentWithFeatures("/home/sandbox", "sandbox", "/usr/bin", string(raw), ""); err != nil {
		t.Fatalf("endpoint refused 1024 Feature entries: %v", err)
	}
	features["K1024"] = "v"
	raw, _ = json.Marshal(features)
	if _, err := boxendpoint.ChildEnvironmentWithFeatures("/home/sandbox", "sandbox", "/usr/bin", string(raw), ""); err == nil {
		t.Fatal("endpoint limit changed: update maxDeliveredEnv with it")
	}
}
