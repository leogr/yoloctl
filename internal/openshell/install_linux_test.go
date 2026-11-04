package openshell

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Fictional archives contain non-executed ELF metadata only. They qualify
// filesystem/byte checks in a private temp directory, never a real VM release.
func installFixture(t *testing.T) (QualifiedRelease, []byte, map[string][]byte) {
	t.Helper()
	d := releaseFixture(t)
	archives := map[string][]byte{}
	for arch, machine := range map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64} {
		binary := elfMetadataFixture(machine, "")
		platform := d.Pins.Platforms[arch]
		for _, artifact := range []*Artifact{&platform.Boundary, &platform.Supervisor} {
			var buffer bytes.Buffer
			gz := gzip.NewWriter(&buffer)
			tarball := tar.NewWriter(gz)
			if err := tarball.WriteHeader(&tar.Header{Name: artifact.Member, Size: int64(len(binary)), Typeflag: tar.TypeReg, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			if _, err := tarball.Write(binary); err != nil {
				t.Fatal(err)
			}
			if err := tarball.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			archives[artifact.URL] = buffer.Bytes()
			artifact.ArchiveSHA256, artifact.BinarySHA256 = sha256Hex(buffer.Bytes()), sha256Hex(binary)
		}
		platform.BoundaryLoader, platform.SupervisorLoader = "static", "static"
		d.Pins.Platforms[arch] = platform
	}
	subject, err := json.Marshal(d.Pins)
	if err != nil {
		t.Fatal(err)
	}
	d.Qualification.SubjectSHA256 = sha256Hex(subject)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	release, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	return release, []byte("rego-fixture"), archives
}

func installTestRoot(t *testing.T, release QualifiedRelease) *os.File {
	t.Helper()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	t.Cleanup(func() {
		// Only our known published fixture directory, never a symlink target.
		name := filepath.Join(root.Name(), release.Identity())
		if info, err := os.Lstat(name); err == nil && info.IsDir() {
			_ = os.Chmod(name, 0o700)
		}
	})
	return root
}

func fixtureFetcher(archives map[string][]byte) ArtifactFetcher {
	return func(_ context.Context, url string) (io.ReadCloser, error) {
		raw, ok := archives[url]
		if !ok {
			return nil, errors.New("unexpected fixture URL")
		}
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
}

func TestConcurrentInstallPublishesOneCompleteWinner(t *testing.T) {
	release, rego, archives := installFixture(t)
	root := installTestRoot(t, release)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ready := make(chan struct{})
	var started atomic.Int32
	base := fixtureFetcher(archives)
	fetch := func(ctx context.Context, url string) (io.ReadCloser, error) {
		if strings.Contains(url, "openshell-sandbox") {
			if started.Add(1) == 2 {
				close(ready)
			}
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return base(ctx, url)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- installReleaseAt(ctx, root, release, "amd64", rego, fetch, os.Geteuid(), os.Getegid())
		}()
	}
	first, second := <-results, <-results
	if first != nil || second != nil {
		t.Fatal(first, second)
	}
	names, err := root.Readdirnames(-1)
	if err != nil || len(names) != 1 || names[0] != release.Identity() {
		t.Fatal("concurrent installer leaked or replaced a publication", names, err)
	}
}

func TestInstallPublishesOnlyCompleteVerifiedPairsAndNeverReplaces(t *testing.T) {
	release, rego, archives := installFixture(t)
	root := installTestRoot(t, release)
	install := func(fetch ArtifactFetcher) error {
		return installReleaseAt(context.Background(), root, release, "amd64", rego, fetch, os.Geteuid(), os.Getegid())
	}
	if err := install(fixtureFetcher(archives)); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root.Name(), release.Identity(), "openshell-sandbox")
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := install(func(context.Context, string) (io.ReadCloser, error) {
		t.Fatal("verified release was fetched again")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(name)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("existing release was overwritten", err)
	}
	// A cross-architecture reuse must be refused even though its manifest ID
	// matches. This VM directory is for one verified native architecture.
	if err := installReleaseAt(context.Background(), root, release, "arm64", rego, fixtureFetcher(archives), os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("wrong-architecture pair adopted")
	}
}

func TestInstallFailureNeverPublishesOrLeaksStagedFiles(t *testing.T) {
	for _, failure := range []string{"second-fetch", "wrong-hash", "wrong-architecture", "rego", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			release, rego, archives := installFixture(t)
			root := installTestRoot(t, release)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancel" {
				cancel()
			}
			if failure == "rego" {
				rego = []byte("changed")
			}
			if failure == "wrong-hash" {
				url := release.document.Pins.Platforms["amd64"].Supervisor.URL
				archives[url] = append(append([]byte(nil), archives[url]...), 'x')
			}
			if failure == "wrong-architecture" {
				d := release.document
				amd64, arm64 := d.Pins.Platforms["amd64"], d.Pins.Platforms["arm64"]
				amd64.Boundary = arm64.Boundary
				d.Pins.Platforms["amd64"] = amd64
				subject, _ := json.Marshal(d.Pins)
				d.Qualification.SubjectSHA256 = sha256Hex(subject)
				raw, _ := json.Marshal(d)
				var err error
				release, err = LoadQualifiedRelease(raw, sha256Hex(raw))
				if err != nil {
					t.Fatal(err)
				}
			}
			fetch := fixtureFetcher(archives)
			if failure == "second-fetch" {
				base := fetch
				fetch = func(ctx context.Context, url string) (io.ReadCloser, error) {
					if strings.Contains(url, "supervisor") {
						return nil, errors.New("fixture unavailable")
					}
					return base(ctx, url)
				}
			}
			if err := installReleaseAt(ctx, root, release, "amd64", rego, fetch, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("incomplete/unqualified release published")
			}
			names, err := os.ReadDir(root.Name())
			if err != nil || len(names) != 0 {
				t.Fatal("refused release leaked publication or scratch", names, err)
			}
		})
	}
}

func TestInstallRejectsCorruptExistingFilesAndLinksWithoutRepair(t *testing.T) {
	for _, corruption := range []string{"symlink-directory", "missing", "binary-bytes", "writable", "hard-link", "symlink-file", "extra"} {
		t.Run(corruption, func(t *testing.T) {
			release, rego, archives := installFixture(t)
			root := installTestRoot(t, release)
			install := func() error {
				return installReleaseAt(context.Background(), root, release, "amd64", rego, fixtureFetcher(archives), os.Geteuid(), os.Getegid())
			}
			if corruption == "symlink-directory" {
				if err := os.Symlink(t.TempDir(), filepath.Join(root.Name(), release.Identity())); err != nil {
					t.Fatal(err)
				}
				if install() == nil {
					t.Fatal("symlink runtime root adopted")
				}
				return
			}
			if err := install(); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root.Name(), release.Identity())
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "openshell-sandbox")
			switch corruption {
			case "missing":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "binary-bytes":
				if err := os.Chmod(file, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("corrupt"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(file, 0o555); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(file, 0o777); err != nil {
					t.Fatal(err)
				}
			case "hard-link":
				if err := os.Link(file, filepath.Join(root.Name(), "other-link")); err != nil {
					t.Fatal(err)
				}
			case "symlink-file":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("openshell-supervisor", file); err != nil {
					t.Fatal(err)
				}
			case "extra":
				if err := os.WriteFile(filepath.Join(dir, "extra"), []byte("foreign"), 0o444); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			if install() == nil {
				t.Fatal("corrupt release silently repaired or adopted")
			}
		})
	}
}
