//go:build linux && amd64

package firecracker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	backendfile "github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/leogr/yoloctl/internal/guestbinary"
)

func TestBundledKernelMatchesRecipe(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if err := unpackPinnedKernel(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := guestbinary.LinuxExecutable(filepath.Join(dir, "kernel"), elf.EM_X86_64, false); err != nil {
		t.Fatal(err)
	}
}

func TestImageTreeGuestLinks(t *testing.T) {
	tree := imageTree{}
	tree.link("lib", "usr/lib")
	tree.link("usr/lib/libx.so", "/usr/lib/libx.so.1")
	tree.put("usr/lib/libx.so.1", 0644, []byte("guest"))
	e, err := tree.resolve("/lib/libx.so")
	if err != nil || e.header.Name != "usr/lib/libx.so.1" {
		t.Fatalf("guest resolution: %v %v", e.header, err)
	}
	tree.link("escape", "../host")
	if _, err := tree.resolve("escape"); err == nil {
		t.Fatal("accepted root escape")
	}
	tree.link("loop", "loop")
	if _, err := tree.resolve("loop"); err == nil {
		t.Fatal("accepted link loop")
	}
	if err := tree.verifyRuntime(); err == nil {
		t.Fatal("accepted incomplete ELF closure")
	}
}

func TestPackageCannotExtractThroughHostLinks(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, h := range []tar.Header{
		{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/tmp", Mode: 0777},
		{Name: "escape/file", Typeflag: tar.TypeReg, Size: 3, Mode: 0644},
	} {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write([]byte("abc"))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	tree := imageTree{}
	if err := tree.tar(context.Background(), &archive, stage, func(s string) string { return s }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Type().IsRegular() {
		t.Fatal("archive links reached host filesystem")
	}
	for _, name := range []string{"../bad", "/absolute", "x/../../bad", "x\\bad"} {
		if _, err := imageName(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

// Opt-in real-input acceptance: no executable on PATH, no prepared image, no
// compiler cache. Only pinned upstream inputs may be preseeded for offline use.
func TestPortableImageAssembly(t *testing.T) {
	output := os.Getenv("YOLOCTL_TEST_PORTABLE_OUTPUT")
	if output == "" {
		t.Skip("real image assembly is opt-in")
	}
	initPath := os.Getenv("YOLOCTL_TEST_PORTABLE_INIT")
	cache := os.Getenv("YOLOCTL_TEST_PORTABLE_CACHE")
	if initPath == "" || cache == "" {
		t.Fatal("explicit init and private cache required")
	}
	sum, err := guestbinary.SHA256File(initPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if err = assembleLocalImage(context.Background(), output, cache, guestbinary.Artifact{Path: initPath, Sum: sum}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(output, "image.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = VerifyImage(output); err != nil {
		t.Fatal(err)
	}
	verifyImageTrustStore(t, filepath.Join(output, "rootfs.ext4"))
}

// verifyImageTrustStore reads the assembled root filesystem: every configured
// root has one link, one hash link and one bundle entry, and no retired root
// remains in the bundle.
func verifyImageTrustStore(t *testing.T, rootfs string) {
	t.Helper()
	f, err := os.Open(rootfs)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := ext4.Read(backendfile.New(f, true), st.Size(), 0, 512)
	if err != nil {
		t.Fatal(err)
	}
	conf, err := fsys.ReadFile(certsConf)
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]bool{}
	for _, line := range strings.Split(string(conf), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			links["/"+caCertificatesDir+"/"+line] = true
		}
	}
	bundle, err := fsys.ReadFile(certsBundle)
	if err != nil {
		t.Fatal(err)
	}
	roots := 0
	for block, rest := pem.Decode(bundle); block != nil; block, rest = pem.Decode(rest) {
		roots++
		sum := sha256.Sum256(block.Bytes)
		if name, ok := distrustedRoots[hex.EncodeToString(sum[:])]; ok {
			t.Fatalf("assembled bundle trusts %s", name)
		}
	}
	entries, err := fsys.ReadDir(etcCertsDir)
	if err != nil {
		t.Fatal(err)
	}
	pems := map[string]string{}
	var hashes []string
	for _, e := range entries {
		name := e.Name()
		if name == path.Base(certsBundle) {
			continue
		}
		target, err := fsys.ReadLink(etcCertsDir + "/" + name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		switch {
		case hashLinkName.MatchString(name):
			hashes = append(hashes, name)
		case strings.HasSuffix(name, ".pem") && links[target]:
			pems[name] = target
		default:
			t.Fatalf("unexpected certificate store entry %s -> %s", name, target)
		}
	}
	for _, name := range hashes {
		target, _ := fsys.ReadLink(etcCertsDir + "/" + name)
		// go-diskfs paths are relative to the filesystem root.
		data, err := fsys.ReadFile(strings.TrimPrefix(pems[target], "/"))
		if err != nil {
			t.Fatalf("%s -> %s: %v", name, target, err)
		}
		block, _ := pem.Decode(data)
		subject, err := certificateSubject(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := opensslSubjectHash(subject)
		if err != nil || !strings.HasPrefix(name, fmt.Sprintf("%08x.", hash)) {
			t.Fatalf("hash link %s does not match %s: %v", name, target, err)
		}
	}
	if len(links) == 0 || roots != len(links) || len(pems) != len(links) || len(hashes) != len(links) {
		t.Fatalf("store has %d roots, %d bundle entries, %d links and %d hash links", len(links), roots, len(pems), len(hashes))
	}
}
