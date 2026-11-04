package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

const testGiB = 1 << 30

// fullID is a 64-character container ID made of one repeated digit.
func fullID(d string) string { return strings.Repeat(d, 64) }

// storageFixture is today's incident in miniature: a 30 GiB system disk with
// two boxes, a stopped box and one foreign container.
func storageFixture() storageSource {
	return storageSource{
		dataMount: "/mnt/data",
		statfs: func(path string) (spacetelemetry.Usage, error) {
			if path == "/" {
				return spacetelemetry.Usage{Total: 30 * testGiB, Free: 6 * testGiB}, nil
			}
			return spacetelemetry.Usage{Total: 10 * testGiB, Free: 8 * testGiB}, nil
		},
		declared: func() ([]string, error) { return []string{"yolo-dev", "opus", "idle"}, nil },
		// nerdctl ps prints 12-character IDs; native inspect has full ones.
		containers: func(context.Context) ([]container.Container, error) {
			return []container.Container{
				{ID: fullID("1")[:12], Name: "opus", Status: "Up 3 hours"},
				{ID: fullID("2")[:12], Name: "yolo-dev", Status: "Up 2 hours"},
				{ID: fullID("3")[:12], Name: "idle", Status: "Exited (0) 1 day ago"},
				{ID: fullID("4")[:12], Name: "scratch", Status: "Created"},
			}, nil
		},
		snapshotKeys: func(_ context.Context, names []string) (map[string]string, error) {
			if strings.Join(names, ",") != "opus,yolo-dev,idle,scratch" {
				return nil, errors.New("unexpected names " + strings.Join(names, ","))
			}
			return map[string]string{fullID("1"): "k-opus", fullID("2"): "k-dev", fullID("3"): "k-idle", fullID("4"): "k-x"}, nil
		},
		snapshots: func(context.Context) (map[string]uint64, error) {
			return map[string]uint64{
				"k-dev": 9 * testGiB, "k-opus": 2 * testGiB, "k-idle": testGiB / 2, "k-x": 0,
				"sha256:base": 3 * testGiB, "sha256:composed": 2 * testGiB,
				"buildkit-rec": testGiB, "extract-1 sha256:tmp": testGiB / 2,
			}, nil
		},
		dirUsage: func(_ context.Context, path string) (uint64, error) {
			switch path {
			case containerdContentDir:
				return 2 * testGiB, nil
			case buildkitStateDir:
				return testGiB / 4, nil
			}
			return 0, errors.New("unexpected directory " + path)
		},
	}
}

func TestSpaceStoragePartitionsTheSystemDisk(t *testing.T) {
	at := time.Date(2026, 10, 27, 20, 0, 0, 0, time.UTC)
	r := collectSpaceStorage(t.Context(), storageFixture(), at)
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %+v", r.Errors)
	}
	if r.System == nil || *r.System != (storageDisk{Total: 30 * testGiB, Free: 6 * testGiB, Used: 24 * testGiB}) || r.Data == nil || r.Data.Used != 2*testGiB {
		t.Fatalf("disks: %+v %+v", r.System, r.Data)
	}
	var names []string
	for _, b := range r.Boxes {
		names = append(names, b.Name)
	}
	if strings.Join(names, ",") != "yolo-dev,opus,idle,scratch" {
		t.Fatalf("boxes sorted by size: %v", names)
	}
	if b := r.Boxes[2]; b.Running || !b.Declared || b.Bytes != testGiB/2 {
		t.Fatalf("stopped box: %+v", b)
	}
	if r.Boxes[3].Declared || !r.Boxes[0].Running {
		t.Fatalf("labels: %+v", r.Boxes)
	}
	if r.Images == nil || *r.Images != (storageImages{Bytes: 7 * testGiB, Layers: 5 * testGiB, Blobs: 2 * testGiB}) {
		t.Fatalf("images: %+v", r.Images)
	}
	if r.BuildKit == nil || *r.BuildKit != (storageBuild{Bytes: testGiB*3/2 + testGiB/4, Snapshots: testGiB * 3 / 2, State: testGiB / 4}) {
		t.Fatalf("buildkit: %+v", r.BuildKit)
	}
	// Each snapshot is counted once: the parts add up to the used space.
	sum := r.Images.Bytes + r.BuildKit.Bytes + *r.Other
	for _, b := range r.Boxes {
		sum += b.Bytes
	}
	if r.Other == nil || sum != r.System.Used {
		t.Fatalf("sum %d != used %d (other %v)", sum, r.System.Used, r.Other)
	}
	if !r.MeasuredAt.Equal(at) {
		t.Fatal(r.MeasuredAt)
	}
	var out bytes.Buffer
	if err := writeSpaceStorage(&out, "yolospace", r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"boxes, images, build caches", "accounts & box settings (kept across VM rebuilds)",
		"yolo-dev", "9.0GiB", "box, running", "box, stopped", "container (not a box of this space)", "guest OS and other"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text output lacks %q:\n%s", want, out.String())
		}
	}
}

// An unmeasured part stays unavailable with its reason; the remainder is then
// unknown instead of silently absorbing it.
func TestSpaceStorageReportsUnavailableParts(t *testing.T) {
	for name, tc := range map[string]struct {
		break_  func(*storageSource)
		items   []string
		boxes   int
		images  bool
		builder bool
	}{
		"snapshots": {func(s *storageSource) {
			s.snapshots = func(context.Context) (map[string]uint64, error) { return nil, errors.New("ctr failed") }
		}, []string{"snapshots"}, 0, false, false},
		"container keys": {func(s *storageSource) {
			s.snapshotKeys = func(context.Context, []string) (map[string]string, error) { return nil, errors.New("inspect failed") }
		}, []string{"boxes"}, 0, false, false},
		"one box": {func(s *storageSource) {
			s.snapshotKeys = func(context.Context, []string) (map[string]string, error) {
				return map[string]string{fullID("1"): "k-opus", fullID("2"): "k-dev", fullID("4"): "k-x"}, nil
			}
		}, []string{"box idle"}, 3, true, false},
		"blobs": {func(s *storageSource) {
			inner := s.dirUsage
			s.dirUsage = func(ctx context.Context, p string) (uint64, error) {
				if p == containerdContentDir {
					return 0, errors.New("walk failed")
				}
				return inner(ctx, p)
			}
		}, []string{"images"}, 4, false, true},
		"system disk": {func(s *storageSource) {
			s.statfs = func(string) (spacetelemetry.Usage, error) { return spacetelemetry.Usage{}, errors.New("statfs failed") }
		}, []string{"system", "data"}, 4, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			src := storageFixture()
			tc.break_(&src)
			r := collectSpaceStorage(t.Context(), src, time.Now())
			var items []string
			for _, e := range r.Errors {
				items = append(items, e.Item)
				if e.Reason == "" {
					t.Errorf("%s has no reason", e.Item)
				}
			}
			if strings.Join(items, ",") != strings.Join(tc.items, ",") {
				t.Fatalf("errors %v, want %v", items, tc.items)
			}
			if r.Other != nil {
				t.Fatalf("remainder %d with a part missing", *r.Other)
			}
			if len(r.Boxes) != tc.boxes || (r.Images != nil) != tc.images || (r.BuildKit != nil) != tc.builder {
				t.Fatalf("boxes %d images %v buildkit %v", len(r.Boxes), r.Images, r.BuildKit)
			}
		})
	}
}

// A declaration failure only loses the box labels, never the sums.
func TestSpaceStorageWithoutDeclarations(t *testing.T) {
	src := storageFixture()
	src.declared = func() ([]string, error) { return nil, errors.New("disk busy") }
	r := collectSpaceStorage(t.Context(), src, time.Now())
	if r.Other == nil || len(r.Errors) != 1 || r.Errors[0].Item != "declarations" {
		t.Fatalf("report: %+v", r)
	}
	var out bytes.Buffer
	if err := writeSpaceStorage(&out, "s", r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "not a box") {
		t.Fatalf("unknown declarations labelled containers:\n%s", out.String())
	}
}

// space.storage is a host read: a box operator is refused.
func TestSpaceStorageIsHostOnlyRead(t *testing.T) {
	op, ok := controllerOperations[spaceStorageMethod]
	if !ok || op.Mutates || op.BoxAllowed {
		t.Fatalf("registry entry %+v %v", op, ok)
	}
	request := controller.Request{ID: strings.Repeat("r", 20), Method: spaceStorageMethod, Space: "work", Dash: -1}
	if err := controller.Authorize(controller.Principal{}, request, "work", controllerOperations); err != nil {
		t.Fatal("host admin refused:", err)
	}
	if err := controller.Authorize(controller.Principal{Box: "lead", Incarnation: "live"}, request, "work", controllerOperations); err == nil {
		t.Fatal("an operator box may read space.storage")
	}
}

func TestDiskUsageCountsEachInodeOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("disk usage is measured only inside the Linux VM (storage_usage_other.go refuses elsewhere)")
	}
	dir := t.TempDir()
	if n, err := diskUsage(t.Context(), filepath.Join(dir, "absent")); err != nil || n != 0 {
		t.Fatalf("absent = %d %v", n, err)
	}
	blob := filepath.Join(dir, "blobs", "sha256", "a")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, bytes.Repeat([]byte("x"), 64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	one, err := diskUsage(t.Context(), dir)
	if err != nil || one < 64<<10 {
		t.Fatalf("one blob = %d %v", one, err)
	}
	if err := os.Link(blob, filepath.Join(dir, "hard")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(dir, "root")); err != nil {
		t.Fatal(err)
	}
	if again, err := diskUsage(t.Context(), dir); err != nil || again != one {
		t.Fatalf("hard link or symlink changed the total: %d -> %d (%v)", one, again, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = diskUsage(ctx, dir) // a small tree may finish first; it must not hang
	if _, err := diskUsage(t.Context(), blob); err == nil {
		t.Fatal("a file root accepted")
	}
}
