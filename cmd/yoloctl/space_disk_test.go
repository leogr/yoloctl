package main

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/ext4sb"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
)

const diskGiB = spaceconfig.GiB

// A new space's system disk: auto is half of the free space within 30–50
// GiB, the floor wins on a small host with a notice, and only a host that
// cannot format the disk above the start headroom is refused. An explicit
// size keeps its 20 GiB floor and has no cap.
func TestSpaceCreateSizesTheSystemDisk(t *testing.T) {
	for _, tc := range []struct {
		value   string
		backend string
		free    uint64 // MiB
		want    string
		refused string
		notice  string
	}{
		{"auto", "firecracker", 80 << 10, "40GiB", "", "system disk 40 GiB (half of 80 GiB free)"},
		{"auto", "firecracker", 120 << 10, "50GiB", "", "system disk 50 GiB (half of 120 GiB free)"},
		{"auto", "firecracker", 50 << 10, "30GiB", "", "system disk 30 GiB (half of 50 GiB free)"},
		{"auto", "firecracker", 200 << 10, "50GiB", "", "(half of 200 GiB free)"},
		{"auto", "firecracker", 20 << 10, "30GiB", "", "the host filesystem has 20.0 GiB free; the VM's 30 GiB disk cannot be filled, and the host pauses the VM at 2 GiB free"},
		{"", "firecracker", 6 << 10, "30GiB", "", "6.0 GiB free; the VM's 30 GiB disk cannot be filled"},
		{"auto", "firecracker", 4300, "", "cannot create space: 4.2 GiB free on /host, at least 4.6 GiB is needed to format a 30 GiB system disk", ""},
		{"auto", "lima", 4300, "30GiB", "", "the VM's 30 GiB disk cannot be filled\n"},
		{"60GiB", "firecracker", 200 << 10, "60GiB", "", ""},
		{"100GiB", "firecracker", 40 << 10, "100GiB", "", "40.0 GiB free; the VM's 100 GiB disk cannot be filled"},
		{"20GiB", "firecracker", 30 << 10, "20GiB", "", ""},
		{"10GiB", "firecracker", 200 << 10, "", "below the 20GiB minimum", ""},
		{"40G", "firecracker", 200 << 10, "", "must use KiB, MiB or GiB", ""},
	} {
		var out bytes.Buffer
		t.Cleanup(bindCLITestLogger(t, &out))
		got, err := resolveSpaceDisk(tc.value, tc.backend, tc.free<<20, "/host", &out)
		switch {
		case tc.refused != "":
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Errorf("%s %s %d MiB: want refusal %q, got %q %v", tc.value, tc.backend, tc.free, tc.refused, got, err)
			}
		case err != nil || got != tc.want || !strings.Contains(out.String(), tc.notice):
			t.Errorf("%s %s %d MiB: %q %v, notice %q", tc.value, tc.backend, tc.free, got, err, out.String())
		case tc.notice == "" && out.Len() > 0:
			t.Errorf("%s: unexpected notice %q", tc.value, out.String())
		}
	}
}

func TestFormatAllocationMatchesTheMeasuredCost(t *testing.T) {
	for _, tc := range []struct{ size, measuredMiB uint64 }{{30, 610}, {60, 1092}, {100, 1735}} {
		got := vmapi.FormatAllocation("firecracker", tc.size*diskGiB) >> 20
		if got < tc.measuredMiB || got > tc.measuredMiB*103/100 {
			t.Errorf("%d GiB: %d MiB, measured %d MiB", tc.size, got, tc.measuredMiB)
		}
	}
	if vmapi.FormatAllocation("lima", 50*diskGiB) != 0 {
		t.Error("Lima formats in the guest")
	}
}

// space set --disk only grows: it saves a larger size for the next
// recreate, without calling the adapter for the disk, and refuses a smaller
// or malformed one without changing anything.
func TestSpaceSetDiskGrowsForTheNextRecreate(t *testing.T) {
	before, applied := resourceSpaceFixture(t)
	old := measureSpaceBacking
	t.Cleanup(func() { measureSpaceBacking = old })
	measureSpaceBacking = func(string, string) (uint64, string, error) { return 40 * diskGiB, "/host", nil }
	for _, bad := range []string{"20GiB", "10GiB", "abc", "45G"} {
		if err := resourceCommand(t, "space", "set", "dev", "--disk", bad); err == nil {
			t.Fatalf("--disk %s accepted", bad)
		}
	}
	if after, _ := spaceconfig.LoadBootstrap("dev"); after.VM.Disk != "30GiB" || len(*applied) != 0 {
		t.Fatalf("a refusal changed the space: %+v %v", after.VM, *applied)
	}
	cmd := newClientRootCmd()
	var out, errw bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &errw))
	cmd.SetOut(&out)
	cmd.SetErr(&errw)
	cmd.SetArgs([]string{"space", "set", "dev", "--disk", "50GiB"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := spaceconfig.LoadBootstrap("dev")
	if err != nil {
		t.Fatal(err)
	}
	want := *before
	want.VM.Disk = "50GiB"
	if !reflect.DeepEqual(&want, after) {
		t.Fatalf("changed more than the disk: %+v", after)
	}
	if out.Len() != 0 || !strings.Contains(errw.String(), "disk 50GiB saved; it applies at the next yoloctl space recreate dev") ||
		!strings.Contains(errw.String(), "40.0 GiB free; the VM's 50 GiB disk cannot be filled") {
		t.Fatalf("output %q, notice %q", out.String(), errw.String())
	}
	if len(*applied) != 1 || (*applied)[0] != (vmapi.Resources{CPUs: 4, Memory: "4GiB"}) {
		t.Fatalf("the adapter gets CPUs and memory only: %v", *applied)
	}
	if err := resourceCommand(t, "space", "set", "dev", "--cpus", "6"); err != nil {
		t.Fatal(err)
	}
	if after, _ := spaceconfig.LoadBootstrap("dev"); after.VM.Disk != "50GiB" || after.VM.CPUs != 6 {
		t.Fatalf("a CPU change keeps the disk: %+v", after.VM)
	}
}

func TestSpaceShowAndListNameASavedDiskNotYetApplied(t *testing.T) {
	if got := diskShown(30*diskGiB, "50GiB"); got != "30.0GiB (disk 50GiB saved, applies at the next space recreate)" {
		t.Fatalf("show: %q", got)
	}
	if got := diskShown(50*diskGiB, "50GiB"); got != "50.0GiB" {
		t.Fatalf("applied: %q", got)
	}
	if got := diskShown(0, "40GiB"); got != "40GiB (saved)" {
		t.Fatalf("no VM: %q", got)
	}
	var out bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &out))
	instances := []vmapi.Instance{{Name: "dev", Status: "Stopped", Disk: 30 * diskGiB}, {Name: "ok", Status: "Running", Disk: 40 * diskGiB}}
	if err := printInstances(&out, instances, map[string]string{"dev": "50GiB", "ok": "40GiB"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "applies at the next space recreate") != 1 || !strings.Contains(out.String(), "disk 50GiB saved") {
		t.Fatalf("list:\n%s", out.String())
	}
	var js bytes.Buffer
	if err := writeSpaceShowJSON(&js, &spaceShowData{Instance: vmapi.Instance{Name: "dev", Disk: 30 * diskGiB}, SavedDisk: "50GiB"}); err != nil || !strings.Contains(js.String(), `"saved_disk_bytes": 53687091200`) {
		t.Fatalf("show JSON: %v %s", err, js.String())
	}
}

// fakeGrower records a data disk grow and checks that each step follows
// the durable checkpoint before it.
type fakeGrower struct {
	t         *testing.T
	supported bool
	sb        ext4sb.Superblock
	size      uint64
	noReflink bool
	logged    bool
	extended  []bool // backup per call
	removed   int
}

func (f *fakeGrower) DataDiskGrowSupported(string, string) (bool, error) { return f.supported, nil }
func (f *fakeGrower) DataDiskState(string, string) (ext4sb.Superblock, uint64, error) {
	return f.sb, f.size, nil
}
func (f *fakeGrower) ExtendDataDisk(space, _ string, from, to uint64, backup bool) error {
	saved, err := spaceconfig.LoadBootstrap(space)
	if err != nil || saved.DataDiskGrow == nil || saved.DataDiskGrow.Phase != spaceconfig.GrowRequested || saved.DataDiskGrow.To != to {
		f.t.Fatalf("extended before the requested checkpoint was durable: %+v %v", saved, err)
	}
	if backup && f.noReflink {
		return fmt.Errorf("%w: operation not supported", vmapi.ErrNoReflink)
	}
	f.extended = append(f.extended, backup)
	f.size = to
	return nil
}
func (f *fakeGrower) DataDiskGrowLogged(string, string) (bool, error) { return f.logged, nil }
func (f *fakeGrower) RemoveDataDiskBackup(string) error               { f.removed++; return nil }

func growFixture(t *testing.T) (*spaceconfig.Bootstrap, *fakeGrower) {
	t.Helper()
	boot, _ := resourceSpaceFixture(t)
	boot.VM.Backend, boot.VM.Image = "firecracker", "/images/fixture"
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	f := &fakeGrower{t: t, supported: true, sb: ext4sb.Superblock{UUID: boot.DiskUUID, Size: 10 * diskGiB}, size: 10 * diskGiB}
	old := dataDiskGrower
	t.Cleanup(func() { dataDiskGrower = old })
	dataDiskGrower = func(kind string) (vmapi.DataDiskGrower, bool) { return f, kind == "firecracker" }
	return boot, f
}

func runSpaceSet(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newClientRootCmd()
	var out bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &out))
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"space", "set", "dev"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

// A data disk grow checks the disk, records a requested checkpoint, copies
// and extends the disk, and records it extended for init; the next stop
// records the end only when init's verified grow is in the log.
func TestSpaceSetDataDiskGrowsOfflineWithACheckpoint(t *testing.T) {
	_, f := growFixture(t)
	out, err := runSpaceSet(t, "--data-disk", "20GiB")
	if err != nil {
		t.Fatal(err)
	}
	boot, _ := spaceconfig.LoadBootstrap("dev")
	if g := boot.DataDiskGrow; g == nil || g.Phase != spaceconfig.GrowExtended || g.From != 10*diskGiB || g.To != 20*diskGiB || !g.Backup || boot.VM.DataDisk != "20GiB" {
		t.Fatalf("checkpoint: %+v %q", g, boot.VM.DataDisk)
	}
	if len(f.extended) != 1 || !f.extended[0] || !strings.Contains(out, "data disk 10 GiB → 20 GiB extended; its filesystem is grown and checked before mounting at the next yoloctl space start dev") {
		t.Fatalf("%v %q", f.extended, out)
	}
	if _, err := runSpaceSet(t, "--data-disk", "30GiB"); err == nil || !strings.Contains(err.Error(), "is pending; start the space to finish it first") {
		t.Fatalf("a second grow while one is pending: %v", err)
	}
	if _, err := runSpaceSet(t, "--cpus", "6"); err != nil {
		t.Fatalf("an unrelated change while a grow is pending: %v", err)
	}
	// The start grew it, but the log does not show init's check: not recorded.
	f.sb.Size = 20 * diskGiB
	if out, err := runSpaceSet(t, "--cpus", "6"); err != nil || strings.Contains(out, "verified") {
		t.Fatalf("recorded without init's line: %q %v", out, err)
	}
	f.logged = true
	out, err = runSpaceSet(t, "--cpus", "6")
	if err != nil || !strings.Contains(out, "space dev: data disk 10 GiB → 20 GiB, verified") || f.removed != 1 {
		t.Fatalf("%q %v removed=%d", out, err, f.removed)
	}
	if boot, _ := spaceconfig.LoadBootstrap("dev"); boot.DataDiskGrow != nil || boot.VM.DataDisk != "20GiB" {
		t.Fatalf("after: %+v", boot)
	}
}

// Every refusal leaves the bootstrap and the disk untouched.
func TestSpaceSetDataDiskRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		setup func(*spaceconfig.Bootstrap, *fakeGrower)
		want  string
	}{
		{"smaller", []string{"--data-disk", "8GiB"}, nil, "only grows"},
		{"below the floor", []string{"--data-disk", "2GiB"}, nil, "below the 4GiB minimum"},
		{"lima", []string{"--data-disk", "20GiB"}, func(b *spaceconfig.Bootstrap, _ *fakeGrower) { b.VM.Backend, b.VM.Image = "lima", "" }, "not supported on Lima"},
		{"old image", []string{"--data-disk", "20GiB"}, func(_ *spaceconfig.Bootstrap, f *fakeGrower) { f.supported = false }, "predates the data disk grow; run yoloctl space recreate dev --update-vm-image first"},
		{"another filesystem", []string{"--data-disk", "20GiB"}, func(_ *spaceconfig.Bootstrap, f *fakeGrower) { f.sb.UUID = "other" }, "not as recorded"},
		{"filesystem of another size", []string{"--data-disk", "20GiB"}, func(_ *spaceconfig.Bootstrap, f *fakeGrower) { f.sb.Size = 12 * diskGiB }, "not as recorded"},
		{"no cheap copy", []string{"--data-disk", "20GiB"}, func(_ *spaceconfig.Bootstrap, f *fakeGrower) { f.noReflink = true }, "space export dev ARCHIVE first, then repeat with --no-backup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boot, f := growFixture(t)
			if tc.setup != nil {
				tc.setup(boot, f)
				if err := boot.Save(); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := readSpaceResourceSnapshot("dev")
			if _, err := runSpaceSet(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			after, _ := readSpaceResourceSnapshot("dev")
			if !bytes.Equal(before.data, after.data) || len(f.extended) != 0 {
				t.Fatalf("a refusal changed something: %v\n%s", f.extended, after.data)
			}
		})
	}
	// Without a cheap copy, --no-backup grows without one.
	_, f := growFixture(t)
	f.noReflink = true
	if _, err := runSpaceSet(t, "--data-disk", "20GiB", "--no-backup"); err != nil || len(f.extended) != 1 || f.extended[0] {
		t.Fatalf("--no-backup: %v %v", err, f.extended)
	}
	if boot, _ := spaceconfig.LoadBootstrap("dev"); boot.DataDiskGrow == nil || boot.DataDiskGrow.Backup {
		t.Fatalf("checkpoint: %+v", boot.DataDiskGrow)
	}
}

func TestDataDiskShownAndExportRefusedWhilePending(t *testing.T) {
	boot, _ := growFixture(t)
	if got := dataDiskShown(boot, false); got != "10 GiB (a data disk grow needs a VM image update)" {
		t.Fatalf("%q", got)
	}
	if got := dataDiskShown(boot, true); got != "10 GiB" {
		t.Fatalf("%q", got)
	}
	if _, err := runSpaceSet(t, "--data-disk", "20GiB"); err != nil {
		t.Fatal(err)
	}
	boot, _ = spaceconfig.LoadBootstrap("dev")
	if got := dataDiskShown(boot, true); got != "20 GiB (grow from 10 GiB pending: done at the next start, recorded at the next stop)" {
		t.Fatalf("%q", got)
	}
	cmd := newClientRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"space", "export", "dev", filepath.Join(t.TempDir(), "a.tar.gz")})
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "data disk grow 10 GiB → 20 GiB is pending") {
		t.Fatalf("export: %v", err)
	}
}
