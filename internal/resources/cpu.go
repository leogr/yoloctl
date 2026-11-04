package resources

import (
	"errors"
	"fmt"
	"math/bits"
	"path"
	"strconv"
	"strings"
)

// HostCPU is the execution capacity visible to this process. It is neither
// idle capacity nor a reservation: other processes and VMs still share it.
type HostCPU struct {
	// Logical counts the CPUs this process may run on: its Linux affinity
	// mask, which reflects cpuset placement, or macOS's available logical CPUs.
	Logical int
	// Quota/Period is the tightest CFS bandwidth limit visible along this
	// process's cgroup hierarchy, in microseconds. A zero Period means no
	// limit was visible; a cgroup namespace may hide ancestor limits.
	Quota, Period uint64
}

// VMCPUs chooses a new VM's boot vCPU count from every visible execution
// slot. Fractional bandwidth rounds up to whole vCPUs (minimum one), which adds
// virtual slots, not CPU time. A positive maximum clips the result to an
// adapter bound; clipped reports whether it did.
func VMCPUs(host HostCPU, maximum int) (n int, clipped bool, err error) {
	if host.Logical < 1 || (host.Quota == 0) != (host.Period == 0) {
		return 0, false, errors.New("invalid host CPU measurement")
	}
	n = host.Logical
	if host.Period != 0 {
		slots := host.Quota / host.Period
		if host.Quota%host.Period != 0 {
			slots++
		}
		if slots < uint64(n) {
			n = int(slots)
		}
	}
	if maximum > 0 && n > maximum {
		return maximum, true, nil
	}
	return n, false, nil
}

// Describe names the measured basis for a selection message.
func (h HostCPU) Describe() string {
	s := fmt.Sprintf("%d available logical CPUs", h.Logical)
	if h.Period != 0 {
		share := strconv.FormatFloat(float64(h.Quota)/float64(h.Period), 'g', 4, 64)
		s += fmt.Sprintf(", caller-visible cgroup CPU bandwidth %s (%d/%dµs)", share, h.Quota, h.Period)
	}
	return s
}

// tighter reports whether quota a/pa permits less CPU time than b/pb.
func tighter(a, pa, b, pb uint64) bool {
	ah, al := bits.Mul64(a, pb)
	bh, bl := bits.Mul64(b, pa)
	return ah < bh || (ah == bh && al < bl)
}

// procCgroups holds this process's cgroup v2 path and its legacy v1 CPU
// controller path, relative to its cgroup namespace root.
type procCgroups struct {
	v2, v1CPU string
}

func parseProcCgroup(raw string) (procCgroups, error) {
	var c procCgroups
	if raw == "" {
		return c, nil
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(raw, "\n"), "\n") {
		id, rest, ok := strings.Cut(line, ":")
		controllers, p, ok2 := strings.Cut(rest, ":")
		if !ok || !ok2 || id == "" || !strings.HasPrefix(p, "/") {
			return procCgroups{}, errors.New("invalid /proc/self/cgroup entry")
		}
		target := (*string)(nil)
		if id == "0" && controllers == "" {
			target = &c.v2
		} else if hasOption(controllers, "cpu") {
			target = &c.v1CPU
		}
		if target == nil {
			continue
		}
		if *target != "" {
			return procCgroups{}, errors.New("duplicate CPU cgroup membership")
		}
		*target = p
	}
	return c, nil
}

// cgroupMount is one mount of a cgroup v2 hierarchy or a v1 hierarchy that
// carries the CPU controller.
type cgroupMount struct {
	root, point string
	v2          bool
}

func parseMountInfo(raw string) ([]cgroupMount, error) {
	var mounts []cgroupMount
	for line := range strings.SplitSeq(strings.TrimSuffix(raw, "\n"), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields, tail := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 6 || len(tail) < 2 {
			return nil, errors.New("invalid /proc/self/mountinfo entry")
		}
		v2 := tail[0] == "cgroup2"
		if !v2 && (tail[0] != "cgroup" || len(tail) < 3 || !hasOption(tail[2], "cpu")) {
			continue
		}
		root, err := unescapeMountField(fields[3])
		if err != nil {
			return nil, err
		}
		point, err := unescapeMountField(fields[4])
		if err != nil {
			return nil, err
		}
		if !path.IsAbs(root) || !path.IsAbs(point) {
			return nil, errors.New("invalid cgroup mount path")
		}
		mounts = append(mounts, cgroupMount{root: root, point: point, v2: v2})
	}
	return mounts, nil
}

// cgroupDir resolves a membership path to a mount point and a relative path
// beneath it. It uses the broadest containing mount root, so a bind mount of
// the caller's own subtree cannot hide stricter ancestors that another mount
// still exposes. visible is false when that hierarchy is not mounted here. A
// mounted hierarchy that does not contain the path is an error, never a
// reason to read another cgroup's limits.
func cgroupDir(mounts []cgroupMount, v2 bool, member string) (point, rel string, visible bool, err error) {
	if path.Clean(member) != member {
		return "", "", false, fmt.Errorf("cgroup %q is outside this process's cgroup namespace view", member)
	}
	best := -1
	for i, m := range mounts {
		if m.v2 != v2 {
			continue
		}
		visible = true
		r, ok := strings.CutPrefix(member, m.root)
		if !ok || (r != "" && !strings.HasPrefix(r, "/") && m.root != "/") {
			continue
		}
		if best < 0 || len(m.root) < len(mounts[best].root) {
			best, rel = i, strings.Trim(r, "/")
		}
	}
	if !visible {
		return "", "", false, nil
	}
	if best < 0 {
		return "", "", false, fmt.Errorf("cgroup %q is not beneath a visible cgroup mount", member)
	}
	return mounts[best].point, rel, true, nil
}

// parseCPUMax decodes cgroup v2 "$MAX $PERIOD"; limited is false for "max".
func parseCPUMax(raw string) (quota, period uint64, limited bool, err error) {
	fields := strings.Fields(raw)
	if len(fields) != 2 || !strings.HasSuffix(raw, "\n") || strings.Count(raw, "\n") != 1 {
		return 0, 0, false, errors.New("invalid cpu.max")
	}
	period, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0, 0, false, errors.New("invalid cpu.max period")
	}
	if fields[0] == "max" {
		return 0, period, false, nil
	}
	quota, err = strconv.ParseUint(fields[0], 10, 64)
	if err != nil || quota == 0 {
		return 0, 0, false, errors.New("invalid cpu.max quota")
	}
	return quota, period, true, nil
}

// parseCFS decodes legacy cpu.cfs_quota_us/cpu.cfs_period_us; -1 is unlimited.
func parseCFS(quotaRaw, periodRaw string) (quota, period uint64, limited bool, err error) {
	q, qerr := strconv.ParseInt(strings.TrimSuffix(quotaRaw, "\n"), 10, 64)
	period, perr := strconv.ParseUint(strings.TrimSuffix(periodRaw, "\n"), 10, 64)
	if qerr != nil || perr != nil || period == 0 || q == 0 || q < -1 {
		return 0, 0, false, errors.New("invalid CFS bandwidth files")
	}
	if q == -1 {
		return 0, period, false, nil
	}
	return uint64(q), period, true, nil
}

func hasOption(list, name string) bool {
	for option := range strings.SplitSeq(list, ",") {
		if option == name {
			return true
		}
	}
	return false
}

// unescapeMountField reverses mountinfo's octal escapes such as \040.
func unescapeMountField(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+4 > len(s) {
			return "", errors.New("invalid mountinfo escape")
		}
		v, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			return "", errors.New("invalid mountinfo escape")
		}
		b.WriteByte(byte(v))
		i += 3
	}
	return b.String(), nil
}
