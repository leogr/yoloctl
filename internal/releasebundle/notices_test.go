package releasebundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"testing"
)

func TestBuildNoticesUsesPreservedTexts(t *testing.T) {
	project := os.DirFS("../..")
	toolchain, err := goToolchainFS()
	if err != nil {
		t.Fatal(err)
	}
	notices, err := BuildNotices(project)
	if err != nil {
		t.Fatal(err)
	}
	checkSection := func(files fs.FS, name, label string) []byte {
		t.Helper()
		original, err := fs.ReadFile(files, name)
		if err != nil {
			t.Fatal(err)
		}
		_, body, found := bytes.Cut(notices, []byte("\n===== "+label+" =====\n"))
		if !found || !bytes.HasPrefix(body, original) {
			t.Fatalf("missing or altered notice section %q", label)
		}
		return body[:len(original)]
	}
	for _, name := range []string{
		"LICENSES/OpenShell-v0.1.10-LICENSE.txt",
		"LICENSES/openshell-runtime/UPSTREAM-NOTICES.txt",
		"LICENSES/openshell-runtime/zig-0.15.2-musl-COPYRIGHT",
		"internal/auditplugin/embedded/NOTICES.txt",
		"third_party/mermaid-text/LICENSE",
		"third_party/mermaid-text/licenses/rust-1.95.0/COPYRIGHT-library.html",
		"internal/terminal/wasm/LICENSE-ghostty",
		"assets/vm/firecracker/kernel.LICENSE",
	} {
		checkSection(project, name, name)
	}
	checkSection(toolchain, "LICENSE", "Go toolchain/LICENSE")

	const indexName = "LICENSES/INVENTORY.json"
	const bundleName = "LICENSES/THIRD_PARTY_LICENSES.txt"
	index := checkSection(project, indexName, indexName)
	bundle := checkSection(project, bundleName, bundleName)
	type sourceFile struct {
		Path   string `json:"path"`
		Offset int    `json:"payload_offset"`
		Bytes  int    `json:"payload_bytes"`
		SHA256 string `json:"sha256"`
	}
	type component struct {
		Files        []sourceFile `json:"files"`
		LicenseFiles []sourceFile `json:"license_files"`
	}
	var inventory struct {
		Go   []component `json:"go_modules"`
		Rust []component `json:"rust_crates"`
		Git  []component `json:"rust_git_packages"`
	}
	if err := json.Unmarshal(index, &inventory); err != nil {
		t.Fatal(err)
	}
	for _, inputs := range [][]component{inventory.Go, inventory.Rust, inventory.Git} {
		for _, item := range inputs {
			for _, file := range append(item.Files, item.LicenseFiles...) {
				if file.Offset < 0 || file.Offset > len(bundle) || file.Bytes <= 0 || file.Bytes > len(bundle)-file.Offset {
					t.Fatalf("invalid notice index range for %s", file.Path)
				}
				sum := sha256.Sum256(bundle[file.Offset : file.Offset+file.Bytes])
				if hex.EncodeToString(sum[:]) != file.SHA256 {
					t.Fatalf("indexed notice bytes differ for %s at byte %d", file.Path, file.Offset)
				}
			}
		}
	}
}
