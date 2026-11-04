package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

func TestParseTelemetryInterval(t *testing.T) {
	if d, err := parseTelemetryInterval(nil); err != nil || d != time.Second {
		t.Fatalf("default = %v %v", d, err)
	}
	if d, err := parseTelemetryInterval([]string{"2s", "500ms"}); err != nil || d != 500*time.Millisecond {
		t.Fatalf("last value = %v %v", d, err)
	}
	for _, bad := range []string{"10ms", "11s", "soon"} {
		if _, err := parseTelemetryInterval([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// frameRecorder cancels the stream after a fixed number of frames, as a
// disconnecting host would.
type frameRecorder struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	frames int
	limit  int
	cancel context.CancelFunc
}

func (r *frameRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames += bytes.Count(p, []byte("\n"))
	if r.frames >= r.limit {
		r.cancel()
	}
	return r.buf.Write(p)
}

func fakeTelemetrySource(t *testing.T) telemetrySource {
	t.Helper()
	proc := t.TempDir()
	worker := &agentbox.Config{Name: "worker", Agent: "none", Account: account.None,
		Workdir:           agentbox.Workdir{Mode: agentbox.WorkdirModeEphemeral},
		UnconfinedNetwork: true, SessionCommand: []string{"python3", "-m", "http.server", "8080"}}
	api := &agentbox.Config{Name: "api", Agent: "none", Account: account.None,
		Workdir: agentbox.Workdir{Mode: agentbox.WorkdirModeHost, Source: "/src/app", Canonical: "/src/app"},
		Ports:   []agentbox.Port{{Target: 3000, Published: 3001}}, Capabilities: []string{"fs.workdir.write"}}
	ptyStats := func(_ context.Context, box string) (spacetelemetry.PTYCounters, error) {
		return spacetelemetry.PTYCounters{Session: "s-" + box, OutputBytes: 5}, nil
	}
	return telemetrySource{
		space: "dev",
		settings: func() (*spaceconfig.Config, error) {
			return &spaceconfig.Config{DefaultAccount: "personal", DefaultWorkdir: "/src", Mounts: []spaceconfig.Mount{{Source: "/src", Writable: true}}}, nil
		},
		boxes: func(context.Context) ([]boxstate.Box, error) {
			return []boxstate.Box{
				{Name: "api", State: boxstate.DisplayRunning, Runtime: boxstate.RuntimeRunning, Session: boxstate.SessionInfo{State: boxstate.SessionRunning}, Config: api},
				{Name: "worker", State: boxstate.DisplayRunning, Runtime: boxstate.RuntimeRunning, Session: boxstate.SessionInfo{State: boxstate.SessionExited, ExitCode: 3}, Config: worker},
				{Name: "old", State: boxstate.DisplayStopped, Runtime: boxstate.RuntimeStopped, Config: api},
			}, nil
		},
		runtime: func(_ context.Context, names []string) (map[string]container.RuntimeState, error) {
			if !reflect.DeepEqual(names, []string{"api", "worker"}) {
				t.Errorf("inspected %v; stopped boxes must not be sampled", names)
			}
			return map[string]container.RuntimeState{
				"api":    {Name: "api", ID: "aaaa", Running: true, PID: 42, IP: "10.4.0.12"},
				"worker": {Name: "worker", ID: "bbbb", Running: true, PID: 41, IP: "10.4.0.11", StartedAt: time.Unix(100, 0)},
			}, nil
		},
		accounts: func(context.Context) ([]spacetelemetry.Account, error) {
			return []spacetelemetry.Account{{Name: "personal", Agents: []string{"claude"}}}, nil
		},
		collector: spacetelemetry.Collector{ProcRoot: proc, CgroupRoot: t.TempDir(), PoolRel: "yoloctl-workloads", PTYStats: ptyStats},
		cpus:      8,
	}
}

// Only a managed room's verified presence creates a room; box features and
// network listeners never do.
func TestStreamTelemetryFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rec := &frameRecorder{limit: 3, cancel: cancel}
	if err := streamTelemetry(ctx, rec, fakeTelemetrySource(t), time.Millisecond); err != nil {
		t.Fatalf("stream ended with %v", err)
	}
	dec := spacetelemetry.NewDecoder(&rec.buf)
	first, err := dec.Decode()
	if err != nil || first.Kind != spacetelemetry.KindInventory {
		t.Fatalf("first frame = %+v %v", first, err)
	}
	inv := first.Inventory
	if inv.Space != "dev" || inv.CPUs != 8 || inv.DefaultAccount != "personal" || len(inv.Grants) != 1 || !inv.Grants[0].Writable {
		t.Fatalf("inventory header = %+v", inv)
	}
	if len(inv.Boxes) != 3 || len(inv.Accounts) != 1 {
		t.Fatalf("inventory = %+v", inv)
	}
	if inv.Rooms == nil || len(inv.Rooms) != 0 {
		t.Fatalf("rooms = %+v", inv.Rooms)
	}
	api, worker := inv.Boxes[0], inv.Boxes[1]
	if api.Workdir != "/src/app" || api.WorkdirMode != agentbox.WorkdirModeHost || len(api.Ports) != 1 ||
		api.Ports[0] != (spacetelemetry.Port{HostIP: "127.0.0.1", Published: 3001, Target: 3000, Protocol: "tcp"}) {
		t.Fatalf("api box = %+v", api)
	}
	if !worker.Unconfined || worker.ExitCode == nil || *worker.ExitCode != 3 || worker.StartedAt == nil || worker.Workdir != "" {
		t.Fatalf("worker box = %+v", worker)
	}
	for range 2 {
		f, err := dec.Decode()
		if err != nil || f.Kind != spacetelemetry.KindSample {
			t.Fatalf("expected sample, got %+v %v", f, err)
		}
		if _, ok := f.Sample.Boxes["api"]; !ok {
			t.Fatalf("running box not sampled: %+v", f.Sample.Boxes)
		}
		if _, ok := f.Sample.Boxes["old"]; ok {
			t.Fatal("stopped box sampled")
		}
		// Only the running session's holder is asked for PTY counters.
		if pty := f.Sample.Boxes["api"].PTY; pty == nil || pty.Session != "s-api" {
			t.Fatalf("api PTY counters = %+v", pty)
		}
		if worker := f.Sample.Boxes["worker"]; worker.PTY != nil || worker.PTYError != "" {
			t.Fatalf("exited session read PTY counters: %+v", worker)
		}
	}
}

// Real cgroup files reach the encoded frames: the inventory names each running
// box's container, and its sample carries that container's charge, limit and
// memory.stat breakdown, with an unread breakdown named instead of zeroed.
func TestStreamTelemetryCarriesBoxMemory(t *testing.T) {
	src := fakeTelemetrySource(t)
	src.collector.ProcRoot, src.collector.CgroupRoot = t.TempDir(), t.TempDir()
	for root, files := range map[string]map[string]string{
		src.collector.ProcRoot: {
			"stat":      "cpu  100 0 50 800 50 0 0 0 0 0\n",
			"meminfo":   "MemTotal: 8388608 kB\nMemAvailable: 5767168 kB\n",
			"42/cgroup": "0::/yoloctl-workloads/aaaa\n",
			"41/cgroup": "0::/yoloctl-workloads/bbbb\n",
		},
		src.collector.CgroupRoot: {
			"yoloctl-workloads/cpu.stat":            "usage_usec 9000\n",
			"yoloctl-workloads/memory.current":      "3436183552\n",
			"yoloctl-workloads/memory.max":          "7516192768\n",
			"yoloctl-workloads/memory.events":       "oom_kill 0\n",
			"yoloctl-workloads/aaaa/cpu.stat":       "usage_usec 5000\n",
			"yoloctl-workloads/aaaa/memory.current": "2040528896\n",
			"yoloctl-workloads/aaaa/memory.max":     "2147483648\n",
			"yoloctl-workloads/aaaa/memory.stat":    "anon 262144000\nfile 1772093440\nshmem 10485760\ninactive_file 1725956096\n",
			"yoloctl-workloads/bbbb/cpu.stat":       "usage_usec 100\n",
			"yoloctl-workloads/bbbb/memory.current": "209715200\n",
			"yoloctl-workloads/bbbb/memory.max":     "max\n",
		},
	} {
		for rel, content := range files {
			path := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rec := &frameRecorder{limit: 2, cancel: cancel}
	if err := streamTelemetry(ctx, rec, src, time.Millisecond); err != nil {
		t.Fatalf("stream ended with %v", err)
	}
	if !bytes.Contains(rec.buf.Bytes(), []byte(`"mem_stat":{"anon":262144000,"file":1772093440,"shmem":10485760,"inactive_file":1725956096}`)) {
		t.Fatalf("encoded frames lack the breakdown:\n%s", rec.buf.String())
	}
	// The inventory names the container whose cgroup the sample read.
	dec := spacetelemetry.NewDecoder(&rec.buf)
	var inv *spacetelemetry.Inventory
	var sample *spacetelemetry.Sample
	for sample == nil {
		f, err := dec.Decode()
		if err != nil {
			t.Fatalf("no sample: %v", err)
		}
		if f.Inventory != nil {
			inv = f.Inventory
		}
		sample = f.Sample
	}
	if inv == nil {
		t.Fatal("no inventory before the sample")
	}
	for _, b := range inv.Boxes {
		if want := map[string]string{"api": "aaaa", "worker": "bbbb"}[b.Name]; b.Container != want {
			t.Errorf("inventory %s container = %q, want %q", b.Name, b.Container, want)
		}
	}
	if sample.MemTotal != 8<<30 || sample.Pool == nil || sample.Pool.Current != 3436183552 || sample.Pool.Max != 7<<30 {
		t.Fatalf("vm/pool memory = %d %+v (%s)", sample.MemTotal, sample.Pool, sample.PoolError)
	}
	api := sample.Boxes["api"]
	if api.Error != "" || api.MemCurrent != 2040528896 || api.MemMax != 2<<30 || api.Container != "aaaa" ||
		slices.Contains(api.Unavailable, spacetelemetry.SignalMemMax) || slices.Contains(api.Unavailable, spacetelemetry.SignalMemStat) ||
		api.MemStat == nil || api.MemStat.InactiveFile == nil || *api.MemStat.InactiveFile != 1725956096 {
		t.Fatalf("api memory = %+v", api)
	}
	worker := sample.Boxes["worker"]
	if worker.Error != "" || worker.MemCurrent != 200<<20 || worker.MemMax != 0 || worker.MemStat != nil || worker.Container != "bbbb" ||
		!slices.Contains(worker.Unavailable, spacetelemetry.SignalMemStat) || slices.Contains(worker.Unavailable, spacetelemetry.SignalMemMax) {
		t.Fatalf("worker memory = %+v", worker)
	}
}

// A failed session lookup is unread output, reported as an error so
// activity stays unknown; a confirmed absent session reads nothing.
func TestTelemetryUnknownSessionIsUnreadOutput(t *testing.T) {
	src := fakeTelemetrySource(t)
	boxes := src.boxes
	src.boxes = func(ctx context.Context) ([]boxstate.Box, error) {
		bs, err := boxes(ctx)
		bs[0].Session = boxstate.SessionInfo{State: boxstate.SessionUnknown}
		bs[1].Session = boxstate.SessionInfo{State: boxstate.SessionNone}
		return bs, err
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rec := &frameRecorder{limit: 2, cancel: cancel}
	if err := streamTelemetry(ctx, rec, src, time.Millisecond); err != nil {
		t.Fatalf("stream ended with %v", err)
	}
	dec := spacetelemetry.NewDecoder(&rec.buf)
	var sample *spacetelemetry.Sample
	for sample == nil {
		f, err := dec.Decode()
		if err != nil {
			t.Fatalf("no sample: %v", err)
		}
		sample = f.Sample
	}
	if api := sample.Boxes["api"]; api.PTY != nil || api.PTYError != "session state unknown" {
		t.Fatalf("unknown session = %+v", api)
	}
	if worker := sample.Boxes["worker"]; worker.PTY != nil || worker.PTYError != "" {
		t.Fatalf("absent session = %+v", worker)
	}
}

func TestTelemetryInventoryKeepsUncertaintyExplicit(t *testing.T) {
	src := fakeTelemetrySource(t)
	src.boxes = func(context.Context) ([]boxstate.Box, error) { return nil, errors.New("runtime unavailable") }
	inv, live := src.inventory(t.Context(), nil, errors.New("disk busy"))
	if inv.BoxesError != "runtime unavailable" || inv.AccountsError != "disk busy" || live != nil {
		t.Fatalf("inventory = %+v live=%v", inv, live)
	}
	if inv.Boxes == nil || inv.Accounts == nil || inv.Rooms == nil {
		t.Fatal("unavailable lists must encode as present-but-empty with an error, not null")
	}

	// A failed runtime lookup keeps the declared privilege and leaves the
	// launch mode unknown rather than confined.
	src = fakeTelemetrySource(t)
	boxes := src.boxes
	src.boxes = func(ctx context.Context) ([]boxstate.Box, error) {
		bs, err := boxes(ctx)
		cfg := *bs[1].Config
		cfg.Privileged = true
		bs[1].Config = &cfg
		return bs, err
	}
	src.runtime = func(context.Context, []string) (map[string]container.RuntimeState, error) {
		return nil, errors.New("inspect failed")
	}
	inv, _ = src.inventory(t.Context(), nil, nil)
	if w := inv.Boxes[1]; !w.Privileged || w.RuntimeMode != "" || w.RuntimeModeReason != "" || w.RuntimePrivileged || w.Container != "" {
		t.Fatalf("privileged box after a failed runtime lookup = %+v", w)
	}
}

// telemetryLaunchDetails is what the batched inspect reports for a box
// created with (privileged) or without (confined) the endpoint launch.
func telemetryLaunchDetails(privileged bool) *container.Details {
	yes := privileged
	if !privileged {
		return &container.Details{Privileged: &yes, Labels: map[string]string{agentbox.LabelAgentbox: "api"},
			Command: sandbox.EntrypointArgs(sandbox.WorkdirDir, false, "sleep", "infinity")}
	}
	return &container.Details{Privileged: &yes,
		Labels:  map[string]string{agentbox.LabelRuntimeMode: string(agentbox.RuntimeModePrivileged), agentbox.LabelLauncher: boxendpoint.Launcher},
		Command: sandbox.PrivilegedEntrypointArgs(sandbox.WorkdirDir),
		Mounts:  []container.DetailMount{{Source: boxendpoint.BinaryDir, Destination: boxendpoint.BinaryDir, ReadOnly: true}}}
}

// The declared setting and the running container's launch evidence reach the
// frame separately, so the view can mark privilege and any disagreement.
func TestTelemetryInventoryCarriesPrivilege(t *testing.T) {
	src := fakeTelemetrySource(t)
	boxes := src.boxes
	src.boxes = func(ctx context.Context) ([]boxstate.Box, error) {
		bs, err := boxes(ctx)
		for i := range bs {
			cfg := *bs[i].Config
			cfg.Privileged = bs[i].Name != "api"
			bs[i].Config = &cfg
		}
		return bs, err
	}
	src.runtime = func(context.Context, []string) (map[string]container.RuntimeState, error) {
		return map[string]container.RuntimeState{
			"api":    {Name: "api", ID: "aaaa", Running: true, PID: 42, Details: telemetryLaunchDetails(false)},
			"worker": {Name: "worker", ID: "bbbb", Running: true, PID: 41, Details: telemetryLaunchDetails(true)},
		}, nil
	}
	inv, _ := src.inventory(t.Context(), nil, nil)
	api, worker, old := inv.Boxes[0], inv.Boxes[1], inv.Boxes[2]
	if !worker.Privileged || worker.RuntimeMode != "privileged" || !worker.RuntimePrivileged || worker.RuntimeModeReason != "" {
		t.Fatalf("privileged box = %+v", worker)
	}
	if api.Privileged || api.RuntimePrivileged || api.RuntimeMode != "confined" {
		t.Fatalf("confined box = %+v", api)
	}
	// A stopped box keeps its declaration and has no runtime evidence.
	if !old.Privileged || old.RuntimeMode != "" || old.RuntimePrivileged {
		t.Fatalf("stopped box = %+v", old)
	}

	// An undeclared box whose container shows privilege is evidence, not
	// confinement; partial evidence leaves the mode unknown with its reason.
	partial := telemetryLaunchDetails(true)
	partial.Mounts = nil
	src.boxes = boxes
	src.runtime = func(context.Context, []string) (map[string]container.RuntimeState, error) {
		return map[string]container.RuntimeState{
			"api":    {Name: "api", ID: "aaaa", Running: true, PID: 42, Details: partial},
			"worker": {Name: "worker", ID: "bbbb", Running: true, PID: 41},
		}, nil
	}
	inv, _ = src.inventory(t.Context(), nil, nil)
	api, worker = inv.Boxes[0], inv.Boxes[1]
	if api.Privileged || !api.RuntimePrivileged || api.RuntimeMode != "" || api.RuntimeModeReason == "" {
		t.Fatalf("undeclared privileged runtime = %+v", api)
	}
	if worker.RuntimePrivileged || worker.RuntimeMode != "" || worker.RuntimeModeReason == "" {
		t.Fatalf("missing details = %+v", worker)
	}
}

func TestTelemetryAccountsDistinguishesUnavailableFromEmpty(t *testing.T) {
	accounts, err := telemetryAccounts([]string{"empty", "used"}, func(name string) []accountStateLeaf {
		if name == "empty" {
			return []accountStateLeaf{}
		}
		return []accountStateLeaf{{Agent: "codex"}}
	})
	if err != nil || len(accounts) != 2 || accounts[0].Agents == nil || len(accounts[0].Agents) != 0 || !reflect.DeepEqual(accounts[1].Agents, []string{"codex"}) {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
	accounts, err = telemetryAccounts([]string{"unreadable"}, func(string) []accountStateLeaf { return nil })
	if err == nil || !strings.Contains(err.Error(), `account "unreadable"`) || accounts != nil {
		t.Fatalf("unavailable native state reported as empty: accounts=%+v err=%v", accounts, err)
	}
}
