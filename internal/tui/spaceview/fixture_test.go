package spaceview

import (
	"context"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

var fixtureStart = time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// fixtureInventory mirrors the approved mockup: three agent boxes, a chat
// server with participants (one with operator authority), an exited session
// and a stopped ephemeral shell.
func fixtureInventory() *spacetelemetry.Inventory {
	home := "/home/dev"
	started := fixtureStart.Add(-2*time.Hour - 41*time.Minute)
	inv := &spacetelemetry.Inventory{
		Space: "dev", DefaultAccount: "personal", DefaultWorkdir: home + "/src", CPUs: 8,
		Grants: []spacetelemetry.Grant{{Path: home + "/src", Writable: true}, {Path: home + "/data"}},
		Accounts: []spacetelemetry.Account{
			{Name: "ci", Agents: []string{}},
			{Name: "personal", Agents: []string{"claude", "gemini"}},
			{Name: "work", Agents: []string{"codex"}},
		},
		Boxes: []spacetelemetry.Box{
			{Name: "api-refactor", State: "running", Runtime: "running", Session: "running", Agent: "claude", Account: "personal",
				WorkdirMode: "host", Workdir: home + "/src/yoloctl", Features: []string{"go", "github", "npm", "yolo.chat.skill", "claude"},
				Capabilities: []string{"fs.workdir.write", "fs.pty.allocate", "network.anthropic", "network.github", "network.go-modules", "network.npm"},
				Ports:        []spacetelemetry.Port{{HostIP: "127.0.0.1", Published: 3000, Target: 3000, Protocol: "tcp"}, {HostIP: "127.0.0.1", Published: 5173, Target: 5173, Protocol: "tcp"}},
				CPUs:         "2", StartedAt: &started},
			{Name: "conductor", State: "running", Runtime: "running", Session: "running", Agent: "claude", Account: "personal",
				WorkdirMode: "host", Workdir: home + "/src/yoloctl", Features: []string{"github", "yolo.chat.skill", "yolo.space.operator", "claude"},
				Capabilities: []string{"fs.workdir.write", "network.anthropic", "network.github", "yolo.space.operator"}, CPUs: "2", StartedAt: &started},
			{Name: "docs-bot", State: "running", Runtime: "running", Session: "running", Agent: "codex", Account: "work",
				WorkdirMode: "host", Workdir: home + "/src/website", Features: []string{"pip", "yolo.chat.skill", "codex"},
				Capabilities: []string{"fs.workdir.write", "network.openai", "network.pypi"}, CPUs: "2", Memory: "2g", StartedAt: &started},
			{Name: "room", State: "running", Runtime: "running", Session: "running", Agent: "none", Account: "none",
				WorkdirMode: "host", Workdir: home + "/src/chat", Features: []string{"yolo.chat.skill"}, Unconfined: true,
				SessionCommand: []string{"/bin/sh", "-c", "umask 077; exec /usr/local/bin/aircd serve --config /run/yoloctl/chat/server.toml"}, CPUs: "2", StartedAt: &started},
			{Name: "scratch", State: "stopped", Runtime: "stopped", Session: "none", Agent: "none", Account: "none",
				WorkdirMode: "ephemeral", Reasons: []string{"container is stopped"}},
			{Name: "web-preview", State: "exited", Runtime: "running", Session: "exited", ExitCode: ptr(1), ExitedAt: ptr(fixtureStart.Add(-4 * time.Minute)),
				Agent: "gemini", Account: "personal", WorkdirMode: "host", Workdir: home + "/src/yoloctl", Features: []string{"npm", "gemini"},
				Capabilities: []string{"fs.workdir.write", "fs.pty.allocate", "network.gemini", "network.npm"},
				Ports:        []spacetelemetry.Port{{HostIP: "127.0.0.1", Published: 8080, Target: 8080, Protocol: "tcp"}}, CPUs: "2", StartedAt: &started},
		},
		Rooms: []spacetelemetry.Room{{Server: "room", Managed: true, Ports: []int{}, Members: []string{"api-refactor", "conductor", "docs-bot"}, Local: 1}},
	}
	// Running boxes carry the container ID their samples name.
	for i, b := range inv.Boxes {
		if b.Runtime == "running" {
			inv.Boxes[i].Container = "c-" + b.Name
		}
	}
	return inv
}

// fixtureSample produces cumulative counters after step seconds.
func fixtureSample(step int) *spacetelemetry.Sample {
	sec := uint64(step)
	box := func(cores float64, mem uint64, limit float64, memMax uint64, net, io uint64, pids uint64) spacetelemetry.BoxSample {
		return spacetelemetry.BoxSample{CPUUsec: uint64(cores*1e6) * sec, CPULimit: limit, MemCurrent: mem, MemMax: memMax,
			Pids: pids, NetRx: net * sec, NetTx: net / 4 * sec, IORead: io / 8 * sec, IOWrite: io * sec,
			PTY: &spacetelemetry.PTYCounters{Session: "s", OutputBytes: (net + io) * sec}}
	}
	s := &spacetelemetry.Sample{
		CPU:      spacetelemetry.CPUTimes{Total: 800 * sec, Idle: 460 * sec},
		Load:     [3]float64{1.92, 1.40, 0.88},
		MemTotal: 16 << 30, MemAvailable: 7 << 30, Uptime: 11520 + float64(step),
		Pool:       &spacetelemetry.Pool{CPUUsec: uint64(3.2e6) * sec, Current: 7 << 30, Max: 14 << 30, Peak: 11 << 30},
		Disk:       &spacetelemetry.Usage{Total: 64 << 30, Free: 46 << 30},
		SystemDisk: &spacetelemetry.Usage{Total: 30 << 30, Free: 10 << 30},
		Boxes: map[string]spacetelemetry.BoxSample{
			"api-refactor": box(1.92, 1400<<20, 2, 0, 12<<10, 1200<<10, 87),
			"conductor":    box(0.21, 610<<20, 2, 0, 4<<10, 12<<10, 31),
			"docs-bot":     box(0.08, 380<<20, 2, 2<<30, 0, 40<<10, 23),
			"room":         box(0.02, 90<<20, 2, 0, 21<<10, 64<<10, 9),
			"web-preview":  box(0.69, 2<<30, 2, 0, 220<<10, 3100<<10, 141),
		},
	}
	for name, b := range s.Boxes {
		b.Container = "c-" + name
		s.Boxes[name] = b
	}
	return s
}

func fixtureModel(t *testing.T, w, h int) *Model {
	t.Helper()
	return fixtureModelOf(t, w, h, fixtureInventory(), fixtureSample)
}

// privilegedFixtureModel adds yolo-dev, a running privileged development
// shell with verified privileged evidence, after the mockup's boxes.
func privilegedFixtureModel(t *testing.T, w, h int) *Model {
	t.Helper()
	inv := fixtureInventory()
	started := fixtureStart.Add(-time.Hour)
	inv.Boxes = append(inv.Boxes, spacetelemetry.Box{Name: "yolo-dev", State: "running", Runtime: "running", Session: "running",
		Agent: "none", Account: "none", WorkdirMode: "host", Workdir: "/home/dev/src/yoloctl", Features: []string{"go", "github"},
		Capabilities: []string{"fs.workdir.write", "fs.pty.allocate", "network.github", "network.go-modules"},
		Unconfined:   true, Privileged: true, RuntimeMode: "privileged", RuntimePrivileged: true,
		CPUs: "2", StartedAt: &started, Container: "c-yolo-dev"})
	return fixtureModelOf(t, w, h, inv, func(step int) *spacetelemetry.Sample {
		s := fixtureSample(step)
		s.Boxes["yolo-dev"] = spacetelemetry.BoxSample{Container: "c-yolo-dev", CPUUsec: uint64(5e4) * uint64(step), CPULimit: 2,
			MemCurrent: 300 << 20, Pids: 12, PTY: &spacetelemetry.PTYCounters{Session: "s", OutputBytes: 1 << 10 * uint64(step)}}
		return s
	})
}

func fixtureModelOf(t *testing.T, w, h int, inv *spacetelemetry.Inventory, sample func(int) *spacetelemetry.Sample) *Model {
	t.Helper()
	m := New(context.Background(), Options{Space: "dev", Interval: time.Second, VM: VMInfo{Backend: "firecracker"},
		Home: "/home/dev", Icons: config.DefaultAgentIcons, Motion: false})
	m.w, m.h = w, h
	m.now = fixtureStart
	m.conn.state = connLive
	m.d.applyInventory(fixtureStart, inv)
	for step := 1; step <= 40; step++ {
		m.d.applySample(fixtureStart.Add(time.Duration(step)*time.Second), sample(step))
	}
	m.sampleSeen = m.now
	m.keepFocus()
	return m
}
