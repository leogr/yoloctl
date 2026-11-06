package assets

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// Inspect the x86 guest artifact on every host, including arm64 development
// boxes. This is an ELF/configuration check, not a running probe load test.
func TestBundledKernelRetainsAuditBTF(t *testing.T) {
	root := "vm/firecracker/"
	raw, err := os.ReadFile(root + "kernel.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		KernelSHA256 string `json:"kernel_sha256"`
		ConfigSHA256 string `json:"config_sha256"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(root + "kernel.config")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(config)) != manifest.ConfigSHA256 {
		t.Fatal("configuration differs from bundled provenance")
	}
	for _, option := range []string{"BPF_SYSCALL", "BPF_JIT", "BPF_EVENTS", "PERF_EVENTS", "KPROBE_EVENTS", "UPROBE_EVENTS", "FTRACE_SYSCALLS", "TRACEPOINTS", "DEBUG_INFO_DWARF4", "DEBUG_INFO_BTF"} {
		if !strings.Contains("\n"+string(config), "\nCONFIG_"+option+"=y\n") {
			t.Fatal("missing built-in audit requirement", option)
		}
	}
	f, err := os.Open(root + "kernel-linux-amd64.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	kernel, err := io.ReadAll(io.LimitReader(gz, 128<<20+1))
	if err != nil || len(kernel) > 128<<20 {
		t.Fatal("invalid or oversized kernel", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(kernel)) != manifest.KernelSHA256 {
		t.Fatal("kernel differs from bundled provenance")
	}
	image, err := elf.NewFile(bytes.NewReader(kernel))
	if err != nil {
		t.Fatal(err)
	}
	if image.Machine != elf.EM_X86_64 || image.Section(".BTF") == nil || image.Section(".BTF").Size == 0 || image.Section(".debug_info") != nil {
		t.Fatal("guest kernel must retain built-in BTF without standalone DWARF")
	}
}
