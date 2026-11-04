package operator

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// RemoveEndpoint withdraws exactly one independently selected registration.
// The lifecycle caller must already have acknowledged removal of its own CID;
// this function never retires other incarnations belonging to the same box.
func RemoveEndpoint(endpoint Endpoint) error {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("operator withdrawal requires native VM-root infrastructure")
	}
	return removeEndpointAt(Root, endpoint, 0, 0)
}

func removeEndpointAt(root string, endpoint Endpoint, uid, gid int) error {
	if err := endpoint.Validate(); err != nil {
		return err
	}
	anchor, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(anchor) }()
	fd, err := unix.Openat2(anchor, strings.TrimPrefix(root, "/"), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var parent unix.Stat_t
	if unix.Fstat(fd, &parent) != nil || parent.Uid != uint32(uid) || parent.Gid != uint32(gid) || parent.Mode&0o7022 != 0 {
		return errors.New("operator cleanup parent is untrusted")
	}
	child, err := unix.Openat2(fd, endpoint.Incarnation, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(child), endpoint.Incarnation)
	defer func() { _ = directory.Close() }()
	var identity unix.Stat_t
	if unix.Fstat(child, &identity) != nil || identity.Uid != uint32(uid) || identity.Gid != uint32(gid) || identity.Mode&0o7777 != 0o755 {
		return errors.New("operator registration directory is untrusted")
	}
	names, err := directory.Readdirnames(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 3 {
		return errors.New("unexpected operator registration member")
	}
	known := make(map[string]unix.Stat_t)
	for _, name := range names {
		mode := uint32(unix.S_IFREG)
		perm := uint32(0o644)
		switch name {
		case "identity.json", "client.json":
		case "control.sock":
			mode = unix.S_IFSOCK
			perm = 0o666
		default:
			return errors.New("unexpected operator registration member")
		}
		var stat unix.Stat_t
		if unix.Fstatat(child, name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || stat.Mode&unix.S_IFMT != mode || stat.Mode&0o7777 != perm || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Nlink != 1 {
			return errors.New("operator registration member is untrusted")
		}
		known[name] = stat
	}
	stat, ok := known["identity.json"]
	if !ok {
		return errors.New("operator registration lacks its identity")
	}
	metadata, err := unix.Openat(child, "identity.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(metadata), "identity.json")
	var opened unix.Stat_t
	check := unix.Fstat(metadata, &opened)
	raw, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	expected, _ := json.Marshal(endpoint)
	if check != nil || readErr != nil || closeErr != nil || opened.Dev != stat.Dev || opened.Ino != stat.Ino || !bytes.Equal(raw, expected) {
		return errors.New("operator registration identity changed")
	}
	// Verify all mappings again before the first unlink. No error is treated as
	// proof of previous cleanup and no name-based fallback follows a refusal.
	for name, old := range known {
		var current unix.Stat_t
		if unix.Fstatat(child, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != old.Dev || current.Ino != old.Ino || current.Mode != old.Mode || current.Uid != old.Uid || current.Gid != old.Gid || current.Nlink != old.Nlink || current.Size != old.Size {
			return errors.New("operator registration changed during cleanup")
		}
	}
	var current unix.Stat_t
	if unix.Fstatat(fd, endpoint.Incarnation, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != identity.Dev || current.Ino != identity.Ino {
		return errors.New("operator registration directory changed")
	}
	for _, name := range []string{"control.sock", "client.json", "identity.json"} {
		if _, ok := known[name]; ok {
			if err := unix.Unlinkat(child, name, 0); err != nil {
				return err
			}
		}
	}
	if err := directory.Sync(); err != nil {
		return err
	}
	if err := unix.Unlinkat(fd, endpoint.Incarnation, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(fd)
}
