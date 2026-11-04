package agentbox

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/crypto/ssh"
)

// This fixture validates nonsecret observations only. The shared manifest is
// fictional test data; no runtime, native activation, install or VM is executed.
func nativeRuntimeFixture(t *testing.T) (spaceenv.Context, *container.Details, []byte, []byte) {
	t.Helper()
	manifest, err := os.ReadFile("../openshell/testdata/qualification-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	hash := func(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
	release, err := openshell.LoadQualifiedRelease(manifest, hash(manifest))
	if err != nil {
		t.Fatal(err)
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	current := spaceenv.Context{Space: "trial", DiskMount: "/var/lib/yoloctl/disks/fixture", DiskID: strings.Repeat("1", 32), DiskUUID: "filesystem-id", UID: 1000, GID: 1000, Generation: "new-controller"}
	binding := openshell.Binding{Space: current.Space, DiskID: current.DiskID, DiskUUID: current.DiskUUID, Box: "fixture", Incarnation: strings.Repeat("2", 32),
		ControllerGeneration: "old-controller", Qualification: release.Identity(), PolicySHA256: hash([]byte("policy-fixture"))}
	in := openshell.BootstrapInput{Binding: binding, UID: 1000, GID: 1000, Workdir: "/workdir/project", ReadOnlyWorkdir: true}
	launch, err := openshell.NewLaunchConfig(in, release, helper.BinarySHA256, false)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := release.Platform(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	var aux struct {
		Pins struct {
			RegoSHA256 string `json:"rego_sha256"`
		} `json:"pins"`
	}
	if err := json.Unmarshal(manifest, &aux); err != nil {
		t.Fatal(err)
	}
	// Fixed public contract fixture for the generation/session binding. The
	// production validator is still exercised on every changed identity below.
	bound, _ := json.Marshal(binding)
	digest := hash(append([]byte("yoloctl-openshell-bootstrap-v2\x00"), bound...))
	session := "12345678-1234-4234-8234-123456789abc"
	generationHash := sha256.Sum256([]byte(digest + "\x00" + session))
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32)).Public())
	if err != nil {
		t.Fatal(err)
	}
	record := openshell.RuntimeRecord{Launch: launch, Generation: "g" + hex.EncodeToString(generationHash[:24]), SessionID: session, Artifacts: platform, Files: map[string]string{}, SSHHostPublicKey: string(ssh.MarshalAuthorizedKey(key))}
	for _, name := range []string{"boundary/bootstrap.json", "boundary/server.crt", "boundary/server.key", "supervisor/runtime-descriptor.json", "supervisor/auth.json", "supervisor/policy.yaml", "supervisor/sandbox-policy.rego", "status/activation.key"} {
		record.Files[name] = hash([]byte("file-fixture-" + name))
	}
	record.Files["supervisor/policy.yaml"] = binding.PolicySHA256
	record.Files["supervisor/sandbox-policy.rego"] = aux.Pins.RegoSHA256
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	args, err := launch.HelperLaunch().Args()
	if err != nil {
		t.Fatal(err)
	}
	privileged := false
	control := filepath.Join(current.DiskMount, "runtime", "openshell", "containers", binding.Incarnation)
	helperPath, _ := openshell.HelperSourceDirectory(helper.BinarySHA256)
	runtimePath, _ := openshell.RuntimeDirectory(release.Identity())
	details := &container.Details{ID: strings.Repeat("a", 64), Name: "fixture", PID: 42, Running: true, StartedAt: "2026-10-30T20:00:00Z", Privileged: &privileged, Command: args,
		Labels: map[string]string{LabelSpace: current.Space, LabelAgentbox: "fixture", openshell.LabelProtocol: openshell.HelperProtocol, openshell.LabelQualification: release.Identity(), openshell.LabelHelperSHA256: helper.BinarySHA256, openshell.LabelIncarnation: binding.Incarnation, openshell.LabelControlRoot: control},
		Mounts: []container.DetailMount{{Source: helperPath, Destination: openshell.HelperDirectory, ReadOnly: true}, {Source: runtimePath, Destination: openshell.ArtifactProjection, ReadOnly: true},
			{Source: control, Destination: openshell.NativeProjection}, {Source: "/fixture/project", Destination: launch.Workdir, ReadOnly: true},
			{Source: sandbox.SocketDirHost("fixture"), Destination: sandbox.SocketDirContainer}}}
	return current, details, raw, manifest
}

func TestNativeRuntimeObservationPreservesExistingGenerationAcrossControllerUpdate(t *testing.T) {
	current, details, raw, manifest := nativeRuntimeFixture(t)
	observed, err := validateOpenShellRuntime(current, "fixture", details, raw, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Record.Launch.Binding.ControllerGeneration == current.Generation || observed.PID != 42 || observed.StartedAt.IsZero() {
		t.Fatal("observation rewrote/guessed the recorded runtime instance")
	}
	if strings.Contains(observed.String(), "auth.json") || strings.Contains(observed.String(), "bootstrap.json") {
		t.Fatal("metadata diagnostic exposed private authority paths")
	}
}

func TestNativeRuntimeMetadataUsesRealFileInfoAndRefusesLinksAndMode(t *testing.T) {
	name := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeMetadataOwner(info, 0o600, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal("real os.FileInfo was rejected", err)
	}
	if err := runtimeMetadataOwner(info, 0o600, os.Geteuid()+1, os.Getegid()); err == nil {
		t.Fatal("different owner accepted")
	}
	if err := runtimeMetadataOwner(info, 0o444, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("unexpected writable metadata mode accepted")
	}
	if err := os.Link(name, name+".link"); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeMetadataOwner(info, 0o600, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("multiply linked authority metadata accepted")
	}
	if err := os.Symlink(name, name+".symlink"); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeMetadata(name+".symlink", 0o600); err == nil {
		t.Fatal("metadata followed a symlink")
	}
}

func TestNativeRuntimeObservationRejectsMixedDiskProcessBindAndManifest(t *testing.T) {
	for name, mutate := range map[string]func(*spaceenv.Context, *container.Details, *[]byte, *[]byte){
		"disk": func(c *spaceenv.Context, _ *container.Details, _ *[]byte, _ *[]byte) { c.DiskUUID = "another" },
		"uid":  func(c *spaceenv.Context, _ *container.Details, _ *[]byte, _ *[]byte) { c.UID++ },
		"control": func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) {
			d.Labels[openshell.LabelControlRoot] = "/var/lib/yoloctl/other/runtime/openshell/containers/" + d.Labels[openshell.LabelIncarnation]
		},
		"missing-pid":      func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.PID = 0 },
		"missing-start":    func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.StartedAt = "" },
		"container-id":     func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.ID = "unknown" },
		"container-name":   func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.Name = "other" },
		"privileged":       func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { *d.Privileged = true },
		"unknown-mode":     func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.Privileged = nil },
		"writable-project": func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.Mounts[3].ReadOnly = false },
		"writable-helper":  func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.Mounts[0].ReadOnly = false },
		"foreign-ssh-bind": func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) {
			d.Mounts[4].Source = sandbox.SocketDirHost("other")
		},
		"read-only-ssh-bind": func(_ *spaceenv.Context, d *container.Details, _ *[]byte, _ *[]byte) { d.Mounts[4].ReadOnly = true },
		"manifest-bytes":     func(_ *spaceenv.Context, _ *container.Details, _ *[]byte, m *[]byte) { *m = append(*m, ' ') },
		"duplicate-json": func(_ *spaceenv.Context, _ *container.Details, r *[]byte, _ *[]byte) {
			*r = []byte(strings.Replace(string(*r), `"schema":2`, `"schema":2,"schema":2`, 1))
		},
		"trailing-json": func(_ *spaceenv.Context, _ *container.Details, r *[]byte, _ *[]byte) {
			*r = append(*r, []byte(`{}`)...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			current, details, raw, manifest := nativeRuntimeFixture(t)
			mutate(&current, details, &raw, &manifest)
			if _, err := validateOpenShellRuntime(current, "fixture", details, raw, manifest); err == nil {
				t.Fatal("mixed native runtime observation accepted")
			}
		})
	}
}
