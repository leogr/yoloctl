package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

// execCmd runs a freshly-constructed command with the given stdin + args,
// capturing stdout/stderr/error. Mirrors execAccount in account_cmd_test.go.
func execCmd(t *testing.T, cmd *cobra.Command, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// TestCapabilityCatalogLs: `capability ls` lists both built-in and custom
// items, tagging each with its source.
func TestCapabilityCatalogLs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Add a custom capability by copying a built-in.
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "copy", "network.github", "network.mine"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	out, _, err := execCmd(t, newCapabilityCmd(), "", "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "network.github") || !strings.Contains(out, "(built-in)") {
		t.Errorf("ls output missing built-in capability:\n%s", out)
	}
	if !strings.Contains(out, "network.mine") || !strings.Contains(out, "(custom)") {
		t.Errorf("ls output missing custom capability:\n%s", out)
	}
}

func TestCapabilityCatalogShowsEncodedSlashGrant(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, _, err := execCmd(t, newCapabilityCmd(), "", "show", "network.opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "registry.npmjs.org ports=[443] allow_encoded_slash=true") || strings.Count(out, "allow_encoded_slash") != 1 {
		t.Fatalf("show lost or broadened the registry parsing permission: %s", out)
	}
	out, _, err = execCmd(t, newCapabilityCmd(), "", "show", "network.opencode", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Network []struct {
			Endpoints []struct {
				Host              string `json:"host"`
				AllowEncodedSlash bool   `json:"allow_encoded_slash"`
			} `json:"endpoints"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	var allowed []string
	for _, grant := range doc.Network {
		for _, endpoint := range grant.Endpoints {
			if endpoint.AllowEncodedSlash {
				allowed = append(allowed, endpoint.Host)
			}
		}
	}
	if len(allowed) != 1 || allowed[0] != "registry.npmjs.org" {
		t.Fatalf("JSON show lost or broadened the registry parsing permission: %s", out)
	}
}

// TestCapabilityCatalogCopyThenLookup: `capability copy` of a built-in writes a
// custom item the registry then loads (the grants shape round-trips).
func TestCapabilityCatalogCopyThenLookup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "copy", "network.github", "network.mygh"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := capabilities.Lookup("network.mygh")
	if err != nil {
		t.Fatalf("Lookup copied capability: %v", err)
	}
	if capabilities.IsBuiltin("network.mygh") {
		t.Error("copy must produce a custom capability")
	}
	if len(got.Network) != 1 || got.Network[0].PolicyName != "mygh" {
		t.Errorf("copied capability network = %+v, want policy_name mygh", got.Network)
	}
	// Re-copying onto an existing custom name is rejected.
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "copy", "network.github", "network.mygh"); err == nil {
		t.Error("copy onto an existing custom name should be rejected")
	}
	// Copying onto a built-in name is rejected.
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "copy", "network.github", "network.openai"); err == nil {
		t.Error("copy onto a built-in name should be rejected")
	}
}

// TestCapabilityCatalogPTYAllocate: the fs.pty.allocate built-in is listed,
// `show` prints its exact filesystem grants, and `copy` round-trips it through
// the high-level grants.pty.allocate authoring kind (a filesystem grant with no
// authoring representation would make `copy` fail with "no representable
// grants").
func TestCapabilityCatalogPTYAllocate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, _, err := execCmd(t, newCapabilityCmd(), "", "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, capabilities.PTYAllocateCapability) {
		t.Errorf("ls output missing %s:\n%s", capabilities.PTYAllocateCapability, out)
	}
	out, _, err = execCmd(t, newCapabilityCmd(), "", "show", capabilities.PTYAllocateCapability)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"built-in", "/dev/pts (read_write)", "/dev/tty (read_write)"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/dev/ptmx") {
		t.Errorf("show output must not grant /dev/ptmx:\n%s", out)
	}
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "copy", capabilities.PTYAllocateCapability, "my.pty"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := capabilities.Lookup("my.pty")
	if err != nil {
		t.Fatalf("Lookup copied capability: %v", err)
	}
	if len(got.Filesystem) != 2 || got.Filesystem[0].Path != "/dev/pts" || got.Filesystem[1].Path != "/dev/tty" {
		t.Errorf("copied capability filesystem = %+v, want /dev/pts + /dev/tty", got.Filesystem)
	}
}

// TestCapabilityCatalogEditDestroyRejectBuiltin: edit/destroy refuse a built-in
// NAME with a copy-first hint.
func TestCapabilityCatalogEditDestroyRejectBuiltin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := execCmd(t, newCapabilityCmd(), "", "edit", "network.github")
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Errorf("edit of a built-in should be rejected mentioning built-in, got %v", err)
	}
	_, _, err = execCmd(t, newCapabilityCmd(), "", "destroy", "network.github")
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Errorf("destroy of a built-in should be rejected mentioning built-in, got %v", err)
	}
}

// TestCapabilityCatalogDestroyCustom: `destroy --force` removes a custom capability.
func TestCapabilityCatalogDestroyCustom(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "copy", "network.github", "network.tmp"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "destroy", "--force", "network.tmp"); err != nil {
		t.Fatalf("destroy --force: %v", err)
	}
	if _, err := capabilities.Lookup("network.tmp"); err == nil {
		t.Error("capability should be gone after destroy")
	}
	// Destroying an unknown custom item errors.
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "destroy", "--force", "network.tmp"); err == nil {
		t.Error("destroy of a missing custom capability should error")
	}
}

// TestFeatureCatalogCopyAndDestroy: a built-in feature copies to a custom item
// that Lookup loads, then destroy removes it; edit/destroy reject built-ins.
func TestFeatureCatalogCopyAndDestroy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := execCmd(t, newFeatureCmd(), "", "copy", "claude", "my-claude"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := features.Lookup("my-claude")
	if err != nil {
		t.Fatalf("Lookup copied feature: %v", err)
	}
	if got.Install == nil || got.Install.Kind != features.InstallNpm {
		t.Errorf("copied feature install = %+v, want npm", got.Install)
	}

	// ls shows both.
	out, _, err := execCmd(t, newFeatureCmd(), "", "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "github\t(built-in)") || !strings.Contains(out, "my-claude\t(custom)") {
		t.Errorf("feature ls missing built-in/custom rows:\n%s", out)
	}

	// edit/destroy reject built-ins.
	if _, _, err := execCmd(t, newFeatureCmd(), "", "edit", "github"); err == nil {
		t.Error("edit of a built-in feature should be rejected")
	}
	if _, _, err := execCmd(t, newFeatureCmd(), "", "destroy", "github"); err == nil {
		t.Error("destroy of a built-in feature should be rejected")
	}

	// destroy the custom one.
	if _, _, err := execCmd(t, newFeatureCmd(), "", "destroy", "--force", "my-claude"); err != nil {
		t.Fatalf("destroy custom: %v", err)
	}
	if _, err := features.Lookup("my-claude"); err == nil {
		t.Error("feature should be gone after destroy")
	}
}

// TestFeatureCatalogShow: `feature show` works for a built-in.
func TestFeatureCatalogShow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, _, err := execCmd(t, newFeatureCmd(), "", "show", "github")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "github") || !strings.Contains(out, "built-in") {
		t.Errorf("feature show output unexpected:\n%s", out)
	}
	if !strings.Contains(out, "network.github") {
		t.Errorf("feature show should list the required capability:\n%s", out)
	}
}

// TestAgentCatalogViaAgentCmd: the agent catalog verbs hang off `agent` and
// behave the same (ls shows embedded built-ins; copy makes a custom;
// edit/destroy reject built-ins).
func TestAgentCatalogViaAgentCmd(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// ls includes the built-in agents.
	out, _, err := execCmd(t, newAgentCmd(), "", "ls")
	if err != nil {
		t.Fatalf("agent ls: %v", err)
	}
	if !strings.Contains(out, "claude\t(built-in)") || !strings.Contains(out, "codex\t(built-in)") {
		t.Errorf("agent ls missing built-ins:\n%s", out)
	}

	// copy a built-in to a custom name; it then resolves and is not built-in.
	if _, _, err := execCmd(t, newAgentCmd(), "", "copy", "claude", "my-claude"); err != nil {
		t.Fatalf("agent copy: %v", err)
	}
	out, _, err = execCmd(t, newAgentCmd(), "", "ls")
	if err != nil {
		t.Fatalf("agent ls: %v", err)
	}
	if !strings.Contains(out, "my-claude\t(custom)") {
		t.Errorf("agent ls missing the custom copy:\n%s", out)
	}

	// edit/destroy reject the built-in.
	if _, _, err := execCmd(t, newAgentCmd(), "", "edit", "claude"); err == nil {
		t.Error("agent edit of a built-in should be rejected")
	}
	if _, _, err := execCmd(t, newAgentCmd(), "", "destroy", "claude"); err == nil {
		t.Error("agent destroy of a built-in should be rejected")
	}

	// destroy the custom one.
	if _, _, err := execCmd(t, newAgentCmd(), "", "destroy", "--force", "my-claude"); err != nil {
		t.Fatalf("agent destroy custom: %v", err)
	}
	out, _, err = execCmd(t, newAgentCmd(), "", "ls")
	if err != nil {
		t.Fatalf("agent ls: %v", err)
	}
	if strings.Contains(out, "my-claude") {
		t.Errorf("custom agent should be gone after destroy:\n%s", out)
	}
}

// Fixtures for the destroy reference guard. The box lives in a bundle without
// a space.yaml: the scan walks box manifests, not space configs.
const (
	refCapability = "network.tmp"
	refFeature    = "my-gh"
	refAgent      = "my-agent"
)

func seedRefCapability(t *testing.T) {
	t.Helper()
	if _, _, err := execCmd(t, newCapabilityCmd(), "", "copy", "network.github", refCapability); err != nil {
		t.Fatalf("seed capability: %v", err)
	}
}

func seedRefFeature(t *testing.T, requires ...string) {
	t.Helper()
	f, err := features.Lookup("github")
	if err != nil {
		t.Fatalf("lookup github feature: %v", err)
	}
	f.Name = refFeature
	f.RequiresCapabilities = requires
	if err := features.SaveUser(f); err != nil {
		t.Fatalf("seed feature: %v", err)
	}
}

func seedRefAgent(t *testing.T, requires ...string) {
	t.Helper()
	p, err := agentprofile.Lookup("claude")
	if err != nil {
		t.Fatalf("lookup claude agent: %v", err)
	}
	p.Name = refAgent
	p.RequiresFeatures = requires
	// Its reporter must be required too; this custom agent declares none.
	p.Session.Observe = nil
	if err := p.Save(); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func seedRefBox(t *testing.T, mutate func(*agentbox.Config)) {
	t.Helper()
	cfg := &agentbox.Config{
		Name:    "coder",
		Space:   "prod",
		Image:   "img:1",
		Workdir: agentbox.NewEphemeralWorkdir("coder"),
	}
	mutate(cfg)
	if err := cfg.Save(); err != nil {
		t.Fatalf("seed box: %v", err)
	}
}

// A custom catalog item that something still names is not destroyed: the
// dangling name would make every box that reaches it fail to compose its
// policy, and the manifest save guard would then refuse any `box set` that does
// not also remove it. Same rule as `account destroy`: name the referrers,
// refuse, and let --force override.
func TestCatalogDestroyRefusesWhileReferenced(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cmd      func() *cobra.Command
		item     string
		seed     func(t *testing.T)
		referrer string
	}{
		{"capability granted by a box", newCapabilityCmd, refCapability, func(t *testing.T) {
			seedRefCapability(t)
			seedRefBox(t, func(c *agentbox.Config) { c.Capabilities = []string{refCapability} })
		}, "box prod/coder"},
		{"capability required by a custom feature", newCapabilityCmd, refCapability, func(t *testing.T) {
			seedRefCapability(t)
			seedRefFeature(t, refCapability)
		}, "feature " + refFeature},
		{"feature declared by a box", newFeatureCmd, refFeature, func(t *testing.T) {
			seedRefFeature(t)
			seedRefBox(t, func(c *agentbox.Config) { c.Features = []string{refFeature} })
		}, "box prod/coder"},
		{"feature required by a custom agent", newFeatureCmd, refFeature, func(t *testing.T) {
			seedRefFeature(t)
			seedRefAgent(t, refFeature)
		}, "agent " + refAgent},
		{"agent selected by a box", newAgentCmd, refAgent, func(t *testing.T) {
			seedRefAgent(t)
			seedRefBox(t, func(c *agentbox.Config) { c.Agent, c.Account = refAgent, "default" })
		}, "box prod/coder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			tc.seed(t)

			// Confirmed at the prompt, yet refused: the references decide.
			_, errOut, err := execCmd(t, tc.cmd(), "y\n", "destroy", tc.item)
			if err == nil {
				t.Fatal("destroy should refuse while the item is still named")
			}
			if !IsUserError(err) {
				t.Errorf("error %q must be a user error (exit 1)", err)
			}
			if !strings.Contains(errOut, tc.referrer) {
				t.Errorf("stderr %q should name the referrer %q", errOut, tc.referrer)
			}
			if out, _, _ := execCmd(t, tc.cmd(), "", "ls"); !strings.Contains(out, tc.item+"\t(custom)") {
				t.Errorf("a refused destroy must keep %s:\n%s", tc.item, out)
			}

			if _, _, err := execCmd(t, tc.cmd(), "", "destroy", "--force", tc.item); err != nil {
				t.Fatalf("destroy --force: %v", err)
			}
			if out, _, _ := execCmd(t, tc.cmd(), "", "ls"); strings.Contains(out, tc.item) {
				t.Errorf("destroy --force should remove %s:\n%s", tc.item, out)
			}
		})
	}
}

// A box manifest that fails to load is reported, never skipped, and its
// references are read loosely, so a broken manifest cannot hide one.
func TestCatalogDestroyReadsAnUnreadableBoxLoosely(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedRefCapability(t)
	path, err := layout.BoxPath("prod", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "schema_version: 1\nname: broken\nspace: prod\ncapabilities: [" + refCapability + "]\nbogus_field: true\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	_, errOut, err := execCmd(t, newCapabilityCmd(), "y\n", "destroy", refCapability)
	if err == nil {
		t.Fatal("destroy should refuse: the unreadable box still grants the capability")
	}
	if !strings.Contains(errOut, "unreadable") || !strings.Contains(errOut, "box prod/broken") {
		t.Errorf("stderr %q should report the unreadable manifest and name it as a referrer", errOut)
	}
}

// A forced scan can still report fields decoded before a type error. Without
// --force, the unknown reference field must stop deletion.
func TestLooseBoxRefsSurvivesAMisshapenField(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := layout.BoxPath("prod", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "account: personal\nfeatures: not-a-list\ncapabilities: [" + refCapability + "]\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := looseBoxRefs("prod", "broken")
	if err == nil {
		t.Fatal("misshapen reference field should report an incomplete read")
	}
	if got.Account != "personal" || len(got.Capabilities) != 1 || got.Capabilities[0] != refCapability {
		t.Errorf("looseBoxRefs = %+v, want the account and the capability despite the misshapen features", got)
	}
}

func TestCatalogDestroyFailsClosedOnUnreadableBoxReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"invalid YAML", "capabilities: [" + refCapability + "]\nports: [\n"},
		{"misshapen reference", "capabilities: " + refCapability + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			seedRefCapability(t)
			path, err := layout.BoxPath("prod", "broken")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.raw), 0o644); err != nil {
				t.Fatal(err)
			}

			_, _, err = execCmd(t, newCapabilityCmd(), "y\n", "destroy", refCapability)
			if err == nil || !IsUserError(err) || !strings.Contains(err.Error(), "cannot determine references") {
				t.Fatalf("destroy error = %v, want a user error for unknown box references", err)
			}
			if _, err := capabilities.Lookup(refCapability); err != nil {
				t.Fatalf("refused destroy removed the capability: %v", err)
			}

			_, stderr, err := execCmd(t, newCapabilityCmd(), "", "destroy", "--force", refCapability)
			if err != nil {
				t.Fatalf("forced destroy: %v", err)
			}
			if !strings.Contains(stderr, "could not be fully read") {
				t.Errorf("forced destroy did not warn about the unreadable box: %q", stderr)
			}
			if _, err := capabilities.Lookup(refCapability); err == nil {
				t.Error("forced destroy should remove the capability")
			}
		})
	}
}
