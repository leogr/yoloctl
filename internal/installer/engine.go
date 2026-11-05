// Package installer publishes complete verified releases behind an atomic link.
// It owns no VM lifecycle and never deletes retained bundles or failed stages.
package installer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/releasebundle"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"golang.org/x/sys/unix"
)

type Existing struct {
	Fingerprint string
	Manifest    *releasebundle.Manifest
	Unknown     bool
	Path        string
}

// Inspect never executes the existing file. A managed link must resolve to a
// verified bundle immediately beneath store/bundles; other files are unknown.
func Inspect(bin, store string) (Existing, error) {
	// Compare against the resolved store, like the resolved link target below.
	if canonical, err := CanonicalPath(store); err == nil {
		store = canonical
	}
	path := filepath.Join(bin, "yoloctl")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Existing{}, nil
	}
	if err != nil {
		return Existing{}, err
	}
	e := Existing{Path: path}
	target := path
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(path)
		if err != nil {
			return e, err
		}
		if !filepath.IsAbs(link) {
			return e, errors.New("existing yoloctl link must be absolute")
		}
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return e, fmt.Errorf("existing yoloctl link is dangling or unavailable: %w", err)
		}
		e.Fingerprint = "link:" + link + ":"
		bundles := filepath.Join(store, "bundles")
		if filepath.Dir(filepath.Dir(target)) == bundles && filepath.Base(target) == "yoloctl" {
			m, err := releasebundle.Verify(filepath.Dir(target))
			if err != nil {
				return e, fmt.Errorf("existing managed install is corrupt: %w", err)
			}
			if filepath.Base(filepath.Dir(target)) != m.Key() {
				return e, errors.New("managed install path differs from manifest")
			}
			e.Manifest = &m
		}
	} else if !info.Mode().IsRegular() {
		return e, errors.New("existing yoloctl is neither a regular file nor a valid symlink")
	}
	raw, err := safepath.ReadFile(filepath.Dir(target), filepath.Base(target), 512<<20)
	if err != nil {
		return e, err
	}
	e.Fingerprint += fmt.Sprintf("%x", sha256.Sum256(raw))
	e.Unknown = e.Manifest == nil
	return e, nil
}

type Options struct {
	Source       string
	Store        string
	BinDir       string
	Expected     Existing
	AllowUnknown bool
	// Fault is used by package tests to model interrupted durability boundaries.
	// Production callers leave it nil; there is no environment escape.
	fault func(string) error
}
type Result struct {
	Executable string
	Link       string
	Already    bool
	Retained   []string
}

type directory struct {
	fd   int
	path string
}

func (d directory) close() { _ = unix.Close(d.fd) }
func openDir(path string, create bool) (directory, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return directory{}, errors.New("install directory must be absolute and clean")
	}
	fd, err := unix.Open("/", traversalFlags(), 0)
	if err != nil {
		return directory{}, fmt.Errorf("locate root: %w", err)
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, traversalFlags(), 0)
		if errors.Is(openErr, os.ErrNotExist) && create {
			if mkdirErr := unix.Mkdirat(fd, part, 0700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return directory{}, mkdirErr
			}
			syncFD, syncErr := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if syncErr == nil {
				syncErr = unix.Fsync(syncFD)
				_ = unix.Close(syncFD)
			}
			if syncErr != nil {
				_ = unix.Close(fd)
				return directory{}, syncErr
			}
			next, openErr = unix.Openat(fd, part, traversalFlags(), 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return directory{}, fmt.Errorf("locate %s in %s: %w", part, path, openErr)
		}
		fd = next
	}
	readable, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(fd)
	if err != nil {
		return directory{}, err
	}
	return directory{fd: readable, path: path}, nil
}

func (d directory) child(name string, create bool) (directory, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return directory{}, errors.New("invalid install path component")
	}
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := unix.Mkdirat(d.fd, name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return directory{}, err
		}
		if err := unix.Fsync(d.fd); err != nil {
			return directory{}, err
		}
		fd, err = unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}

	if err != nil {
		return directory{}, err
	}
	return directory{fd: fd, path: filepath.Join(d.path, name)}, nil
}
func (d directory) write(name string, data []byte, mode uint32) error {
	fd, err := unix.Openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	if err := f.Chmod(os.FileMode(mode)); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
func (d directory) sync() error { return unix.Fsync(d.fd) }

// openLockFile opens or creates install.lock in the store; tests replace it.
var openLockFile = func(dirfd int) (int, error) {
	return unix.Openat(dirfd, "install.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
}

// lockOpenAttempts bounds the open attempts when creating install.lock
// returns ENOENT. The retry handles a possible concurrent-create ENOENT
// (two installs on a fresh store failed this way on macOS CI; the kernel
// cause is not proved). Any other error, or ENOENT on the last attempt, is
// returned.
const lockOpenAttempts = 5

func lock(ctx context.Context, d directory) (func(), error) {
	fd, err := openLockFile(d.fd)
	for attempt := 1; errors.Is(err, unix.ENOENT) && attempt < lockOpenAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		fd, err = openLockFile(d.fd)
	}
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) {
		_ = unix.Close(fd)
		return nil, errors.New("unsafe installer lock")
	}
	if err := d.sync(); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func Publish(ctx context.Context, o Options) (result Result, err error) {
	phase := "validate destination"
	defer func() {
		if err != nil {
			err = fmt.Errorf("install %s: %w", phase, err)
		}
	}()
	if err := ValidatePaths(o.Source, o.Store, o.BinDir); err != nil {
		return result, err
	}
	// Directories are opened no-follow below: resolve a symlinked prefix
	// (for example a home behind a symlink) once, as ValidatePaths does.
	if o.Store, err = CanonicalPath(o.Store); err != nil {
		return result, err
	}
	if o.BinDir, err = CanonicalPath(o.BinDir); err != nil {
		return result, err
	}
	phase = "verify source"
	m, err := releasebundle.Verify(o.Source)
	if err != nil {
		return result, err
	}
	manifestRaw, err := safepath.ReadFile(o.Source, "MANIFEST.json", 1<<20)
	if err != nil {
		return result, err
	}
	var confirmed releasebundle.Manifest
	if err := strictyaml.Decode(manifestRaw, &confirmed); err != nil {
		return result, err
	}
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(confirmed)
	if !json.Valid(manifestRaw) || string(a) != string(b) {
		return result, errors.New("source manifest changed")
	}
	phase = "open store"
	store, err := openDir(o.Store, true)
	if err != nil {
		return result, err
	}
	defer store.close()
	var st unix.Stat_t
	if err := unix.Fstat(store.fd, &st); err != nil || st.Uid != uint32(os.Getuid()) || st.Mode&0022 != 0 {
		return result, errors.New("install store is not private to this user")
	}
	phase = "lock"
	unlock, err := lock(ctx, store)
	if err != nil {
		return result, err
	}
	defer unlock()
	phase = "open bin directory"
	bins, err := openDir(o.BinDir, true)
	if err != nil {
		return result, err
	}
	defer bins.close()
	if err := unix.Fstat(bins.fd, &st); err != nil || st.Uid != uint32(os.Getuid()) || st.Mode&0022 != 0 {
		return result, errors.New("bin directory is not private to this user")
	}
	phase = "open bundles"
	bundles, err := store.child("bundles", true)
	if err != nil {
		return result, err
	}
	defer bundles.close()
	result.Executable = filepath.Join(bundles.path, m.Key(), "yoloctl")
	result.Link = filepath.Join(bins.path, "yoloctl")
	phase = "inspect selection"
	current, err := Inspect(bins.path, store.path)
	if err != nil {
		return result, err
	}
	same := current.Manifest != nil && current.Manifest.Key() == m.Key()
	if !same && current.Fingerprint != o.Expected.Fingerprint {
		return result, errors.New("installed selection changed; run install again to review it")
	}
	if current.Unknown && !o.AllowUnknown {
		return result, errors.New("unknown build conflict requires explicit terminal confirmation")
	}
	fail := func(point string) error {
		if o.fault != nil {
			return o.fault(point)
		}
		return nil
	}
	phase = "inspect bundle key"
	selected, err := bundles.child(m.Key(), false)
	if err == nil {
		selected.close()
		existing, err := releasebundle.Verify(filepath.Dir(result.Executable))
		if err != nil {
			return result, fmt.Errorf("immutable bundle key already exists with invalid bytes: %w", err)
		}
		a, _ := json.Marshal(existing)
		b, _ := json.Marshal(m)
		existingRaw, readErr := safepath.ReadFile(filepath.Dir(result.Executable), "MANIFEST.json", 1<<20)
		if readErr != nil {
			return result, readErr
		}
		if string(a) != string(b) || string(existingRaw) != string(manifestRaw) {
			return result, errors.New("immutable bundle key has different bytes")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	} else {
		stageName := ".pending-" + rand.Text()
		phase = "create stage"
		stage, err := bundles.child(stageName, true)
		if err != nil {
			return result, err
		}
		defer stage.close()
		phase = "copy payload"
		for name, sum := range m.Files {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			raw, err := safepath.ReadFile(o.Source, name, 512<<20)
			if err != nil {
				return result, err
			}
			if fmt.Sprintf("%x", sha256.Sum256(raw)) != sum {
				return result, errors.New("source bundle changed during copy")
			}
			if err := fail("copy"); err != nil {
				return result, err
			}
			mode := uint32(0444)
			if strings.HasPrefix(name, "yoloctl") {
				mode = 0555
			}
			if err := stage.write(name, raw, mode); err != nil {
				return result, fmt.Errorf("copy %s: %w", name, err)
			}
		}
		phase = "write manifest"
		if err := stage.write("MANIFEST.json", manifestRaw, 0444); err != nil {
			return result, err
		}

		if err := fail("fsync"); err != nil {
			return result, err
		}
		if err := stage.sync(); err != nil {
			return result, err
		}
		if err := fail("rename"); err != nil {
			return result, err
		}
		phase = "publish directory"
		if err := publishDirectory(bundles.fd, stageName, m.Key()); err != nil {
			return result, fmt.Errorf("publish bundle directory in %s: %w", bundles.path, err)
		}
		if err := bundles.sync(); err != nil {
			return result, &atomicfile.CommitError{Err: err}
		}
	}
	if same {
		result.Already = true
	} else {
		// Retain an unmanaged regular executable before replacing its directory
		// entry. Unknown symlinks retain their original target outside the store.
		if current.Unknown && !strings.HasPrefix(current.Fingerprint, "link:") {
			retained := ".yoloctl-unknown-" + current.Fingerprint
			if err := unix.Linkat(bins.fd, "yoloctl", bins.fd, retained, 0); err != nil {
				if !errors.Is(err, unix.EEXIST) {
					return result, err
				}
				sum, err := FileSHA(filepath.Join(bins.path, retained))
				if err != nil || sum != current.Fingerprint {
					return result, errors.New("retained unknown executable differs")
				}
			}
			if err := bins.sync(); err != nil {
				return result, err
			}
		}
		phase = "stage link"
		temp := ".yoloctl-link-" + rand.Text()
		if err := makeSymlink(result.Executable, bins.fd, temp); err != nil {
			if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
				return result, fmt.Errorf("%s does not allow symbolic links (%w); the published bundle is %s: choose a bin directory on a local disk with --bin-dir", bins.path, err, result.Executable)
			}
			return result, err
		}
		if err := fail("link"); err != nil {
			return result, err
		}
		phase = "select link"
		if err := unix.Renameat(bins.fd, temp, bins.fd, "yoloctl"); err != nil {
			return result, err
		}
		if err := fail("after-link"); err != nil {
			return result, &atomicfile.CommitError{Err: err}
		}
		if err := bins.sync(); err != nil {
			return result, &atomicfile.CommitError{Err: err}
		}
	}
	phase = "list retained"
	fd, err := unix.Dup(bundles.fd)
	if err != nil {
		return result, err
	}
	f := os.NewFile(uintptr(fd), bundles.path)
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			result.Retained = append(result.Retained, filepath.Join(bundles.path, entry.Name()))
		}
	}
	return result, nil
}

// FileSHA reports a regular-file fingerprint without executing it.
func FileSHA(path string) (string, error) {
	data, err := safepath.ReadFile(filepath.Dir(path), filepath.Base(path), 512<<20)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// Test hooks for the two publication renames and the selection link.
var (
	makeSymlink     = unix.Symlinkat
	noReplaceRename = renameNoReplace
	plainRename     = func(parent int, source, target string) error { return unix.Renameat(parent, source, parent, target) }
)

// publishDirectory publishes the complete stage under target without
// replacing what is there. The no-replace rename is always tried first, once.
// Only when the filesystem lacks that flag (many FUSE mounts: EINVAL) does it
// fall back, still under install.lock: anything at target (lookup without
// following links) is EEXIST, only ENOENT permits a plain same-directory
// rename, and any other lookup error is returned. Cooperating installers hold
// the lock, and rename(2) cannot replace a non-empty directory or a
// non-directory; unlike the flag, it would replace an empty directory that a
// foreign process creates between the lookup and the rename.
func publishDirectory(parent int, source, target string) error {
	err := noReplaceRename(parent, source, target)
	if err == nil || !noReplaceUnsupported(err) {
		return err
	}
	var st unix.Stat_t
	if lookup := unix.Fstatat(parent, target, &st, unix.AT_SYMLINK_NOFOLLOW); lookup == nil {
		return unix.EEXIST
	} else if !errors.Is(lookup, unix.ENOENT) {
		return lookup
	}
	return plainRename(parent, source, target)
}
