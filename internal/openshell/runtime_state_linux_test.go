package openshell

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type materialFixture struct {
	root    *os.File
	release QualifiedRelease
	record  RuntimeRecord
	owners  runtimeOwners
}

func newMaterialFixture(t *testing.T) materialFixture {
	t.Helper()
	release, rego, _ := installFixture(t)
	in := bootstrapInput()
	in.Binding.Qualification = release.Identity()
	policy := []byte("filesystem_policy:\n  include_workdir: false\n  read_only:\n    - /workdir/project\n  read_write:\n    - /home/sandbox\n")
	in.Binding.PolicySHA256 = sha256Hex(policy)
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := NewBootstrap(issuer, in, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	launch, err := NewLaunchConfig(in, release, strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := release.Platform(runtime.GOARCH)
	if err != nil {
		t.Skip("filesystem fixture requires a qualified test architecture")
	}
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	// testing.TempDir's numbered leaf can be 0755; authority fixtures model
	// the explicitly private native directory, not the test harness default.
	if err := root.Chmod(0o700); err != nil {
		t.Fatal(err)
	}
	record := RuntimeRecord{Launch: launch, Generation: bootstrap.descriptor.Generation, SessionID: bootstrap.descriptor.SessionID,
		Artifacts: platform, Files: map[string]string{}, SSHHostPublicKey: string(bootstrap.sshPublicKey)}
	dir := filepath.Join(root.Name(), "generations", record.Generation)
	for _, leaf := range []string{"boundary", "supervisor", "status"} {
		if err := os.MkdirAll(filepath.Join(dir, leaf), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(value any) []byte {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	for name, body := range map[string][]byte{
		"boundary/bootstrap.json": encode(bootstrap.boundary), "boundary/server.crt": bootstrap.certificate, "boundary/server.key": bootstrap.privateKey,
		"supervisor/runtime-descriptor.json": encode(bootstrap.descriptor), "supervisor/auth.json": encode(bootstrap.auth),
		"supervisor/policy.yaml": policy, "supervisor/sandbox-policy.rego": rego, "status/activation.key": bytes.Repeat([]byte{1}, 32),
	} {
		record.Files[name] = sha256Hex(body)
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	uid, gid := os.Geteuid(), os.Getegid()
	fixture := materialFixture{root, release, record, runtimeOwners{uid, gid, uid, gid, uid, gid}}
	fixture.writeRecords(t)
	return fixture
}

func (f materialFixture) path(name string) string {
	return filepath.Join(f.root.Name(), "generations", f.record.Generation, name)
}
func (f materialFixture) writeRecords(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(f.record)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(f.root.Name(), "current.json"), f.path("record.json")} {
		if err := os.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeMaterialUsesOnlyExactGenerationAndRemainsRedacted(t *testing.T) {
	f := newMaterialFixture(t)
	material, err := readRuntimeMaterialAt(f.root, f.release, f.record.Launch.HelperLaunch(), f.owners)
	if err != nil {
		t.Fatal(err)
	}
	if recordIdentity(material.record) != recordIdentity(f.record) || len(material.files) != 8 || !bytes.Contains(material.files["boundary/bootstrap.json"], []byte("test-secret")) {
		t.Fatal("runtime material did not retain exact selected files")
	}
	if got := fmt.Sprintf("%v %#v", material, material); strings.Contains(got, "test-secret") || strings.Contains(got, "PRIVATE KEY") {
		t.Fatal("runtime material formatter exposed credentials")
	}
	// This test exercises only temp-directory files. The PID 1 entry refuses
	// ordinary callers before changing dumpability or touching fixed mounts.
	if _, err := readRuntimeMaterial(f.record.Launch.HelperLaunch(), f.release); err == nil {
		t.Fatal("ordinary process read privileged runtime material")
	}
}

func TestRuntimeMaterialRefusesUnqualifiedSelectionAndPolicyMismatch(t *testing.T) {
	for _, name := range []string{"unqualified", "wrong-helper", "wrong-incarnation", "unknown-field", "duplicate-field", "old-schema", "record-mismatch", "policy-mismatch"} {
		t.Run(name, func(t *testing.T) {
			f := newMaterialFixture(t)
			launch := f.record.Launch.HelperLaunch()
			release := f.release
			switch name {
			case "unqualified":
				release = QualifiedRelease{}
			case "wrong-helper":
				launch.HelperSHA256 = strings.Repeat("b", 64)
			case "wrong-incarnation":
				launch.Incarnation = strings.Repeat("c", 32)
			case "unknown-field", "duplicate-field":
				raw, err := os.ReadFile(filepath.Join(f.root.Name(), "current.json"))
				if err != nil {
					t.Fatal(err)
				}
				field := `"unknown":true,`
				if name == "duplicate-field" {
					field = `"generation":"unused",`
				}
				raw = append([]byte("{"+field), raw[1:]...)
				if err := os.WriteFile(filepath.Join(f.root.Name(), "current.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "old-schema":
				f.record.Launch.Schema = 1
				f.writeRecords(t)
			case "record-mismatch":
				if err := os.WriteFile(f.path("record.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "policy-mismatch":
				body := []byte("filesystem_policy:\n  include_workdir: true\n  read_only: [/workdir/project]\n  read_write: [/workdir/project]\n")
				f.record.Launch.Binding.PolicySHA256 = sha256Hex(body)
				f.record.Files["supervisor/policy.yaml"] = sha256Hex(body)
				// Preserve the generation binding after changing the policy hash.
				digest, err := f.record.Launch.Binding.digest()
				if err != nil {
					t.Fatal(err)
				}
				generation, err := generationForSession(digest, f.record.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Dir(f.path("record.json")), filepath.Join(f.root.Name(), "generations", generation)); err != nil {
					t.Fatal(err)
				}
				f.record.Generation = generation
				if err := os.WriteFile(f.path("supervisor/policy.yaml"), body, 0o600); err != nil {
					t.Fatal(err)
				}
				f.writeRecords(t)
			}
			if _, err := readRuntimeMaterialAt(f.root, release, launch, f.owners); err == nil {
				t.Fatal("invalid runtime selection accepted")
			}
		})
	}
}

func TestRuntimeMaterialRefusesLinksWritersMissingAndChangedAuthority(t *testing.T) {
	for _, name := range []string{"symlink-file", "hardlink-file", "symlink-directory", "writable-directory", "readable-file", "missing-file", "changed-file", "oversize-file", "wrong-owner", "readable-root"} {
		t.Run(name, func(t *testing.T) {
			f := newMaterialFixture(t)
			authority := f.path("supervisor/auth.json")
			var err error
			switch name {
			case "symlink-file", "hardlink-file":
				err = os.Rename(authority, f.path("retained-auth.json"))
				if err != nil {
					t.Fatal(err)
				}
				if name == "symlink-file" {
					err = os.Symlink(f.path("retained-auth.json"), authority)
				} else {
					err = os.Link(f.path("retained-auth.json"), authority)
				}
			case "symlink-directory":
				err = os.Rename(f.path("supervisor"), f.path("retained-supervisor"))
				if err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(f.path("retained-supervisor"), f.path("supervisor"))
			case "writable-directory":
				err = os.Chmod(f.path("supervisor"), 0o777)
			case "readable-file":
				err = os.Chmod(authority, 0o644)
			case "missing-file":
				err = os.Remove(authority)
			case "changed-file":
				err = os.WriteFile(authority, []byte("different"), 0o600)
			case "oversize-file":
				err = os.WriteFile(authority, bytes.Repeat([]byte("x"), (1<<20)+1), 0o600)
			case "wrong-owner":
				f.owners.supervisorUID++
			case "readable-root":
				err = os.Chmod(f.root.Name(), 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readRuntimeMaterialAt(f.root, f.release, f.record.Launch.HelperLaunch(), f.owners); err == nil {
				t.Fatal("untrusted runtime authority accepted")
			}
		})
	}
}

func TestRuntimeAuthorityFIFORefusalIsBounded(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	fifo := filepath.Join(dir.Name(), "authority")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := runtimeReadAt(dir, "authority", os.Geteuid(), os.Getegid(), 1<<20); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("FIFO accepted as runtime authority")
		}
	case <-time.After(2 * time.Second):
		// Release an accidentally blocking reader before failing the regression.
		fd, err := unix.Open(fifo, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		t.Fatal("runtime authority read blocked on a FIFO")
	}
}
