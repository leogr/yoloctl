package agentbox

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

type forwardStatus struct {
	Schema      int                 `json:"schema"`
	Unit        string              `json:"unit"`
	Spec        port.Spec           `json:"spec"`
	Binding     port.ForwardBinding `json:"binding"`
	PID         int                 `json:"pid"`
	StartTicks  string              `json:"start_ticks"`
	SocketFD    int                 `json:"socket_fd"`
	SocketInode uint64              `json:"socket_inode"`
}

func decodeForwardStatus(raw []byte) (forwardStatus, error) {
	var status forwardStatus
	if len(raw) == 0 || len(raw) > 64<<10 {
		return status, errors.New("forward status exceeds bounds")
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return status, err
	}
	canonical, err := json.Marshal(status)
	if err != nil || !bytes.Equal(canonical, raw) {
		return status, errors.New("noncanonical/unknown/duplicate forward status data")
	}
	start, parseErr := strconv.ParseUint(status.StartTicks, 10, 64)
	if parseErr != nil || start == 0 || strconv.FormatUint(start, 10) != status.StartTicks {
		return status, errors.New("invalid forward status process start")
	}
	name, err := port.OpenShellUnitName(status.Spec)
	if err != nil || status.Schema != 1 || status.Unit != name || status.PID <= 1 || status.SocketFD < 3 || status.SocketFD > 1<<20 || status.SocketInode == 0 {
		return status, errors.New("invalid forward status instance")
	}
	if _, err := port.ForwardWorkerArgs(status.Spec, status.Binding); err != nil {
		return status, err
	}
	return status, nil
}

func readForwardStatusLocked(directory, name string, uid, gid int) ([]byte, forwardStatus, error) {
	raw, info, err := safepath.ReadFileInfo(directory, name, 64<<10)
	if err != nil {
		return nil, forwardStatus{}, err
	}
	if err := runtimeMetadataOwner(info, 0o600, uid, gid); err != nil {
		return nil, forwardStatus{}, err
	}
	status, err := decodeForwardStatus(raw)
	return raw, status, err
}

func (s forwardStatusStore) publishLocked(spec port.Spec, binding port.ForwardBinding, process forwardProcessIdentity, socket forwardListenerIdentity) ([]byte, error) {
	directory, uid, gid := s.directory, s.uid, s.gid
	unit, err := port.OpenShellUnitName(spec)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(forwardStatus{1, unit, spec, binding, process.PID, process.StartTicks, socket.FD, socket.Inode})
	if err != nil {
		return nil, err
	}
	if _, err := decodeForwardStatus(raw); err != nil {
		return nil, err
	}
	name := binding.Attempt + ".json"
	_, previous, err := readForwardStatusLocked(directory, name, uid, gid)
	if err == nil {
		// A supervised restart may update its own observation after exclusive
		// bind. It never repairs or adopts another descriptor/attempt's state.
		if previous.Spec != spec || previous.Binding != binding {
			return nil, errors.New("forward status belongs to another service")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := safepath.WriteFileOwned(directory, name, raw, 0o600, uid, gid); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s forwardStatusStore) removeLocked(name string, expected []byte) error {
	directory, uid, gid := s.directory, s.uid, s.gid
	raw, _, err := readForwardStatusLocked(directory, name, uid, gid)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, expected) {
		return errors.New("forward status changed; refusing foreign cleanup")
	}
	return safepath.RemoveFile(directory, name)
}

// Every reader and mutation uses the same stable per-attempt lock. The empty
// lock inode is never replaced or removed while a generation may have workers;
// only fully stopped generation teardown may remove its directory.
type forwardStatusStore struct {
	directory string
	uid, gid  int
	// Optional contention observation is used by deterministic protocol tests;
	// it cannot select paths, bypass a lock or grant readiness.
	contended func()
}

func (s forwardStatusStore) transaction(ctx context.Context, name string, work func() error) (err error) {
	attempt, ok := strings.CutSuffix(name, ".json")
	raw, decodeErr := hex.DecodeString(attempt)
	if !ok || decodeErr != nil || len(raw) != 16 || hex.EncodeToString(raw) != attempt {
		return errors.New("invalid forward status attempt name")
	}
	root, err := unix.Open(s.directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unix.Close(root)) }()
	var directory unix.Stat_t
	if unix.Fstat(root, &directory) != nil || directory.Uid != uint32(s.uid) || directory.Gid != uint32(s.gid) || directory.Mode&0o7777 != 0o700 {
		return errors.New("forward status directory is not private to its owner")
	}
	lockName := attempt + ".lock"
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	lock, err := unix.Openat(root, lockName, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		lock, err = unix.Openat(root, lockName, flags, 0)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unix.Close(lock)) }()
	var identity unix.Stat_t
	if unix.Fstat(lock, &identity) != nil || identity.Mode&unix.S_IFMT != unix.S_IFREG || identity.Uid != uint32(s.uid) || identity.Gid != uint32(s.gid) || identity.Mode&0o7777 != 0o600 || identity.Nlink != 1 || identity.Size != 0 {
		return errors.New("forward status lock is not a private stable regular file")
	}
	if created {
		if err := unix.Fsync(lock); err != nil {
			return err
		}
		if err := unix.Fsync(root); err != nil {
			return err
		}
	}
	notified := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if !notified && s.contended != nil {
			s.contended()
			notified = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer func() { err = errors.Join(err, unix.Flock(lock, unix.LOCK_UN)) }()
	var current unix.Stat_t
	if unix.Fstatat(root, lockName, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != identity.Dev || current.Ino != identity.Ino || current.Nlink != 1 {
		return errors.New("forward status lock identity changed")
	}
	return work()
}

func (s forwardStatusStore) publish(ctx context.Context, spec port.Spec, binding port.ForwardBinding, process forwardProcessIdentity, socket forwardListenerIdentity) (raw []byte, err error) {
	err = s.transaction(ctx, binding.Attempt+".json", func() error {
		var publishErr error
		raw, publishErr = s.publishLocked(spec, binding, process, socket)
		return publishErr
	})
	return raw, err
}

func publishForwardStatus(ctx context.Context, directory string, spec port.Spec, binding port.ForwardBinding, process forwardProcessIdentity, socket forwardListenerIdentity, uid, gid int) ([]byte, error) {
	return (forwardStatusStore{directory: directory, uid: uid, gid: gid}).publish(ctx, spec, binding, process, socket)
}

func readForwardStatus(ctx context.Context, directory, name string, uid, gid int) (raw []byte, status forwardStatus, err error) {
	store := forwardStatusStore{directory: directory, uid: uid, gid: gid}
	err = store.transaction(ctx, name, func() error {
		var readErr error
		raw, status, readErr = readForwardStatusLocked(directory, name, uid, gid)
		return readErr
	})
	return raw, status, err
}

func removeForwardStatus(ctx context.Context, directory, name string, expected []byte, uid, gid int) error {
	store := forwardStatusStore{directory: directory, uid: uid, gid: gid}
	return store.transaction(ctx, name, func() error { return store.removeLocked(name, expected) })
}
