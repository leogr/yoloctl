package observe

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// mkdirAt is replaceable so tests can simulate a full tmpfs.
var mkdirAt = unix.Mkdirat

// Prepare creates gen-<gen> for one launch, owned by the sandbox uid/gid and
// private to it. It runs before the holder starts. Only the controller can
// create entries in the root-owned root, so no sandbox process can pre-seed a
// future generation; a directory left by an earlier attempt at the same
// generation is reused only while untouched.
//
// Evidence it may discard, and nothing else:
//   - obsolete generations (gen-<k>, k < gen-1), removed before creating, so
//     junk left in them cannot exhaust the caps ahead of this launch;
//   - the previous generation gen-<gen-1>, only when creation then still finds
//     the caps exhausted and the caller has proven that launch's holder gone.
//     Creation is retried once; droppedPrevious tells the caller to show that
//     launch's unread reports as a gap.
//
// Other entries are left untouched. Every failure is ErrUnavailable; nothing
// here launches or replays a holder.
func Prepare(dir string, gen uint64, uid, gid int, previousGone bool) (droppedPrevious bool, err error) {
	if gen == 0 {
		return false, unavailable("generation 0")
	}
	root, err := openRoot(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(root) }()
	names, err := readNames(root, RootInodes)
	if err != nil {
		return false, unavailable("list observation root: %v", err)
	}
	for _, entry := range names {
		if k, ok := parseGenName(entry); ok && gen > 1 && k < gen-1 {
			if err := removeAt(root, entry, 0); err != nil {
				return false, unavailable("prune %s: %v", entry, err)
			}
		}
	}
	name := genName(gen)
	err = createGeneration(root, name, uid, gid)
	if gen > 1 && previousGone && (errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.EDQUOT)) {
		if err := removeAt(root, genName(gen-1), 0); err != nil {
			return false, unavailable("prune %s: %v", genName(gen-1), err)
		}
		droppedPrevious = true
		err = createGeneration(root, name, uid, gid)
	}
	return droppedPrevious, err
}

// parseGenName accepts only the canonical names genName produces.
func parseGenName(name string) (uint64, bool) {
	digits, ok := strings.CutPrefix(name, "gen-")
	if !ok || digits == "" {
		return 0, false
	}
	k, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || k == 0 || strconv.FormatUint(k, 10) != digits {
		return 0, false
	}
	return k, true
}

func createGeneration(root int, name string, uid, gid int) error {
	err := mkdirAt(root, name, 0o700)
	created := err == nil
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("%w: create %s: %w", ErrUnavailable, name, err)
	}
	fd, err := unix.Openat(root, name, dirFlags, 0)
	if err != nil {
		return unavailable("open %s: %v", name, err)
	}
	defer func() { _ = unix.Close(fd) }()
	if created {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return unavailable("chown %s: %v", name, err)
		}
		if err := unix.Fchmod(fd, 0o700); err != nil {
			return unavailable("chmod %s: %v", name, err)
		}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return unavailable("stat %s: %v", name, err)
	}
	if st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0o7777 != 0o700 {
		return unavailable("%s is not the sandbox's private generation directory", name)
	}
	if !created {
		if names, err := readNames(fd, 0); err != nil || len(names) != 0 {
			return unavailable("%s was already used by an earlier launch attempt", name)
		}
	}
	return nil
}
