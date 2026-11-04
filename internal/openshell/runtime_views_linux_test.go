package openshell

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRuntimeSharedPreparationIsFreshAndPreservesExistingState(t *testing.T) {
	status, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = status.Close() }()
	shared, err := prepareRuntimeSharedAt(status, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shared.close() }()
	for name, mode := range map[string]uint32{"runtime-control": 0o711, "runtime-supervisor": 0o700, "runtime-supervisor/tmp": 0o700, "runtime-supervisor/ssh": 0o700, "runtime-supervisor/proxy-tls": 0o700} {
		info, err := os.Stat(filepath.Join(status.Name(), name))
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if stat.Mode&0o7777 != mode || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
			t.Fatal("private leaf changed owner or permissions", name)
		}
	}
	retained := filepath.Join(status.Name(), "runtime-control", "retained")
	if err := os.WriteFile(retained, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRuntimeSharedAt(status, os.Geteuid(), os.Getegid()); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing runtime was adopted", err)
	}
	if raw, err := os.ReadFile(retained); err != nil || string(raw) != "must survive" {
		t.Fatal("existing runtime state changed")
	}
}

func TestRuntimeViewsCopyOnlyTheirSelectedAuthority(t *testing.T) {
	f := newMaterialFixture(t)
	material, err := readRuntimeMaterialAt(f.root, f.release, f.record.Launch.HelperLaunch(), f.owners)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []runtimeChildRole{runtimeSupervisor, runtimeBoundary} {
		t.Run(string(role), func(t *testing.T) {
			payload, err := material.childPayload(role)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			// File-only tempdir view: no mount/unshare, no root identities and no
			// production projection is touched by this regression.
			target := "config"
			if err := createViewPath(root, target, 0o700, os.Geteuid(), os.Getegid()); err != nil {
				t.Fatal(err)
			}
			if err := writeRuntimeViewFiles(root, payload, string(role), target, os.Geteuid(), os.Getegid(), 0o600); err != nil {
				t.Fatal(err)
			}
			names, err := os.ReadDir(filepath.Join(root.Name(), target))
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != len(payload.Files) {
				t.Fatal("unexpected private view members")
			}
			for _, name := range names {
				raw, err := os.ReadFile(filepath.Join(root.Name(), target, name.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, payload.Files[string(role)+"/"+name.Name()]) {
					t.Fatal("private view bytes changed")
				}
			}
			// Publication is create-only: neither retries nor a second role can
			// overwrite an already selected credential file.
			if err := writeRuntimeViewFiles(root, payload, string(role), target, os.Geteuid(), os.Getegid(), 0o600); !errors.Is(err, os.ErrExist) {
				t.Fatal("private authority overwrite allowed", err)
			}
		})
	}
}
