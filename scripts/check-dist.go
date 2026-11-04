//go:build ignore

// Verify final archives without executing them, including ELF/Mach-O and
// companion resolution for both guest architectures.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/releasebundle"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: check-dist DIST_DIR")
	}
	dir := os.Args[1]
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	bundle, version := "", ""
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || filepath.Base(fields[1]) != fields[1] || seen[fields[1]] {
			return fmt.Errorf("invalid SHA256SUMS")
		}
		seen[fields[1]] = true
		raw, err := os.ReadFile(filepath.Join(dir, fields[1]))
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != fields[0] {
			return fmt.Errorf("archive checksum mismatch: %s", fields[1])
		}
		stage, err := os.MkdirTemp("", "ybd-")
		if err != nil {
			return err
		}
		if err := extract(filepath.Join(dir, fields[1]), strings.TrimSuffix(fields[1], ".tar.gz"), stage); err != nil {
			return err
		}
		m, err := releasebundle.Verify(stage)
		if err != nil {
			return err
		}
		if fields[1] != fmt.Sprintf("yoloctl-%s-%s.tar.gz", m.OS, m.Arch) {
			return fmt.Errorf("archive filename identity mismatch")
		}
		if bundle == "" {
			bundle, version = m.Bundle, m.Version
		}
		if m.Bundle != bundle || m.Version != version {
			return fmt.Errorf("archives do not share one build")
		}
		self := filepath.Join(stage, "yoloctl")
		if m.OS == "linux" {
			machine := elf.EM_X86_64
			if m.Arch == "arm64" {
				machine = elf.EM_AARCH64
			}
			if _, err := guestbinary.LinuxExecutable(self, machine, false); err != nil {
				return err
			}
		} else {
			f, err := macho.Open(self)
			if err != nil {
				return err
			}
			want := macho.CpuAmd64
			if m.Arch == "arm64" {
				want = macho.CpuArm64
			}
			if f.Cpu != want {
				return fmt.Errorf("wrong Mach-O architecture")
			}
			signed := false
			for _, load := range f.Loads {
				if f.ByteOrder.Uint32(load.Raw()[:4]) == 0x1d {
					signed = true
				}
			}
			_ = f.Close()
			if m.Arch == "arm64" && !signed {
				return fmt.Errorf("darwin/arm64 lacks LC_CODE_SIGNATURE")
			}
			fmt.Printf("%s/%s LC_CODE_SIGNATURE=%t (not codesign validation)\n", m.OS, m.Arch, signed)
		}
		guestbinary.AMD64SHA256 = m.Files["yoloctl-linux-amd64"]
		guestbinary.ARM64SHA256 = m.Files["yoloctl-linux-arm64"]
		for _, arch := range []string{"amd64", "arm64"} {
			if _, err := guestbinary.Resolve(self, arch); err != nil {
				return err
			}
		}
		if m.OS == "linux" && m.Arch == "amd64" {
			guestbinary.InitAMD64SHA256 = m.Files["yoloctl-vm-init-linux-amd64"]
			if _, err := guestbinary.ResolveInit(self); err != nil {
				return err
			}
		}
		fmt.Println("PASS:", fields[1], "bundle="+bundle)
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
	}
	if len(seen) != 4 {
		return fmt.Errorf("expected exactly four archives")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 5 {
		return fmt.Errorf("unexpected distribution files")
	}
	return nil
}
// extract accepts only regular files directly inside the archive's single
// folder (its version-less name) and writes them flat into dir.
func extract(path, folder, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, inFolder := strings.CutPrefix(h.Name, folder+"/")
		if !inFolder || filepath.Base(name) != name || name == "." || name == ".." || h.Typeflag != tar.TypeReg || h.Size > 512<<20 || seen[name] {
			return fmt.Errorf("unsafe or duplicate archive entry")
		}
		seen[name] = true
		data, err := io.ReadAll(io.LimitReader(tr, 512<<20))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
}
