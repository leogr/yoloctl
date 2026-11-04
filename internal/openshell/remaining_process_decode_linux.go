package openshell

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

const nativeProcessMetadataLimit = 64 << 10

// Linux MAX_PID_NS_LEVEL is 32; level zero is also emitted by proc status.
const nativeProcessNamespacePIDLimit = 33

// These are observations, not activation/liveness or harmless-process claims.
// In particular, stable zombies and zero start ticks remain explicit census
// members. The existing live activation decoder deliberately stays unchanged.
type nativeProcessStat struct {
	tid    int
	start  string
	zombie bool
}

type nativeProcessStatus struct {
	kernelThread bool
	nspids       []int
}

func nativeProcessNumber(name string) (int, bool, error) {
	if name == "" {
		return 0, false, errors.New("empty process directory name")
	}
	for _, c := range []byte(name) {
		if c < '0' || c > '9' {
			return 0, false, nil
		}
	}
	n, err := strconv.ParseUint(name, 10, 31)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != name {
		return 0, false, errors.New("noncanonical process directory identity")
	}
	return int(n), true, nil
}

func decodeNativeProcessStat(raw []byte, tid int) (nativeProcessStat, error) {
	if tid <= 0 || len(raw) == 0 || len(raw) > nativeProcessMetadataLimit || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return nativeProcessStat{}, errors.New("missing or unbounded process stat")
	}
	text := string(raw)
	open, close := strings.Index(text, " ("), strings.LastIndex(text, ") ")
	if open < 1 || close <= open || text[:open] != strconv.Itoa(tid) {
		return nativeProcessStat{}, errors.New("process stat identity differs from retained task")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 || len(fields[0]) != 1 || !strings.Contains("RSDTtPIZ", fields[0]) {
		return nativeProcessStat{}, errors.New("missing, exited or unknown task state")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || strconv.FormatUint(start, 10) != fields[19] {
		return nativeProcessStat{}, errors.New("noncanonical process start identity")
	}
	return nativeProcessStat{tid: tid, start: fields[19], zombie: fields[0] == "Z"}, nil
}

func decodeNativeProcessStatus(raw []byte, pid, tid int) (nativeProcessStatus, error) {
	if pid <= 0 || tid <= 0 || len(raw) == 0 || len(raw) > nativeProcessMetadataLimit || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return nativeProcessStatus{}, errors.New("missing or unbounded process status")
	}
	var result nativeProcessStatus
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "Tgid" && key != "Pid" && key != "Kthread" && key != "NSpid" {
			continue
		}
		if seen[key] {
			return nativeProcessStatus{}, errors.New("duplicate process status identity")
		}
		seen[key] = true
		fields := strings.Fields(value)
		if key == "Kthread" {
			if len(fields) != 1 || fields[0] != "0" && fields[0] != "1" {
				return nativeProcessStatus{}, errors.New("invalid kernel-thread observation")
			}
			result.kernelThread = fields[0] == "1"
			continue
		}
		if key == "NSpid" {
			if len(fields) == 0 || len(fields) > nativeProcessNamespacePIDLimit {
				return nativeProcessStatus{}, errors.New("invalid task PID namespace mapping")
			}
			for _, field := range fields {
				n, numeric, err := nativeProcessNumber(field)
				if err != nil || !numeric {
					return nativeProcessStatus{}, errors.New("invalid task namespace PID")
				}
				result.nspids = append(result.nspids, n)
			}
			if result.nspids[0] != tid {
				return nativeProcessStatus{}, errors.New("task namespace mapping differs from proc view")
			}
			continue
		}
		want := tid
		if key == "Tgid" {
			want = pid
		}
		if len(fields) != 1 || fields[0] != strconv.Itoa(want) {
			return nativeProcessStatus{}, errors.New("task PID/TGID membership differs from proc view")
		}
	}
	if len(seen) != 4 {
		return nativeProcessStatus{}, errors.New("incomplete task status observations")
	}
	return result, nil
}

type nativeProcessProcMount struct {
	identity pinFileIdentity
	point    string
	row      string
}

// A correct procfs type alone does not establish an unfiltered process view.
// Mount options are kernel observations, not caller-provided visibility flags.
func decodeNativeProcessProcMount(raw []byte, identity pinFileIdentity) (nativeProcessProcMount, error) {
	if identity.device == 0 || identity.inode == 0 || identity.mount == 0 {
		return nativeProcessProcMount{}, errors.New("proc view lacks a complete descriptor mapping")
	}
	mounts, err := parseNativePinMounts(raw)
	if err != nil {
		return nativeProcessProcMount{}, err
	}
	var result nativeProcessProcMount
	for _, mount := range mounts {
		if mount.id == identity.mount {
			if mount.filesystem != "proc" || mount.root != "/" || mount.device != identity.device {
				return nativeProcessProcMount{}, errors.New("retained proc descriptor is not a complete proc mount")
			}
			result.identity, result.point = identity, mount.point
		}
	}
	if result.point == "" {
		return nativeProcessProcMount{}, errors.New("retained proc mount is absent from reader view")
	}
	for _, mount := range mounts {
		if mount.id == identity.mount {
			continue
		}
		if mount.point == result.point {
			return nativeProcessProcMount{}, errors.New("proc mount is obscured by another mount")
		}
		if rest, under := strings.CutPrefix(mount.point, strings.TrimSuffix(result.point, "/")+"/"); under {
			first, _, _ := strings.Cut(rest, "/")
			_, numeric, numberErr := nativeProcessNumber(first)
			if numeric || numberErr != nil || first == "self" || first == "thread-self" {
				return nativeProcessProcMount{}, errors.New("proc task or reader paths are obscured by a mount")
			}
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != strconv.FormatUint(identity.mount, 10) {
			continue
		}
		separator := 6
		for fields[separator] != "-" {
			separator++ // Complete shape was checked by parseNativePinMounts.
		}
		for _, options := range []string{fields[5], fields[separator+3]} {
			if err := validateNativeProcessMountOptions(options); err != nil {
				return nativeProcessProcMount{}, err
			}
		}
		seen := make(map[string]bool)
		for _, optional := range fields[6:separator] {
			key, value, hasValue := strings.Cut(optional, ":")
			if seen[key] {
				return nativeProcessProcMount{}, errors.New("duplicate proc mount propagation field")
			}
			seen[key] = true
			if key == "unbindable" && !hasValue {
				continue
			}
			n, err := strconv.ParseUint(value, 10, 64)
			if !hasValue || key != "shared" && key != "master" && key != "propagate_from" || err != nil || n == 0 || strconv.FormatUint(n, 10) != value {
				return nativeProcessProcMount{}, errors.New("unknown proc mount propagation field")
			}
		}
		result.row = strings.Join(fields, " ")
	}
	return result, nil
}

func validateNativeProcessMountOptions(raw string) error {
	seen := make(map[string]bool)
	for _, option := range strings.Split(raw, ",") {
		key, value, hasValue := strings.Cut(option, "=")
		if seen[key] {
			return errors.New("duplicate proc view option")
		}
		seen[key] = true
		switch key {
		case "rw", "ro", "nosuid", "nodev", "noexec", "relatime", "strictatime", "noatime", "nodiratime", "lazytime", "sync", "dirsync", "seclabel":
			if hasValue {
				return errors.New("unexpected proc mount option value")
			}
		case "hidepid":
			if !hasValue || value != "0" && value != "off" {
				return errors.New("filtered proc process visibility")
			}
		case "gid":
			n, err := strconv.ParseUint(value, 10, 32)
			if !hasValue || err != nil || strconv.FormatUint(n, 10) != value {
				return errors.New("invalid proc visibility group")
			}
		default:
			// subset=pid hides non-task metadata, not all tasks. This initial
			// observer refuses it/unknown views rather than inferring coverage.
			return errors.New("unsupported or unknown proc view option")
		}
	}
	if seen["rw"] == seen["ro"] {
		return errors.New("ambiguous proc mount access mode")
	}
	return nil
}
