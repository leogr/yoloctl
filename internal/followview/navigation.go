package followview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leogr/yoloctl/internal/filelink"
	"golang.org/x/sys/unix"
)

// openBounded pins every directory descriptor and refuses symlink components.
// Only the system-owned macOS /tmp alias is normalized, before containment.
func openBounded(path, root string) (*os.File, error) {
	normalize := func(s string) string {
		if runtime.GOOS == "darwin" && (s == "/tmp" || strings.HasPrefix(s, "/tmp/")) {
			return "/private" + s
		}
		return s
	}
	path, root = normalize(path), normalize(root)
	if !filelink.CleanAbsolute(path) || !filelink.CleanAbsolute(root) || !Within(path, root) || path == root {
		return nil, errors.New("file is outside its navigation root")
	}
	fd, err := unix.Open("/", navigationDirectoryFlags(), 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags = navigationDirectoryFlags()
		}
		next, e := unix.Openat(fd, p, flags, 0)
		_ = unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = errors.New("select a readable regular file")
		}
		return nil, err
	}
	return f, nil
}
func HostProbe(ctx context.Context, targets []filelink.Target) map[filelink.Target]error {
	out := map[filelink.Target]error{}
	for _, t := range targets {
		if len(out) >= filelink.MaxTargets {
			break
		}
		err := ctx.Err()
		if err == nil {
			if t.Mode != "host" || t.Space != "" {
				err = errors.New("not a host target")
			} else {
				var f *os.File
				f, err = openBounded(t.Path, t.Root)
				if f != nil {
					_ = f.Close()
				}
			}
		}
		out[t] = err
	}
	return out
}
func (r VMReader) Probe(ctx context.Context, targets []filelink.Target) map[filelink.Target]error {
	out := map[filelink.Target]error{}
	if len(targets) == 0 {
		return out
	}
	paths := make([]string, 0, len(targets))
	root := targets[0].Root
	for _, t := range targets {
		if len(paths) >= filelink.MaxTargets {
			break
		}
		if t.Mode != "vm" || t.Root != root || !Within(t.Path, root) {
			out[t] = errors.New("invalid VM navigation target")
		} else {
			paths = append(paths, t.Path)
		}
	}
	if len(paths) == 0 {
		return out
	}
	response, err := r.call(ctx, guestRequest{Op: "probe", Path: pathsFirst(paths, root), Paths: paths, NavigationRoot: root})
	for _, t := range targets {
		if _, done := out[t]; done {
			continue
		}
		e := err
		if e == nil {
			v, ok := response.Probes[t.Path]
			if !ok {
				e = errors.New("readability was not measured")
			} else if v != "" {
				e = errors.New(v)
			}
		}
		out[t] = e
	}
	return out
}
func pathsFirst(paths []string, fallback string) string {
	if len(paths) > 0 {
		return paths[0]
	}
	return fallback
}
