//go:build linux && amd64

package firecracker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backendfile "github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"golang.org/x/sys/unix"
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

func quietSeed(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	old := prepReport
	prepReport = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { prepReport = old })
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
	art := a
	lines := quietSeed(t)
	if err := seedSystemDisk(context.Background(), dir, art); err != nil {
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
	for _, stage := range []string{"system disk seed build begin", "system disk seed verify end", "system disk seed publish end"} {
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
			art := fakeArtifact(t, 1<<20)
			quietSeed(t)
			before, orphans := diskSum(t, dir), seedEntries(t, dir)
			if err := seedSystemDisk(context.Background(), dir, art); err != nil {
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
	art := a
	if err := seedSystemDisk(context.Background(), dir, art); err != nil {
		t.Fatal(err)
	}
	seeded, _ := identityOf(filepath.Join(dir, "system.ext4"))
	if err := seedSystemDisk(context.Background(), dir, art); err != nil {
		t.Fatal(err)
	}
	if again, _ := identityOf(filepath.Join(dir, "system.ext4")); again != seeded {
		t.Fatal("same build rewrote the seeded disk")
	}
	art = b
	if err := seedSystemDisk(context.Background(), dir, art); err != nil {
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
	art := a
	if err := seedSystemDisk(context.Background(), dir, art); err != nil {
		t.Fatal(err)
	}
	before := diskSum(t, dir)
	marker, _ := os.ReadFile(filepath.Join(dir, pristineName))
	wrong := fakeArtifact(t, 1<<20)
	wrong.Sum = strings.Repeat("0", 64) // verification must fail
	art = wrong
	if err := seedSystemDisk(context.Background(), dir, art); err != nil {
		t.Fatalf("a verification failure must fall back, not fail the boot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	art = fakeArtifact(t, 1<<20)
	if err := seedSystemDisk(ctx, dir, art); err == nil {
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
	art := fakeArtifact(t, 1<<20)
	if err := seedSystemDisk(context.Background(), dir, art); err != nil || diskSum(t, dir) != before {
		t.Fatal("a disk without a marker was seeded")
	}
}

// Preparation records written to vm.log are what the boot watch counts: a
// stage's completed work keeps it alive, and silence trips the idle limit.
func TestPrepRecordsFeedTheBootWatchUntilSilence(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vm.log")
	if err := os.WriteFile(logPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	old, oldGap := prepReport, prepProgressGap
	prepReport = func(format string, args ...any) { _, _ = fmt.Fprintf(log, format+"\n", args...) }
	prepProgressGap = 0
	t.Cleanup(func() { prepReport, prepProgressGap = old, oldGap })
	t0 := time.Now()
	watch, err := newBootActivity(logPath, t0)
	if err != nil {
		t.Fatal(err)
	}
	p := newPrepProgress("synthetic stage")
	at := t0
	for i := 0; i < 3; i++ { // completed work every 100 s, beyond one idle window in total
		at = at.Add(100 * time.Second)
		p.add(4096)
		if err := watch.observe(logPath, at); err != nil {
			t.Fatalf("progressing preparation tripped the watch: %v", err)
		}
	}
	if err := watch.observe(logPath, at.Add(bootIdleTimeout)); err == nil {
		t.Fatal("a silent preparation did not reach the idle limit")
	}
}

// Formatting writes, the binary copy and the verify read all report
// completed bytes, not only the binary's source reads.
func TestSeedReportsFormatAndVerifyProgress(t *testing.T) {
	dir := seedFixture(t)
	lines := quietSeed(t)
	oldGap := prepProgressGap
	prepProgressGap = 0
	t.Cleanup(func() { prepProgressGap = oldGap })
	if err := seedSystemDisk(context.Background(), dir, fakeArtifact(t, 1<<20)); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*lines, "\n")
	for _, stage := range []string{"system disk seed build progress", "system disk seed verify progress"} {
		if !strings.Contains(joined, stage) {
			t.Fatalf("no %q record in %q", stage, joined)
		}
	}
}

// The helper's instance lock excludes seeding: a held lock stops Helper
// before it reads the marker or touches the disk.
func TestHelperWithHeldLockDoesNotSeed(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	dir, err := Driver{}.InstanceDir("dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
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
	lock, err := lockFile(filepath.Join(dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	quietSeed(t)
	before := diskSum(t, dir)
	marker, _ := os.ReadFile(filepath.Join(dir, pristineName))
	if err := Helper(context.Background(), "dev"); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("helper ran beside a held instance lock: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, pristineName))
	if diskSum(t, dir) != before || !bytes.Equal(marker, after) || len(seedEntries(t, dir)) != 0 {
		t.Fatal("seeding ran without the instance lock")
	}
}

// The backing-storage descriptor names the published (seeded) inode, never
// the replaced one.
func TestPrepareSystemDiskOpensTheFinalInode(t *testing.T) {
	dir := seedFixture(t)
	quietSeed(t)
	dataDir := t.TempDir()
	if err := sparse(filepath.Join(dataDir, "data.ext4"), 1<<20); err != nil {
		t.Fatal(err)
	}
	old, _ := identityOf(filepath.Join(dir, "system.ext4"))
	storage, err := prepareSystemDisk(context.Background(), dir, dataDir, fakeArtifact(t, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.close()
	var opened unix.Stat_t
	if err := unix.Fstat(int(storage.files[0].Fd()), &opened); err != nil {
		t.Fatal(err)
	}
	final, _ := identityOf(filepath.Join(dir, "system.ext4"))
	if opened.Ino != final.Inode || final.Inode == old.Inode {
		t.Fatalf("descriptor inode %d, final %d, replaced %d", opened.Ino, final.Inode, old.Inode)
	}
}

// Interrupted publication: the marker already describes a seed that never
// replaced system.ext4. The disk, the seed file and the marker stay.
func TestInterruptedPublicationIsSkipped(t *testing.T) {
	dir := seedFixture(t)
	quietSeed(t)
	orphan := filepath.Join(dir, seedPrefix+"0123456789abcdef"+seedSuffix)
	if err := os.WriteFile(orphan, []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := identityOf(orphan)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePristine(dir, pristineMarker{Schema: 1, State: seededState, ArtifactSHA256: strings.Repeat("a", 64), Identity: id}); err != nil {
		t.Fatal(err)
	}
	before := diskSum(t, dir)
	marker, _ := os.ReadFile(filepath.Join(dir, pristineName))
	if err := seedSystemDisk(context.Background(), dir, fakeArtifact(t, 1<<20)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, pristineName))
	if diskSum(t, dir) != before || !bytes.Equal(marker, after) {
		t.Fatal("an interrupted publication changed the disk or its record")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("the unexplained seed was removed: %v", err)
	}
}
