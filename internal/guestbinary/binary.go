// Package guestbinary verifies the exact Linux companion selected by a host build.
package guestbinary

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Filled by make with the static companions' content hashes.
var AMD64SHA256, ARM64SHA256, InitAMD64SHA256 string

type Artifact struct {
	Path, Sum string
	Dynamic   bool
}

// ResolveInit verifies the small, separately built guest PID 1. Installed
// yoloctl can prepare an image without Go or a repository checkout.
func ResolveInit(self string) (Artifact, error) {
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(filepath.Dir(resolved), "yoloctl-vm-init-linux-amd64")
	if InitAMD64SHA256 == "" {
		return Artifact{}, fmt.Errorf("no verified VM init companion; run make build and install the complete bundle")
	}
	if _, err := LinuxExecutable(path, elf.EM_X86_64, false); err != nil {
		return Artifact{}, fmt.Errorf("VM init companion %s: %w; install the complete yoloctl bundle", path, err)
	}
	sum, err := SHA256File(path)
	if err != nil {
		return Artifact{}, err
	}
	if sum != InitAMD64SHA256 {
		return Artifact{}, fmt.Errorf("VM init companion does not match this build; install the complete yoloctl bundle")
	}
	return Artifact{Path: path, Sum: sum}, nil
}

// Resolve uses the guest architecture, including for an emulated host CLI.
// Bundles prefer the matching static companion. Standalone Linux executables
// can copy themselves; dynamically linked ones must pass a guest startup probe.
func Resolve(self, uname string) (Artifact, error) {
	var arch string
	var machine elf.Machine
	var expected string
	switch uname {
	case "x86_64", "amd64":
		arch, machine = "amd64", elf.EM_X86_64
		expected = AMD64SHA256
	case "aarch64", "arm64":
		arch, machine = "arm64", elf.EM_AARCH64
		expected = ARM64SHA256
	default:
		return Artifact{}, fmt.Errorf("session: unsupported VM architecture %q; supported architectures: amd64, arm64", uname)
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return Artifact{}, fmt.Errorf("session: resolve host executable: %w", err)
	}
	dynamic, selfErr := LinuxExecutable(resolved, machine, true)
	path := filepath.Join(filepath.Dir(resolved), "yoloctl-linux-"+arch)
	// An unstamped Linux development build must use itself, never a stale
	// companion left by a previous make. A copied standalone bundle host can
	// also use itself when its same-architecture companion is absent.
	_, pathErr := os.Stat(path)
	if selfErr == nil && (expected == "" || os.IsNotExist(pathErr)) {
		sum, err := SHA256File(resolved)
		return Artifact{resolved, sum, dynamic}, err
	}
	if expected == "" {
		return Artifact{}, fmt.Errorf("session: no verified Linux/%s companion for this host executable; run make build and install the complete bundle", arch)
	}
	if _, err := LinuxExecutable(path, machine, false); err != nil {
		return Artifact{}, fmt.Errorf("session: unusable Linux/%s guest binary %s: %w; run make build and install the complete bundle", arch, path, err)
	}
	sum, err := SHA256File(path)
	if err != nil {
		return Artifact{}, err
	}
	if sum != expected {
		return Artifact{}, fmt.Errorf("session: Linux/%s companion %s does not match this host build; run make build and install the complete bundle", arch, path)
	}
	return Artifact{Path: path, Sum: sum}, nil
}

func LinuxExecutable(path string, machine elf.Machine, allowDynamic bool) (bool, error) {
	f, err := elf.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != machine || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) || (f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX) {
		return false, fmt.Errorf("not a Linux executable for %s", machine)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			if !allowDynamic {
				return false, fmt.Errorf("requires a dynamic loader; rebuild with CGO_ENABLED=0")
			}
			return true, nil
		}
	}
	return false, nil
}

func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
