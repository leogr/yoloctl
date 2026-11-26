package releasebundle

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"strings"
)

// BuildNotices assembles one offline notice document for all four platforms.
// Both architectures' Linux companions ship with every platform archive.
func BuildNotices(project fs.FS) ([]byte, error) {
	toolchain, err := goToolchainFS()
	if err != nil {
		return nil, err
	}
	var text bytes.Buffer
	text.WriteString(`yoloctl third-party licences and notices

The separate LICENSE file covers yoloctl's original code. The sections
below preserve the terms and attributions of third-party components.
The dependency index includes build, test and other platform inputs.
Its byte offsets start at the first byte of the corresponding text
bundle section, immediately after the LICENSES/THIRD_PARTY_LICENSES.txt
heading. Each indexed text can also be found by its SHA-256.

For the bundled Linux kernel, see the source archive and SHA-256 in the
kernel manifest section. The corresponding source, configuration and
recipe are separate release materials.
`)
	writeSection := func(files fs.FS, name, label string) error {
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("assemble %s section %s: %w", NoticesFile, label, err)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return fmt.Errorf("assemble %s: %s is empty", NoticesFile, label)
		}
		text.WriteString("\n===== " + label + " =====\n")
		text.Write(body)
		text.WriteByte('\n')
		return nil
	}
	for _, name := range []string{
		"LICENSES/Apache-2.0.txt",
		"LICENSES/OpenShell-v0.1.10-LICENSE.txt",
		"LICENSES/AIRC-MIT.txt",
		"internal/auditplugin/embedded/NOTICES.txt",
		"third_party/mermaid-text/LICENSE",
		"internal/terminal/wasm/LICENSE-ghostty",
		"internal/terminal/wasm/LICENSE-uucode",
		"internal/terminal/wasm/LICENSE-zig",
		"internal/terminal/wasm/LICENSE_Bjoern_Hoehrmann",
		"internal/terminal/wasm/LICENSE_unicode",
		"assets/vm/firecracker/kernel.LICENSE",
		"assets/vm/firecracker/kernel.json",
		"third_party/ultraviolet/LICENSE",
		"third_party/sftp-v2/LICENSE",
		"LICENSES/INVENTORY.json",
		"LICENSES/THIRD_PARTY_LICENSES.txt",
	} {
		if err := writeSection(project, name, name); err != nil {
			return nil, err
		}
	}
	for _, dir := range []string{"third_party/mermaid-text/licenses", "LICENSES/openshell-runtime"} {
		err := fs.WalkDir(project, dir, func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if name == "third_party/mermaid-text/licenses/manifest.json" {
				return nil
			}
			return writeSection(project, name, name)
		})
		if err != nil {
			return nil, err
		}
	}

	for _, name := range []string{"LICENSE", "PATENTS"} {
		if err := writeSection(toolchain, name, "Go toolchain/"+name); err != nil {
			return nil, err
		}
	}
	err = fs.WalkDir(toolchain, "src/vendor", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		base := path.Base(name)
		if strings.HasPrefix(base, "LICENSE") || base == "PATENTS" || base == "NOTICE" {
			return writeSection(toolchain, name, "Go toolchain/"+name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return text.Bytes(), nil
}

func goToolchainFS() (fs.FS, error) {
	root, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return nil, fmt.Errorf("locate Go toolchain for %s: %w", NoticesFile, err)
	}
	return os.DirFS(strings.TrimSpace(string(root))), nil
}
