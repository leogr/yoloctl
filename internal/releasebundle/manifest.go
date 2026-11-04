// Package releasebundle verifies complete, immutable release bundles without
// executing their contents. The archive checksum/attestation authenticates the
// manifest; the manifest binds every payload byte and the linked companions.
package releasebundle

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	identity "github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/strictyaml"
)

type Manifest struct {
	Schema  int               `json:"schema" yaml:"schema"`
	Version string            `json:"version" yaml:"version"`
	Channel string            `json:"channel" yaml:"channel"`
	Bundle  string            `json:"bundle" yaml:"bundle"`
	OS      string            `json:"os" yaml:"os"`
	Arch    string            `json:"arch" yaml:"arch"`
	Files   map[string]string `json:"files" yaml:"files"`
}

func Payload(osname, arch string) ([]string, error) {
	if (osname != "linux" && osname != "darwin") || (arch != "amd64" && arch != "arm64") {
		return nil, fmt.Errorf("unsupported release platform %s/%s", osname, arch)
	}
	names := []string{"INSTALL.txt", "LICENSE", "yoloctl", "yoloctl-linux-amd64", "yoloctl-linux-arm64"}
	if osname == "linux" && arch == "amd64" {
		names = append(names, "yoloctl-vm-init-linux-amd64")
	}
	slices.Sort(names)
	return names, nil
}

func (m Manifest) Validate() error {
	if m.Schema != 1 || !identity.IsRelease(m.Version) || len(m.Version) > 128 || m.Channel != "release" {
		return fmt.Errorf("invalid release manifest identity")
	}
	b, err := hex.DecodeString(m.Bundle)
	if err != nil || len(b) != 16 || strings.ToLower(m.Bundle) != m.Bundle {
		return fmt.Errorf("invalid bundle identity")
	}
	names, err := Payload(m.OS, m.Arch)
	if err != nil {
		return err
	}
	if len(names) != len(m.Files) {
		return fmt.Errorf("incomplete release manifest")
	}
	for _, name := range names {
		sum, err := hex.DecodeString(m.Files[name])
		if err != nil || len(sum) != sha256.Size {
			return fmt.Errorf("invalid checksum for %s", name)
		}
	}
	return nil
}

func (m Manifest) Key() string { return m.Version + "-" + m.OS + "-" + m.Arch + "-" + m.Bundle }

// Verify reads only bounded regular files; links and extra entries are errors.
func Verify(dir string) (Manifest, error) {
	var m Manifest
	raw, err := safepath.ReadFile(dir, "MANIFEST.json", 1<<20)
	if err != nil {
		return m, err
	}
	if !json.Valid(raw) {
		return m, fmt.Errorf("invalid JSON manifest")
	}
	if err := strictyaml.Decode(raw, &m); err != nil {
		return m, err
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m, err
	}
	if len(entries) != len(m.Files)+1 {
		return m, fmt.Errorf("unexpected bundle entries")
	}
	for name, sum := range m.Files {
		data, err := safepath.ReadFile(dir, name, 512<<20)
		if err != nil {
			return m, err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != sum {
			return m, fmt.Errorf("checksum mismatch: %s", name)
		}
	}
	if err := VerifyLinked(dir, m); err != nil {
		return m, err
	}
	return m, nil
}

// LinkerValues inspects Go metadata rather than running an unknown binary.
func LinkerValues(path string) (map[string]string, error) {
	raw, err := safepath.ReadFile(filepath.Dir(path), filepath.Base(path), 512<<20)
	if err != nil {
		return nil, err
	}
	info, err := buildinfo.Read(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, setting := range info.Settings {
		if setting.Key != "-ldflags" {
			continue
		}
		fields := strings.Fields(setting.Value)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "-X" {
				key, value, ok := strings.Cut(fields[i+1], "=")
				if ok {
					values[key] = value
				}
			}
		}
	}
	return values, nil
}

func VerifyLinked(dir string, m Manifest) error {
	const prefix = "github.com/leogr/yoloctl/internal/"
	for _, name := range []string{"yoloctl", "yoloctl-linux-amd64", "yoloctl-linux-arm64"} {
		values, err := LinkerValues(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s metadata: %w", name, err)
		}
		if values[prefix+"buildinfo.Version"] != m.Version || values[prefix+"controller.BuildID"] != m.Bundle {
			return fmt.Errorf("%s linked identity differs from manifest", name)
		}
		if name == "yoloctl" {
			for symbol, file := range map[string]string{"AMD64": "yoloctl-linux-amd64", "ARM64": "yoloctl-linux-arm64", "InitAMD64": "yoloctl-vm-init-linux-amd64"} {
				if sum, ok := m.Files[file]; ok && values[prefix+"guestbinary."+symbol+"SHA256"] != sum {
					return fmt.Errorf("linked companion hash differs: %s", file)
				}
			}
		}
	}
	return nil
}
