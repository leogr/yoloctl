package auditplugin

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestEmbeddedPluginPinsAndSource(t *testing.T) {
	var manifest struct {
		Schema                       int
		Name, Version, API, SDK, Zig string
		EventSchema                  string `json:"required_event_schema"`
		Falco                        string `json:"falco_version"`
		Libs                         string `json:"falco_libs"`
		Source                       string `json:"source_sha256"`
	}
	if err := json.Unmarshal(Manifest(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != 1 || manifest.Name != "yolo" || manifest.Version != "1.0.1" || manifest.API != "3.12.0" || manifest.SDK != "falcosecurity/libs@0.26.1" || manifest.Zig != "0.15.2" || manifest.EventSchema != "4.0.0" || manifest.Falco != "0.45.2" || manifest.Libs != "0.26.1" {
		t.Fatal(manifest)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		raw, _, err := Plugin(arch)
		if err != nil {
			t.Fatal(err)
		}
		file, err := elf.NewFile(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		expected := elf.EM_X86_64
		if arch == "arm64" {
			expected = elf.EM_AARCH64
		}
		if file.Type != elf.ET_DYN || file.Machine != expected {
			t.Fatal(arch, file.Type, file.Machine)
		}
		symbols, err := file.DynamicSymbols()
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, symbol := range symbols {
			if symbol.Section != elf.SHN_UNDEF {
				found[symbol.Name] = true
			}
		}
		for _, name := range []string{"plugin_init", "plugin_extract_fields", "plugin_get_fields", "plugin_get_required_api_version", "plugin_get_required_event_schema_version", "plugin_destroy"} {
			if !found[name] {
				t.Fatal("missing ABI export", name)
			}
		}
		deps, err := file.ImportedLibraries()
		if err != nil {
			t.Fatal(err)
		}
		for _, dep := range deps {
			if strings.Contains(dep, "stdc++") || strings.Contains(dep, "libc++") {
				t.Fatal("unshipped C++ runtime dependency", dep)
			}
		}
	}
	_, here, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	source := filepath.Join(repo, "third_party", "audit-plugin")
	paths := []string{"plugin.cpp", "snapshot.hpp", "exports.map", "LICENSE", "include/pins.json", "include/plugin/plugin_api.h", "include/plugin/plugin_types.h"}
	sort.Strings(paths)
	var descriptor strings.Builder
	for _, name := range paths {
		raw, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(&descriptor, "%s\t%x\n", name, sha256.Sum256(raw))
	}
	recipe, err := os.ReadFile(filepath.Join(repo, "scripts", "build-audit-plugin.py"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(&descriptor, "build_recipe\t%x\n", sha256.Sum256(recipe))
	sum := sha256.Sum256([]byte(descriptor.String()))
	if hex.EncodeToString(sum[:]) != manifest.Source {
		t.Fatal("audit plugin source/recipe changed; regenerate with scripts/build-audit-plugin.py")
	}
}

func TestEmbeddedPluginNotices(t *testing.T) {
	var manifest struct {
		Notices string `json:"notices_sha256"`
	}
	if err := json.Unmarshal(Manifest(), &manifest); err != nil {
		t.Fatal(err)
	}
	raw, err := files.ReadFile("embedded/NOTICES.txt")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != manifest.Notices {
		t.Fatal("plugin notices do not match qualification")
	}
	_, here, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	var expected bytes.Buffer
	for _, item := range []struct{ name, path string }{{"yoloctl", "LICENSE"}, {"Falco C ABI", "third_party/audit-plugin/LICENSE"}, {"libcxx", "third_party/audit-plugin/runtime-licenses/libcxx.txt"}, {"libcxxabi", "third_party/audit-plugin/runtime-licenses/libcxxabi.txt"}, {"libunwind", "third_party/audit-plugin/runtime-licenses/libunwind.txt"}, {"zig", "third_party/audit-plugin/runtime-licenses/zig.txt"}} {
		body, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(item.path)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(&expected, "\n==== %s ====\n", item.name)
		expected.Write(body)
	}
	if !bytes.Equal(expected.Bytes(), raw) {
		t.Fatal("plugin runtime notices changed; regenerate")
	}
}
