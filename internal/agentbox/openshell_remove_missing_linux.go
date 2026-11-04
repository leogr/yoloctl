//go:build linux

package agentbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// CheckMissingNativeRemoval runs before the existing missing-container destroy
// cleanup. That cleanup follows the old path: each of its own steps decides by
// its OK/error. Native runtime records never refuse it. A same-box record whose
// container is gone (for example after `space recreate` replaced the VM system
// disk that held the containers, while the records stay on the data disk) is
// left in place and named in a returned notice; an unreadable record is named
// too. No record is deleted here, and a later create of the same name does not
// read these records. Only the current native storage identity is required.
func (a LiveApplier) CheckMissingNativeRemoval(ctx context.Context, name string) ([]string, error) {
	if !spaceenv.Native() {
		return nil, nil
	}
	if a.VerifyStorage == nil {
		return nil, errors.New("missing-runtime removal requires current native storage")
	}
	if err := layout.ValidateName("agentbox", name); err != nil {
		return nil, err
	}
	if err := a.VerifyStorage(ctx); err != nil {
		return nil, err
	}
	control, err := layout.OpenShellControlDir()
	if err != nil {
		return nil, err
	}
	current := spaceenv.Current()
	if current == nil {
		return nil, errors.New("missing-runtime removal requires the native space identity")
	}
	return nativeRemovalNotices(ctx, filepath.Join(control, "containers"), name,
		nativeRemovalSpace{current.Space, current.DiskID, current.DiskUUID}, 0, 0), nil
}

type nativeRemovalSpace struct{ Space, DiskID, DiskUUID string }

const nativeRemovalScanLimit = 4096

// nativeRemovalNotices reads root-owned origin markers without following links.
// It reports, never refuses: same-box records are retained, unreadable ones are
// left in place. uid/gid are the expected owner (root in production; the
// current user in project fixtures).
func nativeRemovalNotices(ctx context.Context, path, name string, space nativeRemovalSpace, uid, gid uint32) []string {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return []string{fmt.Sprintf("native runtime records at %s could not be read (%v); left in place", path, err)}
	}
	root := os.NewFile(uintptr(fd), "native origin inventory")
	defer func() { _ = root.Close() }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uid || st.Gid != gid || st.Mode&0o7022 != 0 {
		return []string{fmt.Sprintf("native runtime records at %s have unexpected ownership/mode; left in place", path)}
	}
	names, err := root.Readdirnames(nativeRemovalScanLimit + 1)
	var notices []string
	if err != nil && !errors.Is(err, io.EOF) {
		notices = append(notices, fmt.Sprintf("native runtime records at %s could not be listed completely (%v)", path, err))
	}
	if len(names) > nativeRemovalScanLimit {
		names = names[:nativeRemovalScanLimit]
		notices = append(notices, fmt.Sprintf("native runtime records at %s exceed %d entries; only the first were checked", path, nativeRemovalScanLimit))
	}
	sort.Strings(names)
	for _, entry := range names {
		if ctx.Err() != nil {
			return append(notices, fmt.Sprintf("native runtime record check at %s was interrupted", path))
		}
		record := filepath.Join(path, entry)
		box, err := readNativeRemovalOrigin(fd, st.Dev, entry, space, uid, gid)
		switch {
		case err != nil:
			notices = append(notices, fmt.Sprintf("native runtime record %s could not be read (%v); left in place", record, err))
		case box == name:
			notices = append(notices, fmt.Sprintf("stale native runtime record %s for box %s is retained (not deleted)", record, name))
		}
	}
	return notices
}

// readNativeRemovalOrigin returns the box named by one canonical origin marker.
func readNativeRemovalOrigin(rootFD int, dev uint64, entry string, space nativeRemovalSpace, uid, gid uint32) (string, error) {
	if !nativeLowerHex(entry, 16) {
		return "", errors.New("unknown entry")
	}
	child, err := unix.Openat(rootFD, entry, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	var directory unix.Stat_t
	if unix.Fstat(child, &directory) != nil || directory.Uid != uid || directory.Gid != gid || directory.Mode&0o7022 != 0 || directory.Dev != dev {
		_ = unix.Close(child)
		return "", errors.New("unexpected directory ownership/mode")
	}
	origin, err := unix.Openat(child, "origin.json", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	_ = unix.Close(child)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(origin), "native origin marker")
	defer func() { _ = f.Close() }()
	var si unix.Stat_t
	if unix.Fstat(origin, &si) != nil || si.Mode&unix.S_IFMT != unix.S_IFREG || si.Mode&0o7777 != 0o600 || si.Uid != uid || si.Gid != gid || si.Nlink != 1 || si.Dev != dev {
		return "", errors.New("unexpected marker ownership/mode")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return "", errors.Join(errors.New("marker unavailable or beyond bound"), err)
	}
	var item nativeRemovalOrigin
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	err = dec.Decode(&item)
	canonical, encodeErr := json.Marshal(item)
	valid := err == nil && encodeErr == nil && bytes.Equal(raw, canonical)
	clear(raw)
	if !valid || item.Space != space.Space || item.DiskID != space.DiskID || item.DiskUUID != space.DiskUUID || item.Incarnation != entry || layout.ValidateName("agentbox", item.Box) != nil || !nativeLowerHex(item.Qualification, 32) || !nativeLowerHex(item.HelperSHA256, 32) {
		return "", errors.New("partial or foreign marker")
	}
	return item.Box, nil
}

type nativeRemovalOrigin struct {
	Space         string `json:"space"`
	DiskID        string `json:"disk_id"`
	DiskUUID      string `json:"disk_uuid"`
	Box           string `json:"box"`
	Incarnation   string `json:"incarnation"`
	Qualification string `json:"qualification"`
	HelperSHA256  string `json:"helper_sha256"`
}
