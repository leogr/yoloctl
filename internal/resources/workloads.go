package resources

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	WorkloadSlice   = "yoloctl-workloads.slice"
	WorkloadGroup   = "/yoloctl-workloads"
	WorkloadLabel   = "yoloctl.workload_pool"
	WorkloadVersion = "1"
)

type WorkloadBudget struct {
	Reserve uint64
	Max     uint64
}

// Workloads leaves control-plane headroom outside a shared cgroup. Explicit
// VMs below 2 GiB retain half their usable RAM for infrastructure instead of
// trying to reserve a whole GiB. This is a limit, not a memory reservation.
func Workloads(total uint64) (WorkloadBudget, error) {
	if total < 64<<20 {
		return WorkloadBudget{}, errors.New("too little guest memory for a workload pool")
	}
	reserve := min(max(GiB, total/8), total/2)
	// Whole MiB limits survive the kernel's page-size rounding on both arches.
	limit := (total - reserve) >> 20 << 20
	return WorkloadBudget{Reserve: total - limit, Max: limit}, nil
}

func CgroupParent(systemd bool) string {
	if systemd {
		return WorkloadSlice
	}
	return WorkloadGroup
}

func CgroupPath(systemd bool) string {
	if systemd {
		return "/sys/fs/cgroup/yoloctl.slice/" + WorkloadSlice
	}
	return "/sys/fs/cgroup" + WorkloadGroup
}

func (b WorkloadBudget) SliceUnit() []byte {
	// There is no pressure-management agent to relieve a memory.high breach.
	// A lower high limit would throttle allocations for minutes before they
	// reach memory.max, stalling interactive siblings as well. Keep the hard
	// limit and explicitly disable this early throttle on both VM backends.
	return []byte(fmt.Sprintf("[Unit]\nDescription=yoloctl shared box and build memory budget\n[Slice]\nMemoryAccounting=yes\nMemoryHigh=infinity\nMemoryMax=%d\nMemorySwapMax=0\n", b.Max))
}

const BuilderDropIn = "[Service]\nSlice=" + WorkloadSlice + "\nRestart=on-failure\nRestartSec=2\nOOMPolicy=stop\n"

// BuildkitWorkloads preserves registry and GC settings while placing RUN
// steps in the same aggregate pool as buildkitd and boxes. Systemd's OCI path
// notation and runc option must agree; setting only the daemon's slice leaves
// containerd-launched build steps outside that slice.
func BuildkitWorkloads(raw []byte, systemd bool) ([]byte, error) {
	var config map[string]any
	if err := toml.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	workers, _ := config["worker"].(map[string]any)
	worker, _ := workers["containerd"].(map[string]any)
	if worker == nil || worker["enabled"] != true || worker["namespace"] != "default" {
		return nil, errors.New("workload pool requires the default containerd BuildKit worker")
	}
	before, err := toml.Marshal(config)
	if err != nil {
		return nil, err
	}
	parent := WorkloadGroup
	if systemd {
		parent = WorkloadSlice + ":buildkit:"
	}
	worker["defaultCgroupParent"] = parent
	runtime, err := table(worker, "runtime")
	if err != nil {
		return nil, err
	}
	if name, ok := runtime["name"]; ok && name != "io.containerd.runc.v2" {
		return nil, errors.New("workload pool requires the containerd runc v2 runtime")
	}
	runtime["name"] = "io.containerd.runc.v2"
	options, err := table(runtime, "options")
	if err != nil {
		return nil, err
	}
	options["SystemdCgroup"] = systemd
	after, err := toml.Marshal(config)
	if bytes.Equal(before, after) {
		return bytes.Clone(raw), err
	}
	return after, err
}

func table(parent map[string]any, key string) (map[string]any, error) {
	if value, ok := parent[key]; ok {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("BuildKit %s is not a table", key)
		}
		return m, nil
	}
	m := map[string]any{}
	parent[key] = m
	return m, nil
}

// PoolFiles are fixed read-only probes; they never accept a guest/user path.
func PoolFiles(systemd bool) []string {
	dir := CgroupPath(systemd)
	return []string{filepath.Join(dir, "memory.max"), filepath.Join(dir, "memory.high"), filepath.Join(dir, "memory.swap.max"), "/proc/meminfo"}
}

// ValidatePool checks actual kernel limits, not an installation marker. Older
// guest images must not silently run newly unlimited boxes outside protection.
func ValidatePool(raw string) error {
	lines := strings.SplitN(raw, "\n", 4)
	if len(lines) != 4 {
		return errors.New("incomplete workload memory budget")
	}
	mem, err := parseMemInfo(lines[3])
	if err != nil {
		return err
	}
	budget, err := Workloads(mem.Total)
	if err != nil {
		return err
	}
	for i, want := range []string{strconv.FormatUint(budget.Max, 10), "max", "0"} {
		if strings.TrimSpace(lines[i]) != want {
			return errors.New("workload memory limits do not match guest memory policy")
		}
	}
	return nil
}
