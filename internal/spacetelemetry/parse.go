package spacetelemetry

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// ParseProcStat reads the aggregate "cpu" line. Total covers user through
// steal; guest time is already included in user and nice.
func ParseProcStat(raw string) (CPUTimes, error) {
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 5 {
			return CPUTimes{}, errors.New("short cpu line in /proc/stat")
		}
		var t CPUTimes
		for i, f := range fields[1:min(len(fields), 9)] {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return CPUTimes{}, fmt.Errorf("parse /proc/stat: %w", err)
			}
			t.Total += n
			if i == 3 || i == 4 { // idle, iowait
				t.Idle += n
			}
		}
		return t, nil
	}
	return CPUTimes{}, errors.New("no cpu line in /proc/stat")
}

func ParseLoadavg(raw string) ([3]float64, error) {
	var out [3]float64
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return out, errors.New("short /proc/loadavg")
	}
	for i := range out {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return out, fmt.Errorf("parse /proc/loadavg: %w", err)
		}
		out[i] = v
	}
	return out, nil
}

// ParseMeminfo returns MemTotal and MemAvailable in bytes.
func ParseMeminfo(raw string) (total, available uint64, err error) {
	var haveTotal, haveAvail bool
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var dst *uint64
		switch fields[0] {
		case "MemTotal:":
			dst, haveTotal = &total, true
		case "MemAvailable:":
			dst, haveAvail = &available, true
		default:
			continue
		}
		n, perr := strconv.ParseUint(fields[1], 10, 64)
		if perr != nil {
			return 0, 0, fmt.Errorf("parse /proc/meminfo: %w", perr)
		}
		*dst = n * 1024
	}
	if !haveTotal || !haveAvail {
		return 0, 0, errors.New("incomplete /proc/meminfo")
	}
	return total, available, nil
}

func ParseUptime(raw string) (float64, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, errors.New("empty /proc/uptime")
	}
	return strconv.ParseFloat(fields[0], 64)
}

// ParseCgroupPath returns the unified-hierarchy path from /proc/PID/cgroup.
// The result is absolute and clean; callers still confine it to the pool.
func ParseCgroupPath(raw string) (string, error) {
	for line := range strings.SplitSeq(raw, "\n") {
		rest, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		if !strings.HasPrefix(rest, "/") || path.Clean(rest) != rest {
			return "", fmt.Errorf("invalid cgroup path %q", rest)
		}
		return rest, nil
	}
	return "", errors.New("no unified cgroup entry")
}

// ParseKeyed reads "key value" lines such as cpu.stat or memory.events.
func ParseKeyed(raw string) (map[string]uint64, error) {
	out := map[string]uint64{}
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid keyed line %q", line)
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, err
		}
		out[fields[0]] = n
	}
	return out, nil
}

// ParseMemStat picks the view's keys from a cgroup memory.stat file. A key
// the kernel does not report stays nil, never zero.
func ParseMemStat(raw string) (MemStat, error) {
	values, err := ParseKeyed(raw)
	if err != nil {
		return MemStat{}, fmt.Errorf("parse memory.stat: %w", err)
	}
	var s MemStat
	for key, dst := range map[string]**uint64{"anon": &s.Anon, "file": &s.File, "shmem": &s.Shmem, "inactive_file": &s.InactiveFile} {
		if v, ok := values[key]; ok {
			*dst = &v
		}
	}
	return s, nil
}

// ParseLimit reads a single-value limit file where "max" means unlimited (0).
func ParseLimit(raw string) (uint64, error) {
	v := strings.TrimSpace(raw)
	if v == "max" {
		return 0, nil
	}
	return strconv.ParseUint(v, 10, 64)
}

// ParseCPUMax converts "quota period" to cores; "max" means unlimited (0).
func ParseCPUMax(raw string) (float64, error) {
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, fmt.Errorf("invalid cpu.max %q", strings.TrimSpace(raw))
	}
	if fields[0] == "max" {
		return 0, nil
	}
	quota, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	period, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0, fmt.Errorf("invalid cpu.max period %q", fields[1])
	}
	return float64(quota) / float64(period), nil
}

// ParseIOStat sums rbytes and wbytes across devices.
func ParseIOStat(raw string) (read, write uint64, err error) {
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		for _, f := range fields[min(len(fields), 1):] {
			key, value, ok := strings.Cut(f, "=")
			if !ok || key != "rbytes" && key != "wbytes" {
				continue
			}
			n, perr := strconv.ParseUint(value, 10, 64)
			if perr != nil {
				return 0, 0, fmt.Errorf("parse io.stat: %w", perr)
			}
			if key == "rbytes" {
				read += n
			} else {
				write += n
			}
		}
	}
	return read, write, nil
}

// ParseNetDev sums received and transmitted bytes, excluding loopback.
func ParseNetDev(raw string) (rx, tx uint64, err error) {
	for line := range strings.SplitSeq(raw, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.Contains(name, "|") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			return 0, 0, fmt.Errorf("short /proc/net/dev line for %s", name)
		}
		r, err1 := strconv.ParseUint(fields[0], 10, 64)
		t, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("parse /proc/net/dev for %s", name)
		}
		rx += r
		tx += t
	}
	return rx, tx, nil
}
