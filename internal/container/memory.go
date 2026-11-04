package container

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/resources"
)

// WorkloadMemory describes the aggregate pool, not the inspected box alone.
// Event counters and peak belong to this cgroup's lifetime (usually VM boot).
type WorkloadMemory struct {
	Current uint64            `json:"current_bytes"`
	Peak    uint64            `json:"peak_bytes"`
	High    *uint64           `json:"high_bytes"` // nil means memory.high=max (no early throttle)
	Max     uint64            `json:"max_bytes"`
	Events  map[string]uint64 `json:"events"`
}

func (c *Client) WorkloadMemory(ctx context.Context) (*WorkloadMemory, error) {
	systemd, err := c.workloadSystemd()
	if err != nil {
		return nil, err
	}
	args := []string{"cat"}
	for _, name := range []string{"memory.current", "memory.peak", "memory.high", "memory.max", "memory.events"} {
		args = append(args, filepath.Join(resources.CgroupPath(systemd), name))
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := c.ShellOutputQuiet(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseWorkloadMemory(raw)
}

func parseWorkloadMemory(raw string) (*WorkloadMemory, error) {
	lines := strings.SplitN(raw, "\n", 5)
	if len(lines) != 5 {
		return nil, fmt.Errorf("incomplete workload memory counters")
	}
	m := &WorkloadMemory{Events: map[string]uint64{}}
	var high uint64
	for i, field := range []*uint64{&m.Current, &m.Peak, &high, &m.Max} {
		if i == 2 && strings.TrimSpace(lines[i]) == "max" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(lines[i]), 10, 64)
		if err != nil {
			return nil, err
		}
		*field = n
		if i == 2 {
			m.High = &high
		}
	}
	for line := range strings.SplitSeq(lines[4], "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid workload memory event")
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, err
		}
		m.Events[fields[0]] = n
	}
	if _, ok := m.Events["oom_kill"]; !ok {
		return nil, fmt.Errorf("missing workload OOM counter")
	}
	return m, nil
}
