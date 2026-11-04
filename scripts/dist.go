//go:build ignore

// Assemble final linked bytes. Darwin executables are never edited or signed.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/releasebundle"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 3 && os.Args[1] == "validate" {
		if !buildinfo.IsRelease(os.Args[2]) || len(os.Args[2]) > 128 {
			return fmt.Errorf("VERSION must be a strict vMAJOR.MINOR.PATCH[-PRERELEASE] tag")
		}
		return nil
	}
	if len(os.Args) != 5 {
		return fmt.Errorf("usage: dist BIN_DIR DIST_DIR VERSION BUNDLE")
	}
	bin, out, version, bundle := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	if !buildinfo.IsRelease(version) {
		return fmt.Errorf("invalid release version")
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	sums := ""
	for _, platform := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"darwin", "amd64"}} {
		m := releasebundle.Manifest{Schema: 1, Version: version, Channel: "release", Bundle: bundle, OS: platform[0], Arch: platform[1], Files: map[string]string{}}
		names, err := releasebundle.Payload(m.OS, m.Arch)
		if err != nil {
			return err
		}
		data := map[string][]byte{}
		for _, name := range names {
			src := filepath.Join(bin, name)
			switch name {
			case "yoloctl":
				src = filepath.Join(bin, "yoloctl-"+m.OS+"-"+m.Arch)
				if m.OS == "linux" {
					src = filepath.Join(bin, "yoloctl-host-linux-"+m.Arch)
				}
			case "LICENSE":
				src = "LICENSE"
			case "INSTALL.txt":
				src = "scripts/INSTALL.txt"
			}
			body, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			data[name] = body
			m.Files[name] = fmt.Sprintf("%x", sha256.Sum256(body))
		}
		if err := m.Validate(); err != nil {
			return err
		}
		// Verify linked stamps/hashes using a private inspection directory without
		// touching the linked source artifacts.
		stage, err := os.MkdirTemp(out, ".verify-")
		if err != nil {
			return err
		}
		for name, body := range data {
			if err := os.WriteFile(filepath.Join(stage, name), body, 0600); err != nil {
				return err
			}
		}
		manifest, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		data["MANIFEST.json"] = append(manifest, '\n')
		if err := os.WriteFile(filepath.Join(stage, "MANIFEST.json"), data["MANIFEST.json"], 0600); err != nil {
			return err
		}
		if _, err := releasebundle.Verify(stage); err != nil {
			return err
		}
		// Keep staging evidence on failure; remove only this known private tree.
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
		names = append(names, "MANIFEST.json")
		slices.Sort(names)
		// Version-less names let releases/latest/download/<name> work; every
		// entry sits in one folder of the same name, so extraction never
		// scatters files into the current directory.
		folder := fmt.Sprintf("yoloctl-%s-%s", m.OS, m.Arch)
		filename := folder + ".tar.gz"
		path := filepath.Join(out, filename)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for _, name := range names {
			mode := int64(0444)
			if name == "yoloctl" || len(name) > 8 && name[:8] == "yoloctl-" {
				mode = 0555
			}
			if err := tw.WriteHeader(&tar.Header{Name: folder + "/" + name, Mode: mode, Size: int64(len(data[name])), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := tw.Write(data[name]); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sums += fmt.Sprintf("%x  %s\n", sha256.Sum256(raw), filename)
	}
	return os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums), 0644)
}
