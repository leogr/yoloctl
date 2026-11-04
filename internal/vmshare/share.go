// Package vmshare serves one host grant over a private VM connection.
// Descriptor-relative operations retain the grant boundary; native account
// state is never shared here.
package vmshare

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	sftp "github.com/pkg/sftp/v2"
	fx "github.com/pkg/sftp/v2/encoding/ssh/filexfer"
	"github.com/pkg/sftp/v2/encoding/ssh/filexfer/openssh"
	"golang.org/x/sys/unix"
)

type Share struct {
	sftp.UnimplementedServerHandler
	root     *os.Root
	writable bool
	next     atomic.Uint64
	syncDir  func(*os.File) error
}

func Open(dir string, writable bool) (*Share, error) {
	r, e := os.OpenRoot(dir)
	if e != nil {
		return nil, e
	}
	return &Share{root: r, writable: writable, syncDir: (*os.File).Sync}, nil
}
func (s *Share) Close() error { return s.root.Close() }
func (s *Share) Serve(c io.ReadWriteCloser) error {
	srv := &sftp.Server{Handler: s, Debug: io.Discard}
	return srv.Serve(c)
}
func relative(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}

type fileHandle struct {
	*os.File
	handle               string
	appendOnly, writable bool
	pending              *fx.NameEntry
}

func (f *fileHandle) Handle() string { return f.handle }
func (f *fileHandle) WriteAt(p []byte, off int64) (int, error) {
	if f.appendOnly {
		return f.Write(p)
	}
	return f.File.WriteAt(p, off)
}
func (f *fileHandle) Stat() (*fx.Attributes, error) {
	st, e := f.File.Stat()
	if e != nil {
		return nil, e
	}
	return attributes(st), nil
}
func attributes(st os.FileInfo) *fx.Attributes {
	a := &fx.Attributes{}
	a.SetSize(uint64(st.Size()))
	a.SetPermissions(fx.FromGoFileMode(st.Mode()))
	a.SetACModTime(accessTime(st), uint32(st.ModTime().Unix()))
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		a.SetUserGroup(s.Uid, s.Gid)
	}
	return a
}
func (s *Share) newHandle(f *os.File, appendOnly bool) *fileHandle {
	return &fileHandle{File: f, handle: strconv.FormatUint(s.next.Add(1), 10), appendOnly: appendOnly, writable: s.writable}
}
func (s *Share) Open(_ context.Context, r *fx.OpenPacket) (sftp.FileHandler, error) {
	p := r.PFlags
	flags := os.O_RDONLY
	if p&(fx.FlagWrite|fx.FlagCreate|fx.FlagTruncate|fx.FlagAppend|fx.FlagExclusive) != 0 {
		if !s.writable {
			return nil, os.ErrPermission
		}
		flags = os.O_WRONLY
		if p&fx.FlagRead != 0 {
			flags = os.O_RDWR
		}
		if p&fx.FlagCreate != 0 {
			flags |= os.O_CREATE
		}
		if p&fx.FlagExclusive != 0 {
			flags |= os.O_EXCL
		}
		if p&fx.FlagAppend != 0 {
			flags |= os.O_APPEND
		}
	}
	perm := os.FileMode(0644)
	if r.Attrs.HasPermissions() {
		perm = os.FileMode(r.Attrs.Permissions.Perm()) & 0777
	}
	f, e := s.openFile(r.Filename, flags|syscall.O_NONBLOCK, perm)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e == nil && !st.Mode().IsRegular() {
		e = os.ErrInvalid
	}
	if e == nil && p&fx.FlagTruncate != 0 {
		e = f.Truncate(0)
	}
	if e != nil {
		_ = f.Close()
		return nil, e
	}
	return s.newHandle(f, p&fx.FlagAppend != 0), nil
}
func (s *Share) OpenDir(_ context.Context, r *fx.OpenDirPacket) (sftp.DirHandler, error) {
	f, e := s.root.OpenFile(relative(r.Path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e == nil && !st.IsDir() {
		e = os.ErrInvalid
	}
	if e != nil {
		_ = f.Close()
		return nil, e
	}
	return s.newHandle(f, false), nil
}
func (f *fileHandle) ReadDir(limit uint32) ([]*fx.NameEntry, error) {
	var out []*fx.NameEntry
	size := 0
	for {
		ent := f.pending
		f.pending = nil
		if ent == nil {
			infos, e := f.Readdir(1)
			if len(infos) == 0 {
				if len(out) > 0 && errors.Is(e, io.EOF) {
					return out, nil
				}
				return out, e
			}
			st := infos[0]
			ent = &fx.NameEntry{Filename: st.Name(), Attrs: *attributes(st)}
		}
		n := ent.MarshalSize()
		if len(out) > 0 && size+n > int(limit) {
			f.pending = ent
			return out, nil
		}
		out = append(out, ent)
		size += n
	}
}
func (f *fileHandle) SetStat(a *fx.Attributes) error {
	if !f.writable {
		return os.ErrPermission
	}
	if err := validateSetStat(a); err != nil {
		return err
	}
	if a.HasSize() {
		if e := f.Truncate(int64(a.Size)); e != nil {
			return e
		}
	}
	if a.HasPermissions() {
		if e := f.Chmod(os.FileMode(a.Permissions.Perm()) & 0777); e != nil {
			return e
		}
	}
	if a.HasACModTime() {
		return syscall.Futimes(int(f.Fd()), []syscall.Timeval{syscall.NsecToTimeval(time.Unix(int64(a.ATime), 0).UnixNano()), syscall.NsecToTimeval(time.Unix(int64(a.MTime), 0).UnixNano())})
	}
	return nil
}

func validateSetStat(a *fx.Attributes) error {
	if a.HasExtended() {
		return fx.StatusOpUnsupported
	}
	if a.HasUserGroup() && (a.UID != uint32(os.Getuid()) || a.GID != uint32(os.Getgid())) {
		return os.ErrPermission
	}
	if a.HasSize() && a.Size > math.MaxInt64 {
		return os.ErrInvalid
	}
	return nil
}
func (s *Share) SetStat(_ context.Context, r *fx.SetStatPacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	a := &r.Attrs
	if err := validateSetStat(a); err != nil {
		return err
	}
	name := relative(r.Path)
	st, e := s.root.Stat(name)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() && !st.IsDir() {
		return os.ErrPermission
	}
	if a.HasSize() {
		f, err := s.root.OpenFile(name, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return os.ErrPermission
		}
		if err := f.Truncate(int64(a.Size)); err != nil {
			return err
		}
	}
	// Path metadata operations do not require data-read access. They retain
	// os.Root's boundary; FSETSTAT above uses only its original open descriptor.
	if a.HasPermissions() {
		if err := s.root.Chmod(name, os.FileMode(a.Permissions.Perm())&0777); err != nil {
			return err
		}
	}
	if a.HasACModTime() {
		return s.root.Chtimes(name, time.Unix(int64(a.ATime), 0), time.Unix(int64(a.MTime), 0))
	}
	return nil
}
func (s *Share) Stat(_ context.Context, r *fx.StatPacket) (*fx.Attributes, error) {
	st, e := s.root.Stat(relative(r.Path))
	if e != nil {
		return nil, e
	}
	return attributes(st), nil
}
func (s *Share) LStat(_ context.Context, r *fx.LStatPacket) (*fx.Attributes, error) {
	st, e := s.root.Lstat(relative(r.Path))
	if e != nil {
		return nil, e
	}
	return attributes(st), nil
}
func (s *Share) RealPath(_ context.Context, r *fx.RealPathPacket) (string, error) {
	return path.Clean("/" + r.Path), nil
}
func (s *Share) ReadLink(_ context.Context, r *fx.ReadLinkPacket) (string, error) {
	return s.root.Readlink(relative(r.Path))
}
func (s *Share) Mkdir(_ context.Context, r *fx.MkdirPacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	perm := os.FileMode(0755)
	if r.Attrs.HasPermissions() {
		perm = os.FileMode(r.Attrs.Permissions.Perm()) & 0777
	}
	return s.changeName(r.Path, func(fd int, name string) error { return unix.Mkdirat(fd, name, uint32(perm)) })
}
func (s *Share) Remove(_ context.Context, r *fx.RemovePacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	return s.changeName(r.Path, func(fd int, name string) error { return unix.Unlinkat(fd, name, 0) })
}
func (s *Share) Rmdir(_ context.Context, r *fx.RmdirPacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	return s.changeName(r.Path, func(fd int, name string) error { return unix.Unlinkat(fd, name, unix.AT_REMOVEDIR) })
}
func (s *Share) Rename(_ context.Context, r *fx.RenamePacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	return s.changeNames(r.OldPath, r.NewPath, unix.Renameat)
}
func (s *Share) POSIXRename(_ context.Context, r *openssh.POSIXRenameExtendedPacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	return s.changeNames(r.OldPath, r.NewPath, unix.Renameat)
}
func (s *Share) Hardlink(_ context.Context, r *openssh.HardlinkExtendedPacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	return s.changeNames(r.OldPath, r.NewPath, func(a int, old string, b int, next string) error { return unix.Linkat(a, old, b, next, 0) })
}
func (s *Share) Symlink(_ context.Context, r *fx.SymlinkPacket) error {
	if !s.writable {
		return os.ErrPermission
	}
	target := path.Clean(path.Join(path.Dir(relative(r.LinkPath)), r.TargetPath))
	if path.IsAbs(r.TargetPath) || target == ".." || strings.HasPrefix(target, "../") {
		return os.ErrPermission
	}
	return s.changeName(r.LinkPath, func(fd int, name string) error { return unix.Symlinkat(r.TargetPath, fd, name) })
}
