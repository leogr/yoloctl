package vmshare

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	clientv1 "github.com/pkg/sftp"
	sftp "github.com/pkg/sftp/v2"
	fx "github.com/pkg/sftp/v2/encoding/ssh/filexfer"
)

type trackedShare struct {
	*Share
	mu      sync.Mutex
	handles []*fileHandle
	wrap    func(sftp.FileHandler) sftp.FileHandler
}

func (s *trackedShare) Open(ctx context.Context, r *fx.OpenPacket) (sftp.FileHandler, error) {
	f, e := s.Share.Open(ctx, r)
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	s.handles = append(s.handles, f.(*fileHandle))
	s.mu.Unlock()
	if s.wrap != nil {
		return s.wrap(f), nil
	}
	return f, nil
}
func (s *trackedShare) OpenDir(ctx context.Context, r *fx.OpenDirPacket) (sftp.DirHandler, error) {
	f, e := s.Share.OpenDir(ctx, r)
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	s.handles = append(s.handles, f.(*fileHandle))
	s.mu.Unlock()
	return f, nil
}
func rawServer(t *testing.T, s sftp.ServerHandler) (net.Conn, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	_ = b.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan error, 1)
	go func() { srv := &sftp.Server{Handler: s, Debug: io.Discard}; done <- srv.Serve(a) }()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return b, done
}

func TestV2DisconnectClosesOpenHandles(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	tracked := &trackedShare{Share: s}
	b, done := rawServer(t, tracked)
	c, e := clientv1.NewClientPipe(b, b)
	if e != nil {
		t.Fatal(e)
	}
	f, e := c.Create("/open")
	if e != nil {
		t.Fatal(e)
	}
	_ = f
	// Real OPENDIR packet through a second client connection is covered below.
	_ = b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Serve failed to end")
	}
	for _, h := range tracked.handles {
		defer func() { _ = h.Close() }()
		if _, e := h.File.Stat(); !errors.Is(e, os.ErrClosed) {
			t.Errorf("retained host file descriptor after disconnect: %v", e)
		}
	}
	_ = c.Close()
}

func TestV2DisconnectClosesDirectory(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	tracked := &trackedShare{Share: s}
	b, done := rawServer(t, tracked)
	wirePacket(t, b, 1, wireUint(3))
	reply := wirePacket(t, b, 11, append(wireUint(1), wireString("/")...))
	if reply[0] != 102 {
		t.Fatalf("opendir: %x", reply)
	}
	_ = b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Serve failed to end")
	}
	for _, h := range tracked.handles {
		defer func() { _ = h.Close() }()
		if _, e := h.File.Stat(); !errors.Is(e, os.ErrClosed) {
			t.Errorf("retained directory descriptor: %v", e)
		}
	}
}

type closeProbe struct {
	net.Conn
	closed bool
}

func (c *closeProbe) Close() error { c.closed = true; return c.Conn.Close() }
func TestV2BadHandshakeClosesTransport(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	p := &closeProbe{Conn: a}
	done := make(chan error, 1)
	go func() {
		srv := &sftp.Server{Handler: &sftp.UnimplementedServerHandler{}, Debug: io.Discard}
		done <- srv.Serve(p)
	}()
	// A truncated INIT; disconnect must still release the server side.
	if _, e := b.Write([]byte{0, 0, 0, 5, 1, 0}); e != nil {
		t.Fatal(e)
	}
	_ = b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Serve failed to end")
	}
	if !p.closed {
		t.Fatal("failed handshake did not close transport")
	}
}

func TestV2MalformedAttributeCountIsBounded(t *testing.T) {
	// A modest count proves the allocation contract without risking this VM.
	var a fx.ExtendedAttributes
	e := a.UnmarshalFrom(fx.NewBuffer(wireUint(65536)))
	if e == nil {
		t.Fatal("accepted a count with no entries")
	}
	if len(a) != 0 {
		t.Fatalf("allocated %d entries from empty payload", len(a))
	}
}

type syncFailure struct {
	sftp.FileHandler
	called chan struct{}
}

func (f *syncFailure) Sync() error { close(f.called); return syscall.EIO }
func TestV2FsyncFailurePropagates(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	called := make(chan struct{})
	tracked := &trackedShare{Share: s, wrap: func(f sftp.FileHandler) sftp.FileHandler { return &syncFailure{f, called} }}
	b, _ := rawServer(t, tracked)
	c, e := clientv1.NewClientPipe(b, b)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = c.Close() }()
	f, e := c.Create("/file")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = f.Close() }()
	if e = f.Sync(); e == nil {
		t.Fatal("host fsync error was lost")
	}
	select {
	case <-called:
	default:
		t.Fatal("host Sync not called")
	}
}

func TestV2OpeningExistingFileDoesNotChmod(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	if e = s.root.WriteFile("existing", []byte("keep"), 0644); e != nil {
		t.Fatal(e)
	}
	// Establish the pre-existing mode independently of the host umask; OPEN's
	// requested 0600 below must not replace it.
	if e = s.root.Chmod("existing", 0644); e != nil {
		t.Fatal(e)
	}
	b, _ := rawServer(t, s)
	wirePacket(t, b, 1, wireUint(3))
	data := append(wireUint(1), wireString("/existing")...)
	data = append(data, wireUint(2|8)...)
	data = append(data, wireUint(4)...)
	data = append(data, wireUint(0600)...)
	reply := wirePacket(t, b, 3, data)
	if reply[0] != 102 {
		t.Fatalf("open: %x", reply)
	}
	st, e := s.root.Stat("existing")
	if e != nil || st.Mode().Perm() != 0644 {
		t.Fatalf("changed existing mode: %v %v", st, e)
	}
}

func TestV2DirectoryListingAndStatVFS(t *testing.T) {
	c, dir := client(t, true)
	for i := 0; i < 1024; i++ {
		name := filepath.Join(dir, time.Unix(int64(i), 0).Format("150405"))
		if e := os.WriteFile(name, nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	entries, e := c.ReadDir("/")
	if e != nil || len(entries) != 1024 {
		t.Fatalf("listing: %d %v", len(entries), e)
	}
	if runtime.GOOS != "linux" {
		return
	}
	st, e := c.StatVFS("/")
	if e != nil || st.Blocks == 0 {
		t.Fatalf("statvfs: %v %v", st, e)
	}
}

type blockingFile struct {
	sftp.FileHandler
	entered, release, synced chan struct{}
	once                     sync.Once
}

func (f *blockingFile) WriteAt(b []byte, off int64) (int, error) {
	f.once.Do(func() { close(f.entered) })
	<-f.release
	return f.FileHandler.WriteAt(b, off)
}
func (f *blockingFile) Sync() error { close(f.synced); return f.FileHandler.Sync() }
func TestV2FsyncWaitsForWrites(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	entered, release, synced := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	tracked := &trackedShare{Share: s, wrap: func(f sftp.FileHandler) sftp.FileHandler {
		return &blockingFile{FileHandler: f, entered: entered, release: release, synced: synced}
	}}
	b, _ := rawServer(t, tracked)
	wirePacket(t, b, 1, wireUint(3))
	open := append(wireUint(1), wireString("/ordered")...)
	open = append(open, wireUint(2|8|32)...)
	open = append(open, wireUint(0)...)
	reply := wirePacket(t, b, 3, open)
	if reply[0] != 102 {
		t.Fatalf("open: %x", reply)
	}
	h := string(reply[9 : 9+binary.BigEndian.Uint32(reply[5:9])])
	write := append(wireUint(2), wireString(h)...)
	write = binary.BigEndian.AppendUint64(write, 0)
	write = append(write, wireString("complete")...)
	fsync := append(wireUint(3), wireString("fsync@openssh.com")...)
	fsync = append(fsync, wireString(h)...)
	sent := make(chan error, 1)
	go func() {
		for _, r := range []struct {
			k byte
			p []byte
		}{{6, write}, {200, fsync}} {
			wire := append(wireUint(uint32(len(r.p)+1)), r.k)
			wire = append(wire, r.p...)
			if _, e := b.Write(wire); e != nil {
				sent <- e
				return
			}
		}
		sent <- nil
	}()
	<-entered
	select {
	case <-synced:
		t.Fatal("fsync overtook write")
	case <-time.After(20 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		var n [4]byte
		if _, e = io.ReadFull(b, n[:]); e != nil {
			t.Fatal(e)
		}
		p := make([]byte, binary.BigEndian.Uint32(n[:]))
		if _, e = io.ReadFull(b, p); e != nil {
			t.Fatal(e)
		}
		if p[0] != 101 || binary.BigEndian.Uint32(p[5:9]) != 0 {
			t.Fatalf("reply: %x", p)
		}
	}
	if e = <-sent; e != nil {
		t.Fatal(e)
	}
	data, e := s.root.ReadFile("ordered")
	if e != nil || string(data) != "complete" {
		t.Fatalf("write at fsync: %q %v", data, e)
	}
	select {
	case <-synced:
	default:
		t.Fatal("host fsync absent")
	}
}
