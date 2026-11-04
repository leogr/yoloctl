package openshell

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeChildrenReceiveDisjointSealedCredentialSets(t *testing.T) {
	f := newMaterialFixture(t)
	material, err := readRuntimeMaterialAt(f.root, f.release, f.record.Launch.HelperLaunch(), f.owners)
	if err != nil {
		t.Fatal(err)
	}
	material.boundaryEnv, err = captureBoundaryEnvironment([]string{"PATH=/image/tools:/usr/bin", "PRIVATE_LITERAL=boundary-only-test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []runtimeChildRole{runtimeSupervisor, runtimeBoundary} {
		t.Run(string(role), func(t *testing.T) {
			payload, err := material.childPayload(role)
			if err != nil {
				t.Fatal(err)
			}
			if err := payload.validate(f.release, f.record.Launch.HelperLaunch(), role); err != nil {
				t.Fatal(err)
			}
			for name := range payload.Files {
				if !strings.HasPrefix(name, string(role)+"/") || strings.Contains(name, "activation") {
					t.Fatal("child received another authority's bytes")
				}
			}
			file, err := sealedRuntimeChild(payload)
			if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS) {
				t.Skipf("private sealed-memory transport unavailable in this executor; native infrastructure check required: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			// Deliberately move the shared offset; the reader must use the whole
			// sealed content rather than silently accepting a shortened frame.
			if _, err := file.Seek(9, 0); err != nil {
				t.Fatal(err)
			}
			got, err := readRuntimeChild(file, os.Geteuid(), os.Getegid(), f.release, f.record.Launch.HelperLaunch(), role)
			if err != nil {
				t.Fatal(err)
			}
			if recordIdentity(got.Record) != recordIdentity(f.record) || got.Role != role {
				t.Fatal("private child generation/role changed")
			}
			if role == runtimeBoundary && !slices.Equal(got.BoundaryEnv, material.boundaryEnv) || role == runtimeSupervisor && len(got.BoundaryEnv) != 0 {
				t.Fatal("sealed transport lost boundary environment or exposed it to supervisor")
			}
			for name := range got.Files {
				if !strings.HasPrefix(name, string(role)+"/") || strings.Contains(name, "activation") {
					t.Fatal("child received another authority's bytes")
				}
			}
			if _, err := file.WriteAt([]byte("x"), 0); !errors.Is(err, unix.EPERM) {
				t.Fatal("sealed credentials remained mutable", err)
			}
			if err := file.Truncate(0); !errors.Is(err, unix.EPERM) {
				t.Fatal("sealed credentials remained truncatable", err)
			}
			if got := fmt.Sprintf("%v %#v", got, got); strings.Contains(got, "test-secret") || strings.Contains(got, "PRIVATE KEY") {
				t.Fatal("child credentials exposed by formatting")
			}
		})
	}
}

func TestRuntimeChildRefusesRoleEscalationAndSourceChanges(t *testing.T) {
	for _, name := range []string{"other-role", "extra-authority", "missing-authority", "changed-authority", "wrong-generation", "wrong-qualification", "ordinary-file", "unsealed-memory"} {
		t.Run(name, func(t *testing.T) {
			f := newMaterialFixture(t)
			material, err := readRuntimeMaterialAt(f.root, f.release, f.record.Launch.HelperLaunch(), f.owners)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := material.childPayload(runtimeBoundary)
			if err != nil {
				t.Fatal(err)
			}
			launch := f.record.Launch.HelperLaunch()
			switch name {
			case "other-role":
				payload.Role = runtimeSupervisor
			case "extra-authority":
				payload.Files["status/activation.key"] = material.files["status/activation.key"]
			case "missing-authority":
				delete(payload.Files, "boundary/server.key")
			case "changed-authority":
				payload.Files["boundary/server.key"] = []byte("different")
			case "wrong-generation":
				launch.Incarnation = strings.Repeat("f", 32)
			case "wrong-qualification":
				launch.Qualification = strings.Repeat("e", 64)
			}
			if name != "ordinary-file" && name != "unsealed-memory" {
				if err := payload.validate(f.release, launch, runtimeBoundary); err == nil {
					t.Fatal("untrusted child authority accepted before channel transport")
				}
			}
			var file *os.File
			switch name {
			case "ordinary-file":
				file, err = os.CreateTemp(t.TempDir(), "ordinary")
				if err == nil {
					_, err = file.WriteString("{}")
					if err == nil {
						err = file.Chmod(0o400)
					}
				}
			case "unsealed-memory":
				var fd int
				fd, err = unix.MemfdCreate("unsealed-fixture", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
				if err == nil {
					file = os.NewFile(uintptr(fd), "unsealed")
					_, err = file.WriteString("{}")
					if err == nil {
						err = file.Chmod(0o400)
					}
				}
			default:
				file, err = sealedRuntimeChild(payload)
			}
			if name != "ordinary-file" && (errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS)) {
				t.Skipf("private sealed-memory transport unavailable in this executor; native infrastructure check required: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if _, err := readRuntimeChild(file, os.Geteuid(), os.Getegid(), f.release, launch, runtimeBoundary); err == nil {
				t.Fatal("untrusted child authority accepted")
			}
		})
	}
}
