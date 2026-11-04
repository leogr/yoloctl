// Package resources defines memory budgets separately from VM/runtime adapters.
package resources

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const GiB uint64 = 1 << 30

type HostMemory struct {
	Total     uint64
	Available uint64
}

// VMDefault chooses a one-time allocation, not a reservation or balloon target.
// The host keeps at least 2 GiB or 1/8 of RAM. Both current available memory and
// the full configured capacity of other live yoloctl VMs bound the choice. We
// use the tighter bound instead of counting their resident memory twice.
func VMDefault(host HostMemory, otherVMs uint64) (uint64, error) {
	if host.Total == 0 || host.Available > host.Total {
		return 0, errors.New("invalid host memory measurement")
	}
	reserve := max(2*GiB, host.Total/8)
	if host.Available < reserve || host.Total < reserve || otherVMs > host.Total-reserve {
		return 0, errors.New("insufficient host memory for an automatic 4 GiB VM; free memory or choose --memory explicitly")
	}
	headroom := min(host.Available-reserve, host.Total-reserve-otherVMs)
	target := min(max(host.Total/4, 4*GiB), 16*GiB, headroom)
	target = target / GiB * GiB
	if target < 4*GiB {
		return 0, errors.New("insufficient host memory for an automatic 4 GiB VM; free memory or choose --memory explicitly")
	}
	return target, nil
}

// VMBytes uses the units accepted by both VM adapters. Explicit allocations
// need not fall inside the automatic 4–16 GiB range.
func VMBytes(s string) (uint64, error) {
	for _, unit := range []struct {
		suffix string
		scale  uint64
	}{{"GiB", GiB}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if v, ok := strings.CutSuffix(s, unit.suffix); ok {
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil || n == 0 {
				return 0, fmt.Errorf("invalid resource size %q", s)
			}
			return n * unit.scale, nil
		}
	}
	return 0, fmt.Errorf("resource size %q must use KiB, MiB or GiB", s)
}

func parseMemInfo(raw string) (HostMemory, error) {
	values := map[string]uint64{}
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return HostMemory{}, errors.New("invalid /proc/meminfo memory units")
		}
		n, err := strconv.ParseUint(fields[1], 10, 54)
		if err != nil {
			return HostMemory{}, err
		}
		if _, duplicate := values[fields[0]]; duplicate {
			return HostMemory{}, errors.New("duplicate /proc/meminfo memory field")
		}
		values[fields[0]] = n << 10
	}
	total, ok := values["MemTotal:"]
	available, availableOK := values["MemAvailable:"]
	if !ok || !availableOK || total == 0 || available > total {
		return HostMemory{}, errors.New("missing or inconsistent host memory measurement")
	}
	return HostMemory{Total: total, Available: available}, nil
}

// Darwin has no MemAvailable equivalent. Free, inactive and speculative pages
// provide a conservative reclaimable-memory estimate. Purgeable pages overlap
// these queues; compressed pages remain allocated and are not added again.
func parseVMStat(raw string, total uint64) (HostMemory, error) {
	lines := strings.Split(raw, "\n")
	var pageSize uint64
	if len(lines) == 0 {
		return HostMemory{}, errors.New("empty vm_stat output")
	}
	if _, err := fmt.Sscanf(lines[0], "Mach Virtual Memory Statistics: (page size of %d bytes)", &pageSize); err != nil || pageSize == 0 || pageSize > 1<<20 {
		return HostMemory{}, errors.New("invalid vm_stat page size")
	}
	pages := map[string]uint64{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok || (key != "Pages free" && key != "Pages inactive" && key != "Pages speculative") {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil || n > total/pageSize {
			return HostMemory{}, errors.New("invalid vm_stat page count")
		}
		if _, duplicate := pages[key]; duplicate {
			return HostMemory{}, errors.New("duplicate vm_stat page count")
		}
		pages[key] = n
	}
	if len(pages) != 3 || total == 0 {
		return HostMemory{}, errors.New("missing vm_stat page counts")
	}
	var available uint64
	for _, n := range pages {
		if n*pageSize > total-available {
			return HostMemory{}, errors.New("inconsistent vm_stat memory measurement")
		}
		available += n * pageSize
	}
	return HostMemory{Total: total, Available: available}, nil
}
