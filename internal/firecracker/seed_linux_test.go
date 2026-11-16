//go:build linux && amd64

package firecracker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	backendfile "github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/leogr/yoloctl/internal/guestbinary"
)

// seedFixture is an instance dir as Create leaves it: a freshly formatted
// system disk plus its creation record.
func seedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	disk := filepath.Join(dir, "system.ext4")
	if err := sparse(disk, 256<<20); err != nil {
		t.Fatal(err)
	}
	if err := formatDisk(context.Background(), disk); err != nil {
		t.Fatal(err)
	}
	if err := recordPristine(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeArtifact makes bootArtifact return a random "guest binary".
func fakeArtifact(t *testing.T, size int) guestbinary.Artifact {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "yoloctl-linux-amd64")
	if err := os.WriteFile(p, data, 0755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return guestbinary.Artifact{Path: p, Sum: hex.EncodeToString(sum[:])}
}

func useArtifact(t *testing.T, a guestbinary.Artifact) {
	t.Helper()
	old := bootArtifact
	bootArtifact = func(Config) (guestbinary.Artifact, error) { return a, nil }
	t.Cleanup(func() { bootArtifact = old })
}

func quietSeed(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	old := seedReport
	seedReport = func(format string, args ...any) { lines = append(lines, format) }
	t.Cleanup(func() { seedReport = old })
	return &lines
}

func diskSum(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "system.ext4"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func seededBytes(t *testing.T, dir string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "system.ext4"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, _ := f.Stat()
	fsys, err := ext4.Read(backendfile.New(f, true), st.Size(), 0, 512)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fsys.ReadFile(seededBinary)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func seedEntries(t *testing.T, dir string) []string {
	t.Helper()
	found, err := seedOrphans(dir)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// Create's record matches the formatted disk exactly and parses strictly.
func TestCreateRecordsPristineSystemDisk(t *testing.T) {
	dir := seedFixture(t)
	m, found, err := readPristine(dir)
	if err != nil || !found || m.State != pristineState || m.ArtifactSHA256 != "" {
		t.Fatalf("marker %+v found=%t err=%v", m, found, err)
	}
	id, err := identityOf(filepath.Join(dir, "system.ext4"))
	if err != nil || id != m.Identity {
		t.Fatalf("identity %+v != %+v (%v)", id, m.Identity, err)
	}
	if err := os.WriteFile(filepath.Join(dir, pristineName), []byte(`{"schema":1,"state":"formatted","identity":{},"extra":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := readPristine(dir); !found || err == nil {
		t.Fatal("unknown marker fields were accepted")
	}
}

// A never-opened disk receives exactly the start's artifact; the image keeps
// formatDisk's journalled parameters and passes a read-only fsck.
func TestSeedInstallsStartArtifactIntoPristineDisk(t *testing.T) {
	dir := seedFixture(t)
	a := fakeArtifact(t, 3<<20)
	useArtifact(t, a)
	lines := quietSeed(t)
	if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(a.Path)
	if !bytes.Equal(seededBytes(t, dir), want) {
		t.Fatal("seeded binary differs from the artifact")
	}
	m, found, err := readPristine(dir)
	if err != nil || !found || m.State != seededState || m.ArtifactSHA256 != a.Sum {
		t.Fatalf("marker after seeding: %+v %v", m, err)
	}
	if id, _ := identityOf(filepath.Join(dir, "system.ext4")); id != m.Identity {
		t.Fatal("marker does not describe the published disk")
	}
	if len(seedEntries(t, dir)) != 0 {
		t.Fatal("seed temp left behind")
	}
	joined := strings.Join(*lines, "\n")
	for _, stage := range []string{"build begin", "verify end", "publish end"} {
		if !strings.Contains(joined, stage) {
			t.Fatalf("missing stage record %q in %q", stage, joined)
		}
	}
	if fsck, err := exec.LookPath("e2fsck"); err == nil {
		out, err := exec.Command(fsck, "-fn", filepath.Join(dir, "system.ext4")).CombinedOutput()
		if err != nil {
			t.Fatalf("e2fsck: %v\n%s", err, out)
		}
	}
	if dump, err := exec.LookPath("dumpe2fs"); err == nil {
		out, err := exec.Command(dump, "-h", filepath.Join(dir, "system.ext4")).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "has_journal") {
			t.Fatalf("seeded disk lost its journal: %v\n%s", err, out)
		}
	}
}

// Ambiguous provenance never replaces the disk and deletes nothing.
func TestSeedSkipsWithoutExactProvenance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"no marker", func(t *testing.T, dir string) { _ = os.Remove(filepath.Join(dir, pristineName)) }},
		{"bad marker", func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, pristineName), []byte("{"), 0600)
		}},
		{"disk changed", func(t *testing.T, dir string) {
			f, err := os.OpenFile(filepath.Join(dir, "system.ext4"), os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteAt([]byte{1}, 200<<20)
			_ = f.Close()
		}},
		{"orphan seed", func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, seedPrefix+"unexplained"+seedSuffix), []byte("x"), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := seedFixture(t)
			tc.setup(t, dir)
			useArtifact(t, fakeArtifact(t, 1<<20))
			quietSeed(t)
			before, orphans := diskSum(t, dir), seedEntries(t, dir)
			if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil {
				t.Fatal(err)
			}
			if diskSum(t, dir) != before || len(seedEntries(t, dir)) != len(orphans) {
				t.Fatal("ambiguous provenance replaced the disk or removed a file")
			}
		})
	}
}

// A seeded, never-booted disk is re-seeded for a different build, and left
// alone for the same build.
func TestSeedReseedsOnlyForADifferentBuild(t *testing.T) {
	dir := seedFixture(t)
	quietSeed(t)
	a, b := fakeArtifact(t, 1<<20), fakeArtifact(t, 1<<20)
	useArtifact(t, a)
	if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil {
		t.Fatal(err)
	}
	seeded, _ := identityOf(filepath.Join(dir, "system.ext4"))
	if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil {
		t.Fatal(err)
	}
	if again, _ := identityOf(filepath.Join(dir, "system.ext4")); again != seeded {
		t.Fatal("same build rewrote the seeded disk")
	}
	useArtifact(t, b)
	if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(b.Path)
	if !bytes.Equal(seededBytes(t, dir), want) {
		t.Fatal("a later build was not re-seeded")
	}
}

// A failed or cancelled re-seed leaves the previous disk and marker intact
// and removes only its own temp (the seeded-A then failed-B case: the next
// successful start's EnsureVMBinary replaces A).
func TestSeedFailureKeepsDiskAndMarker(t *testing.T) {
	dir := seedFixture(t)
	quietSeed(t)
	a := fakeArtifact(t, 1<<20)
	useArtifact(t, a)
	if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil {
		t.Fatal(err)
	}
	before := diskSum(t, dir)
	marker, _ := os.ReadFile(filepath.Join(dir, pristineName))
	wrong := fakeArtifact(t, 1<<20)
	wrong.Sum = strings.Repeat("0", 64) // verification must fail
	useArtifact(t, wrong)
	if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil {
		t.Fatalf("a verification failure must fall back, not fail the boot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	useArtifact(t, fakeArtifact(t, 1<<20))
	if err := seedSystemDisk(ctx, dir, Config{}); err == nil {
		t.Fatal("cancellation must stop the helper")
	}
	after, _ := os.ReadFile(filepath.Join(dir, pristineName))
	if diskSum(t, dir) != before || !bytes.Equal(marker, after) || len(seedEntries(t, dir)) != 0 {
		t.Fatal("a failed re-seed changed the disk or marker, or left its temp")
	}
}

// Invalidation is durable and fails closed.
func TestInvalidatePristineFailsClosed(t *testing.T) {
	dir := seedFixture(t)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if os.Getuid() != 0 {
		if err := invalidatePristine(dir); err == nil {
			t.Fatal("an unremovable marker did not stop the VMM start")
		}
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := invalidatePristine(dir); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := readPristine(dir); found {
		t.Fatal("marker survived invalidation")
	}
	quietSeed(t)
	before := diskSum(t, dir)
	useArtifact(t, fakeArtifact(t, 1<<20))
	if err := seedSystemDisk(context.Background(), dir, Config{}); err != nil || diskSum(t, dir) != before {
		t.Fatal("a disk without a marker was seeded")
	}
}
