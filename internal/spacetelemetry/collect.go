package spacetelemetry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
)

// fileLimit bounds every kernel file read. /proc/stat on a large VM and a busy
// tcp table stay well below it.
const fileLimit = 1 << 20

const (
	// PTYSampleTimeout bounds all holder counter reads of one sample together,
	// so stalled holders cost the sample their evidence, not one bound each.
	PTYSampleTimeout = 500 * time.Millisecond
	// ptyReaders bounds concurrent holder connections per sample.
	ptyReaders = 8
)

// Target is one running box whose counters are sampled. PID is the
// container's init process; ID, when known, must appear in the box's cgroup
// name so a reused PID cannot redirect the read to another workload.
type Target struct {
	Name string
	PID  int
	ID   string
	// Session is set when the box has a running session whose holder can
	// report PTY counters. SessionUnknown is set when the session state
	// could not be read: its counters are unread, never "no session".
	Session        bool
	SessionUnknown bool
}

// Collector reads guest counters. Roots are injectable for tests; PoolRel is
// the shared workload cgroup relative to CgroupRoot.
type Collector struct {
	ProcRoot   string
	CgroupRoot string
	PoolRel    string
	DiskMount  string
	// SystemMount is the VM's root filesystem, the system disk.
	SystemMount string
	// Statfs reports total and available bytes for the data and system disks.
	Statfs func(path string) (Usage, error)
	// PTYStats reads a box's session holder counters and must return once ctx
	// ends; nil reports them as unavailable for every session.
	PTYStats func(ctx context.Context, box string) (PTYCounters, error)
}

func (c Collector) read(root, rel string) (string, error) {
	raw, err := safepath.ReadFile(root, rel, fileLimit)
	return string(raw), err
}

// Sample reads one consistent-enough round of counters. Individual failures
// are reported in place so one vanished box does not hide the rest. Ending ctx
// ends the holder reads too.
func (c Collector) Sample(ctx context.Context, targets []Target) *Sample {
	s := &Sample{Boxes: map[string]BoxSample{}}
	var errs []string
	if raw, err := c.read(c.ProcRoot, "stat"); err != nil {
		errs = append(errs, err.Error())
	} else if s.CPU, err = ParseProcStat(raw); err != nil {
		errs = append(errs, err.Error())
	}
	if raw, err := c.read(c.ProcRoot, "loadavg"); err == nil {
		s.Load, _ = ParseLoadavg(raw)
	}
	if raw, err := c.read(c.ProcRoot, "meminfo"); err != nil {
		errs = append(errs, err.Error())
	} else if s.MemTotal, s.MemAvailable, err = ParseMeminfo(raw); err != nil {
		errs = append(errs, err.Error())
	}
	if raw, err := c.read(c.ProcRoot, "uptime"); err == nil {
		s.Uptime, _ = ParseUptime(raw)
	}
	if pool, err := c.pool(); err != nil {
		s.PoolError = err.Error()
	} else {
		s.Pool = pool
	}
	if c.DiskMount != "" && c.Statfs != nil {
		if usage, err := c.Statfs(c.DiskMount); err == nil {
			s.Disk = &usage
		}
	}
	if c.SystemMount != "" && c.Statfs != nil {
		if usage, err := c.Statfs(c.SystemMount); err == nil {
			s.SystemDisk = &usage
		}
	}
	ptys := c.ptys(ctx, targets)
	for _, t := range targets {
		box, err := c.box(t)
		if err != nil {
			box = BoxSample{Error: err.Error()}
		}
		box.Container = t.ID
		if r, ok := ptys[t.Name]; ok {
			box.PTY, box.PTYError = r.counters, r.err
		}
		s.Boxes[t.Name] = box
	}
	s.Error = strings.Join(errs, "; ")
	return s
}

func (c Collector) pool() (*Pool, error) {
	p := &Pool{}
	stat, err := c.keyed(c.PoolRel + "/cpu.stat")
	if err != nil {
		return nil, err
	}
	p.CPUUsec = stat["usage_usec"]
	for name, dst := range map[string]*uint64{"memory.current": &p.Current, "memory.max": &p.Max, "memory.peak": &p.Peak} {
		raw, err := c.read(c.CgroupRoot, c.PoolRel+"/"+name)
		if err != nil {
			if name == "memory.peak" {
				continue // older kernels have no peak; it stays unavailable
			}
			return nil, err
		}
		if *dst, err = ParseLimit(raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	events, err := c.keyed(c.PoolRel + "/memory.events")
	if err != nil {
		return nil, err
	}
	oom, ok := events["oom_kill"]
	if !ok {
		return nil, errors.New("missing workload OOM counter")
	}
	p.OOMKills = oom
	return p, nil
}

func (c Collector) keyed(rel string) (map[string]uint64, error) {
	raw, err := c.read(c.CgroupRoot, rel)
	if err != nil {
		return nil, err
	}
	return ParseKeyed(raw)
}

// BoxCgroup resolves a box's cgroup directory (relative to CgroupRoot) from
// its init process, confined to one direct child of the workload pool.
func (c Collector) BoxCgroup(t Target) (string, error) {
	if t.PID <= 0 {
		return "", errors.New("box has no running init process")
	}
	raw, err := c.read(c.ProcRoot, strconv.Itoa(t.PID)+"/cgroup")
	if err != nil {
		return "", err
	}
	full, err := ParseCgroupPath(raw)
	if err != nil {
		return "", err
	}
	rest, ok := strings.CutPrefix(strings.TrimPrefix(full, "/"), c.PoolRel+"/")
	if !ok {
		return "", fmt.Errorf("box process is outside the workload pool (%s)", full)
	}
	child, _, _ := strings.Cut(rest, "/")
	if child == "" || child == "." || child == ".." {
		return "", fmt.Errorf("invalid box cgroup %s", full)
	}
	if t.ID != "" && !strings.Contains(child, t.ID) {
		return "", fmt.Errorf("box cgroup %s does not belong to container %s", child, shortID(t.ID))
	}
	return c.PoolRel + "/" + child, nil
}

func (c Collector) box(t Target) (BoxSample, error) {
	var b BoxSample
	cg, err := c.BoxCgroup(t)
	if err != nil {
		return b, err
	}
	stat, err := c.keyed(cg + "/cpu.stat")
	if err != nil {
		return b, err
	}
	b.CPUUsec = stat["usage_usec"]
	if raw, err := c.read(c.CgroupRoot, cg+"/cpu.max"); err == nil {
		b.CPULimit, _ = ParseCPUMax(raw)
	}
	raw, err := c.read(c.CgroupRoot, cg+"/memory.current")
	if err != nil {
		return b, err
	}
	if b.MemCurrent, err = ParseLimit(raw); err != nil {
		return b, err
	}
	// An unread limit is named: it must not read as "no per-box limit".
	if raw, err := c.read(c.CgroupRoot, cg+"/memory.max"); err != nil {
		b.Unavailable = append(b.Unavailable, SignalMemMax)
	} else if b.MemMax, err = ParseLimit(raw); err != nil {
		b.MemMax = 0
		b.Unavailable = append(b.Unavailable, SignalMemMax)
	}
	// The breakdown only qualifies the charge: its failure is named and
	// never discards a valid memory.current.
	if raw, err := c.read(c.CgroupRoot, cg+"/memory.stat"); err != nil {
		b.Unavailable = append(b.Unavailable, SignalMemStat)
	} else if stat, err := ParseMemStat(raw); err != nil {
		b.Unavailable = append(b.Unavailable, SignalMemStat)
	} else {
		b.MemStat = &stat
	}
	if raw, err := c.read(c.CgroupRoot, cg+"/pids.current"); err == nil {
		b.Pids, _ = ParseLimit(raw)
	}
	// Disk and network bytes feed resource rates: a failed read is named,
	// never reported as zero bytes.
	if raw, err := c.read(c.CgroupRoot, cg+"/io.stat"); err != nil {
		b.Unavailable = append(b.Unavailable, SignalIO)
	} else if r, w, err := ParseIOStat(raw); err != nil {
		b.Unavailable = append(b.Unavailable, SignalIO)
	} else {
		b.IORead, b.IOWrite = r, w
	}
	if raw, err := c.read(c.ProcRoot, strconv.Itoa(t.PID)+"/net/dev"); err != nil {
		b.Unavailable = append(b.Unavailable, SignalNet)
	} else if rx, tx, err := ParseNetDev(raw); err != nil {
		b.Unavailable = append(b.Unavailable, SignalNet)
	} else {
		b.NetRx, b.NetTx = rx, tx
	}
	return b, nil
}

type ptyResult struct {
	counters *PTYCounters
	err      string
}

// ptys reads the holder counters of every session concurrently, under one
// deadline shared by the whole sample.
func (c Collector) ptys(ctx context.Context, targets []Target) map[string]ptyResult {
	out := map[string]ptyResult{}
	var names []string
	for _, t := range targets {
		switch {
		case t.Session:
			out[t.Name] = ptyResult{err: "session counters unavailable"}
			names = append(names, t.Name)
		case t.SessionUnknown:
			out[t.Name] = ptyResult{err: "session state unknown"}
		}
	}
	if c.PTYStats == nil || len(names) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, PTYSampleTimeout)
	defer cancel()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, ptyReaders)
	)
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r ptyResult
			select {
			case sem <- struct{}{}:
				counters, err := c.PTYStats(ctx, name)
				<-sem
				if err != nil {
					r = ptyResult{err: err.Error()}
				} else {
					r = ptyResult{counters: &counters}
				}
			case <-ctx.Done():
				r = ptyResult{err: "session counters unavailable: " + context.Cause(ctx).Error()}
			}
			mu.Lock()
			out[name] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
