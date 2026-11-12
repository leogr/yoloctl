package spacetelemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestParseProcStat(t *testing.T) {
	raw := "cpu  100 5 50 800 20 3 2 1 9 0\ncpu0 1 2 3 4 5 6 7 8 0 0\nintr 1\n"
	got, err := ParseProcStat(raw)
	if err != nil {
		t.Fatal(err)
	}
	// user..steal = 100+5+50+800+20+3+2+1; guest columns are already in user.
	if got != (CPUTimes{Total: 981, Idle: 820}) {
		t.Fatalf("got %+v", got)
	}
	if _, err := ParseProcStat("intr 1\n"); err == nil {
		t.Fatal("missing cpu line accepted")
	}
	if _, err := ParseProcStat("cpu 1 x 3 4\n"); err == nil {
		t.Fatal("non-numeric field accepted")
	}
}

func TestParseMeminfoAndLoad(t *testing.T) {
	total, avail, err := ParseMeminfo("MemTotal:       16384 kB\nMemFree: 1 kB\nMemAvailable:    4096 kB\n")
	if err != nil || total != 16384*1024 || avail != 4096*1024 {
		t.Fatalf("meminfo = %d %d %v", total, avail, err)
	}
	if _, _, err := ParseMeminfo("MemTotal: 1 kB\n"); err == nil {
		t.Fatal("incomplete meminfo accepted")
	}
	load, err := ParseLoadavg("1.92 1.40 0.88 2/300 4000\n")
	if err != nil || load != [3]float64{1.92, 1.40, 0.88} {
		t.Fatalf("load = %v %v", load, err)
	}
	if up, err := ParseUptime("11520.50 9000.00\n"); err != nil || up != 11520.5 {
		t.Fatalf("uptime = %v %v", up, err)
	}
}

// A key the kernel does not report stays absent; zero is a real value.
func TestParseMemStat(t *testing.T) {
	got, err := ParseMemStat("anon 100\nfile 0\nkernel 20\nshmem 7\nactive_file 3\ninactive_file 90\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Anon == nil || *got.Anon != 100 || got.File == nil || *got.File != 0 || got.Shmem == nil || *got.Shmem != 7 ||
		got.InactiveFile == nil || *got.InactiveFile != 90 {
		t.Fatalf("stat = %+v", got)
	}
	got, err = ParseMemStat("anon 100\n")
	if err != nil || got.Anon == nil || got.File != nil || got.Shmem != nil || got.InactiveFile != nil {
		t.Fatalf("absent keys = %+v %v", got, err)
	}
	for _, bad := range []string{"anon x\n", "anon 1 2\n"} {
		if _, err := ParseMemStat(bad); err == nil {
			t.Errorf("malformed memory.stat %q accepted", bad)
		}
	}
}

func TestParseCgroupFiles(t *testing.T) {
	p, err := ParseCgroupPath("0::/yoloctl-workloads/abc123\n")
	if err != nil || p != "/yoloctl-workloads/abc123" {
		t.Fatalf("path = %q %v", p, err)
	}
	for _, bad := range []string{"0::relative\n", "0::/a/../b\n", "1:cpu:/x\n"} {
		if _, err := ParseCgroupPath(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if v, err := ParseLimit("max\n"); err != nil || v != 0 {
		t.Fatalf("max = %d %v", v, err)
	}
	if v, err := ParseCPUMax("200000 100000\n"); err != nil || v != 2 {
		t.Fatalf("cpu.max = %v %v", v, err)
	}
	if v, err := ParseCPUMax("max 100000\n"); err != nil || v != 0 {
		t.Fatalf("unlimited cpu.max = %v %v", v, err)
	}
	if _, err := ParseCPUMax("100 0\n"); err == nil {
		t.Fatal("zero period accepted")
	}
	r, w, err := ParseIOStat("254:0 rbytes=100 wbytes=20 rios=1 wios=2\n254:16 rbytes=5 wbytes=7 dbytes=0\n")
	if err != nil || r != 105 || w != 27 {
		t.Fatalf("io = %d %d %v", r, w, err)
	}
	keyed, err := ParseKeyed("usage_usec 42\nuser_usec 40\n")
	if err != nil || keyed["usage_usec"] != 42 {
		t.Fatalf("keyed = %v %v", keyed, err)
	}
}

func TestParseNetDevSkipsLoopback(t *testing.T) {
	raw := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  999       9    0    0    0     0          0         0      999       9    0    0    0     0       0          0
  eth0: 1200      12    0    0    0     0          0         0      300       3    0    0    0     0       0          0
  veth1:   10       1    0    0    0     0          0         0       20       1    0    0    0     0       0          0
`
	rx, tx, err := ParseNetDev(raw)
	if err != nil || rx != 1210 || tx != 320 {
		t.Fatalf("net = %d %d %v", rx, tx, err)
	}
}

func TestFrameRoundTripAndValidation(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	at := time.Unix(1700000000, 0).UTC()
	priv := Box{Name: "yolo-dev", Privileged: true, RuntimeMode: "privileged", RuntimePrivileged: true}
	odd := Box{Name: "odd", RuntimeModeReason: "labels disagree", RuntimePrivileged: true}
	if err := enc.Encode(Frame{Kind: KindInventory, At: at, Inventory: &Inventory{Space: "dev", Boxes: []Box{priv, odd, {Name: "plain"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(Frame{Kind: KindSample, At: at, Sample: &Sample{Boxes: map[string]BoxSample{"a": {CPUUsec: 7}}}}); err != nil {
		t.Fatal(err)
	}
	buf.WriteString(`{"v":1,"kind":"sample"}` + "\n")
	dec := NewDecoder(&buf)
	f, err := dec.Decode()
	if err != nil || f.Inventory.Space != "dev" || !f.At.Equal(at) {
		t.Fatalf("inventory frame = %+v %v", f, err)
	}
	if got := f.Inventory.Boxes; len(got) != 3 || !reflect.DeepEqual(got[0], priv) || !reflect.DeepEqual(got[1], odd) || !reflect.DeepEqual(got[2], Box{Name: "plain"}) {
		t.Fatalf("privilege fields did not round trip: %+v", got)
	}
	f, err = dec.Decode()
	if err != nil || f.Sample.Boxes["a"].CPUUsec != 7 {
		t.Fatalf("sample frame = %+v %v", f, err)
	}
	if _, err := dec.Decode(); err == nil {
		t.Fatal("sample frame without payload accepted")
	}
	stale := NewDecoder(strings.NewReader(`{"v":0,"kind":"sample","sample":{}}` + "\n"))
	if _, err := stale.Decode(); err == nil {
		t.Fatal("wrong version accepted")
	}
	if _, err := NewDecoder(strings.NewReader("")).Decode(); err != io.EOF {
		t.Fatalf("empty stream = %v", err)
	}
}

func TestDecoderRefusesOversizedFrame(t *testing.T) {
	line := `{"v":1,"kind":"inventory","inventory":{"space":"` + strings.Repeat("x", MaxFrame) + `"}}` + "\n"
	if _, err := NewDecoder(strings.NewReader(line)).Decode(); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func ptr[T any](v T) *T { return &v }

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testCollector(t *testing.T) Collector {
	root := t.TempDir()
	proc, cg := filepath.Join(root, "proc"), filepath.Join(root, "cgroup")
	id := strings.Repeat("ab", 32)
	writeTree(t, proc, map[string]string{
		"stat":       "cpu  100 0 50 800 50 0 0 0 0 0\n",
		"loadavg":    "0.50 0.25 0.10 1/100 42\n",
		"meminfo":    "MemTotal: 1000 kB\nMemAvailable: 400 kB\n",
		"uptime":     "60.00 50.00\n",
		"41/cgroup":  "0::/yoloctl-workloads/" + id + "\n",
		"41/net/dev": "h\nh\n  eth0: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0\n",
		"42/cgroup":  "0::/system.slice/other.service\n",
		"43/cgroup":  "0::/yoloctl-workloads/" + strings.Repeat("cd", 32) + "\n",
		"44/cgroup":  "0::/yoloctl-workloads/" + id + "/supervised/agent\n",
		"44/net/dev": "h\nh\n",
		"45/cgroup":  "0::/yoloctl-workloads\n",
	})
	writeTree(t, cg, map[string]string{
		"yoloctl-workloads/cpu.stat":                  "usage_usec 9000\nuser_usec 1\n",
		"yoloctl-workloads/memory.current":            "700\n",
		"yoloctl-workloads/memory.max":                "900\n",
		"yoloctl-workloads/memory.peak":               "800\n",
		"yoloctl-workloads/memory.events":             "low 0\nhigh 0\nmax 1\noom 1\noom_kill 2\n",
		"yoloctl-workloads/" + id + "/cpu.stat":       "usage_usec 5000\n",
		"yoloctl-workloads/" + id + "/cpu.max":        "200000 100000\n",
		"yoloctl-workloads/" + id + "/memory.current": "300\n",
		"yoloctl-workloads/" + id + "/memory.max":     "max\n",
		"yoloctl-workloads/" + id + "/memory.stat":    "anon 120\nfile 160\nkernel 20\nshmem 10\nactive_file 30\ninactive_file 120\n",
		"yoloctl-workloads/" + id + "/pids.current":   "12\n",
		"yoloctl-workloads/" + id + "/io.stat":        "254:0 rbytes=4 wbytes=8\n",
	})
	return Collector{
		ProcRoot: proc, CgroupRoot: cg, PoolRel: "yoloctl-workloads", DiskMount: "/disk",
		SystemMount: "/",
		Statfs: func(path string) (Usage, error) {
			if path == "/" {
				return Usage{Total: 30 << 30, Free: 5 << 30}, nil
			}
			return Usage{Total: 64, Free: 46}, nil
		},
	}
}

func TestCollectorSample(t *testing.T) {
	c := testCollector(t)
	id := strings.Repeat("ab", 32)
	s := c.Sample(t.Context(), []Target{
		{Name: "api", PID: 41, ID: id},
		{Name: "outside", PID: 42},
		{Name: "reused", PID: 43, ID: id},
		{Name: "nested", PID: 44, ID: id},
		{Name: "gone", PID: 1234},
	})
	if s.Error != "" || s.CPU != (CPUTimes{Total: 1000, Idle: 850}) || s.MemTotal != 1000*1024 || s.Uptime != 60 {
		t.Fatalf("vm sample = %+v", s)
	}
	if s.Pool == nil || *s.Pool != (Pool{CPUUsec: 9000, Current: 700, Max: 900, Peak: 800, OOMKills: 2}) {
		t.Fatalf("pool = %+v (%s)", s.Pool, s.PoolError)
	}
	if s.Disk == nil || *s.Disk != (Usage{Total: 64, Free: 46}) {
		t.Fatalf("disk = %+v", s.Disk)
	}
	if s.SystemDisk == nil || *s.SystemDisk != (Usage{Total: 30 << 30, Free: 5 << 30}) {
		t.Fatalf("system disk = %+v", s.SystemDisk)
	}
	api := s.Boxes["api"]
	want := BoxSample{CPUUsec: 5000, CPULimit: 2, MemCurrent: 300, Pids: 12, IORead: 4, IOWrite: 8, NetRx: 10, NetTx: 20, Container: id,
		MemStat: &MemStat{Anon: ptr(uint64(120)), File: ptr(uint64(160)), Shmem: ptr(uint64(10)), InactiveFile: ptr(uint64(120))}}
	if !reflect.DeepEqual(api, want) {
		t.Fatalf("api = %+v", api)
	}
	if nested := s.Boxes["nested"]; nested.Error != "" || nested.CPUUsec != 5000 {
		t.Fatalf("nested init cgroup = %+v", nested)
	}
	for _, name := range []string{"outside", "reused", "gone"} {
		if s.Boxes[name].Error == "" {
			t.Errorf("%s sampled without confinement error: %+v", name, s.Boxes[name])
		}
	}
	if !strings.Contains(s.Boxes["outside"].Error, "outside the workload pool") {
		t.Errorf("outside error = %q", s.Boxes["outside"].Error)
	}
	if !strings.Contains(s.Boxes["reused"].Error, "does not belong") {
		t.Errorf("reused error = %q", s.Boxes["reused"].Error)
	}
}

// Holder counters are read only for running sessions, and a failed read is
// reported as such instead of as zero counters, even when cgroups failed.
func TestCollectorReadsPTYCountersOnlyForSessions(t *testing.T) {
	c := testCollector(t)
	id := strings.Repeat("ab", 32)
	var (
		mu    sync.Mutex
		asked []string
	)
	c.PTYStats = func(_ context.Context, box string) (PTYCounters, error) {
		mu.Lock()
		asked = append(asked, box)
		mu.Unlock()
		if box == "broken" {
			return PTYCounters{}, errors.New("session stats unavailable: EOF")
		}
		return PTYCounters{Session: "s1", OutputBytes: 9, InputPackets: 1, InputBytes: 2}, nil
	}
	s := c.Sample(t.Context(), []Target{
		{Name: "api", PID: 41, ID: id, Session: true},
		{Name: "idle", PID: 41, ID: id},
		{Name: "broken", PID: 41, ID: id, Session: true},
		{Name: "gone", PID: 1234, Session: true},
		{Name: "unsure", PID: 41, ID: id, SessionUnknown: true},
	})
	if got := s.Boxes["api"].PTY; got == nil || *got != (PTYCounters{Session: "s1", OutputBytes: 9, InputPackets: 1, InputBytes: 2}) || s.Boxes["api"].PTYError != "" {
		t.Fatalf("api pty = %+v %q", got, s.Boxes["api"].PTYError)
	}
	if idle := s.Boxes["idle"]; idle.PTY != nil || idle.PTYError != "" {
		t.Fatalf("box without a session read PTY counters: %+v", idle)
	}
	if broken := s.Boxes["broken"]; broken.PTY != nil || !strings.Contains(broken.PTYError, "unavailable") {
		t.Fatalf("failed read = %+v", broken)
	}
	if gone := s.Boxes["gone"]; gone.Error == "" || gone.PTY == nil {
		t.Fatalf("PTY evidence must not depend on cgroup metrics: %+v", gone)
	}
	// An unknown session state is unread output, which activity reports as
	// unknown, never as a box without a session.
	if unsure := s.Boxes["unsure"]; unsure.PTY != nil || unsure.PTYError != "session state unknown" {
		t.Fatalf("unknown session = %+v", unsure)
	}
	slices.Sort(asked)
	if strings.Join(asked, ",") != "api,broken,gone" {
		t.Fatalf("stats asked for %v", asked)
	}
	c.PTYStats = nil
	if api := c.Sample(t.Context(), []Target{{Name: "api", PID: 41, ID: id, Session: true}}).Boxes["api"]; api.PTY != nil || api.PTYError == "" {
		t.Fatalf("missing reader reported as counters: %+v", api)
	}
}

// Unread disk or network counters are named, not reported as zero bytes.
func TestCollectorNamesUnreadByteCounters(t *testing.T) {
	c := testCollector(t)
	id := strings.Repeat("ab", 32)
	target := []Target{{Name: "api", PID: 41, ID: id}}
	if api := c.Sample(t.Context(), target).Boxes["api"]; len(api.Unavailable) != 0 {
		t.Fatalf("readable counters reported unavailable: %v", api.Unavailable)
	}
	if err := os.Remove(filepath.Join(c.CgroupRoot, "yoloctl-workloads", id, "io.stat")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.ProcRoot, "41", "net", "dev"), []byte("h\nh\n  eth0: 10 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := c.Sample(t.Context(), target).Boxes["api"]
	if !reflect.DeepEqual(api.Unavailable, []string{SignalIO, SignalNet}) || api.IORead != 0 || api.NetRx != 0 || api.Error != "" {
		t.Fatalf("unread counters = %+v", api)
	}
}

// An unread memory limit or breakdown is named and leaves the charge intact:
// it never reads as "no limit" or as a zero breakdown.
func TestCollectorNamesUnreadMemoryCounters(t *testing.T) {
	c := testCollector(t)
	id := strings.Repeat("ab", 32)
	target := []Target{{Name: "api", PID: 41, ID: id}}
	dir := filepath.Join(c.CgroupRoot, "yoloctl-workloads", id)
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("2147483648\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if api := c.Sample(t.Context(), target).Boxes["api"]; api.MemMax != 2<<30 || len(api.Unavailable) != 0 {
		t.Fatalf("limit = %+v", api)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("lots\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte("anon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := c.Sample(t.Context(), target).Boxes["api"]
	if !reflect.DeepEqual(api.Unavailable, []string{SignalMemMax, SignalMemStat}) || api.MemMax != 0 || api.MemStat != nil ||
		api.MemCurrent != 300 || api.Error != "" {
		t.Fatalf("malformed memory counters = %+v", api)
	}
	for _, name := range []string{"memory.max", "memory.stat"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	api = c.Sample(t.Context(), target).Boxes["api"]
	if !reflect.DeepEqual(api.Unavailable, []string{SignalMemMax, SignalMemStat}) || api.MemCurrent != 300 || api.Error != "" {
		t.Fatalf("missing memory counters = %+v", api)
	}
}

// Holder reads share one deadline. Observe that deadline and cancellation at
// the holder boundary, rather than timing unrelated Sample filesystem work.
func TestCollectorBoundsStalledHolderReads(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared_deadline", true: "parent_cancel"}[cancelled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := Collector{}
				var targets []Target
				for i := range 3 * ptyReaders {
					targets = append(targets, Target{Name: fmt.Sprintf("b%d", i), Session: true})
				}
				observed := make(chan context.Context, len(targets))
				c.PTYStats = func(ctx context.Context, _ string) (PTYCounters, error) {
					observed <- ctx
					<-ctx.Done()
					return PTYCounters{}, ctx.Err()
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan map[string]ptyResult, 1)
				before := time.Now()
				go func() { result <- c.ptys(ctx, targets) }()
				var first context.Context
				select {
				case first = <-observed:
				case <-time.After(5 * time.Second):
					t.Fatal("holder read never entered")
				}
				seen := time.Now()
				deadline, ok := first.Deadline()
				if !ok || deadline.Before(before.Add(PTYSampleTimeout)) || deadline.After(seen.Add(PTYSampleTimeout)) {
					t.Fatalf("holder deadline %v outside timeout construction interval [%v,%v]", deadline, before, seen)
				}
				want := context.DeadlineExceeded
				if cancelled {
					want = context.Canceled
					cancel()
				}
				var got map[string]ptyResult
				select {
				case got = <-result:
				case <-time.After(5 * time.Second):
					t.Fatal("holder reads ignored deadline/cancellation")
				}
				if !errors.Is(first.Err(), want) {
					t.Fatal("holder ended for wrong reason", first.Err(), want)
				}
				close(observed) // ptys joins every callback before returning.
				for next := range observed {
					d, ok := next.Deadline()
					if !ok || d != deadline || next.Done() != first.Done() {
						t.Fatal("holder reads did not share one context/deadline")
					}
				}
				if len(got) != len(targets) {
					t.Fatal("missing stalled holder results", len(got))
				}
				for _, target := range targets {
					if r := got[target.Name]; r.counters != nil || r.err == "" {
						t.Fatalf("stalled holder %s = %+v", target.Name, r)
					}
				}
			})
		})
	}
}

// Sample also reads disk/cgroup files. Delay an unrelated statfs read, cancel
// during it, then prove every holder sees cancellation rather than a fresh wait.
func TestCollectorCancelledSampleAfterUnrelatedIO(t *testing.T) {
	c := testCollector(t)
	id := strings.Repeat("ab", 32)
	targets := []Target{{Name: "api", PID: 41, ID: id, Session: true}}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	var once sync.Once
	original := c.Statfs
	c.Statfs = func(path string) (Usage, error) {
		once.Do(func() { close(entered); <-release })
		return original(path)
	}
	late := make(chan error, len(targets))
	c.PTYStats = func(ctx context.Context, _ string) (PTYCounters, error) {
		late <- ctx.Err()
		return PTYCounters{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan *Sample, 1)
	go func() { result <- c.Sample(ctx, targets) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated IO never entered")
	}
	cancel()
	unblock()
	var sample *Sample
	select {
	case sample = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled sample did not finish")
	}
	close(late)
	for err := range late {
		if !errors.Is(err, context.Canceled) {
			t.Fatal("holder did not inherit cancellation", err)
		}
	}
	if got := sample.Boxes["api"]; got.PTY != nil || got.PTYError == "" {
		t.Fatal("canceled holder reported counters", got)
	}
}

func TestCollectorRejectsPoolItselfAndSymlinks(t *testing.T) {
	c := testCollector(t)
	if _, err := c.BoxCgroup(Target{Name: "pool", PID: 45}); err == nil {
		t.Fatal("pool root accepted as a box cgroup")
	}
	link := filepath.Join(c.ProcRoot, "51")
	if err := os.Symlink(filepath.Join(c.ProcRoot, "41"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BoxCgroup(Target{Name: "link", PID: 51}); err == nil {
		t.Fatal("followed a symlinked process directory")
	}
}

// Free space decides first, then the used fraction; both thresholds are
// inclusive at the boundary the plan names.
func TestDiskPressure(t *testing.T) {
	const gib = 1 << 30
	for _, tc := range []struct {
		name string
		u    Usage
		want DiskLevel
	}{
		{"plenty", Usage{Total: 30 * gib, Free: 10 * gib}, DiskOK},
		// The incident's state after cleanup (81%, 5.7 GiB free) is below both
		// confirmed thresholds.
		{"incident after cleanup", Usage{Total: 30 * gib, Free: 57 * gib / 10}, DiskOK},
		{"just under 4 GiB", Usage{Total: 30 * gib, Free: 4*gib - 1}, DiskWarn},
		{"85% used with room", Usage{Total: 100 * gib, Free: 15 * gib}, DiskWarn},
		{"critical by free space", Usage{Total: 30 * gib, Free: 3 * gib / 2}, DiskCritical},
		{"95% used of a big disk", Usage{Total: 200 * gib, Free: 10 * gib}, DiskCritical},
		{"small data disk", Usage{Total: 97 * gib / 10, Free: gib / 2}, DiskCritical},
		// A small disk's floors are 15% and 10% of its size, not 4 and 2 GiB.
		{"data disk 69% used", Usage{Total: 97 * gib / 10, Free: 3 * gib}, DiskOK},
		{"data disk 86% used", Usage{Total: 97 * gib / 10, Free: 14 * gib / 10}, DiskWarn},
		{"data disk under 10% free", Usage{Total: 97 * gib / 10, Free: 9 * gib / 10}, DiskCritical},
		{"20 GiB disk under 2 GiB free", Usage{Total: 20 * gib, Free: 2*gib - 1}, DiskCritical},
		{"unknown size", Usage{}, DiskOK},
	} {
		if got := tc.u.Pressure(); got != tc.want {
			t.Errorf("%s: %+v = %d, want %d", tc.name, tc.u, got, tc.want)
		}
	}
	if u := (Usage{Total: 30 * gib}); u.WarnFree() != 4*gib || u.CriticalFree() != 2*gib {
		t.Fatalf("30 GiB floors = %d/%d", u.WarnFree(), u.CriticalFree())
	}
	if (Usage{Total: 10, Free: 20}).Used() != 0 {
		t.Fatal("free above total must not underflow")
	}
}

// A system disk that cannot be read is absent, never an empty disk, and a
// sample without the field still decodes.
func TestSystemDiskUnavailable(t *testing.T) {
	c := testCollector(t)
	c.Statfs = func(path string) (Usage, error) {
		if path == "/" {
			return Usage{}, errors.New("statfs failed")
		}
		return Usage{Total: 64, Free: 46}, nil
	}
	if s := c.Sample(t.Context(), nil); s.SystemDisk != nil || s.Disk == nil {
		t.Fatalf("sample = %+v / %+v", s.SystemDisk, s.Disk)
	}
	raw := `{"v":1,"kind":"sample","at":"2026-10-27T10:00:00Z","sample":{"cpu":{"total":1,"idle":1},"load":[0,0,0],"mem_total":1,"mem_available":1,"uptime":1,"disk":{"total":64,"free":46},"boxes":{}}}` + "\n"
	f, err := NewDecoder(strings.NewReader(raw)).Decode()
	if err != nil || f.Sample.SystemDisk != nil || f.Sample.Disk == nil {
		t.Fatalf("frame = %+v, %v", f.Sample, err)
	}
}
