package vmshare

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	clientv1 "github.com/pkg/sftp"
	fx "github.com/pkg/sftp/v2/encoding/ssh/filexfer"
)

func TestV2LargeTransferExactBytes(t *testing.T) {
	c, root := client(t, true)
	want := make([]byte, 4<<20)
	_, _ = rand.New(rand.NewSource(42)).Read(want)
	f, err := c.Create("/large")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if n, err := f.ReadFrom(bytes.NewReader(want)); err != nil || n != int64(len(want)) {
		t.Fatalf("upload: %d %v", n, err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "large"))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("host file differs: %v", err)
	}
	r, err := c.Open("/large")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err = io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("download differs: %v", err)
	}
}

func TestV2HandleTimesAfterRename(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	b, _ := rawServer(t, s)
	wirePacket(t, b, 1, wireUint(3))
	p := append(wireUint(1), wireString("/before")...)
	p = append(p, wireUint(2|8|32)...)
	p = append(p, wireUint(0)...)
	reply := wirePacket(t, b, 3, p)
	if reply[0] != 102 {
		t.Fatalf("open: %x", reply)
	}
	h := string(reply[9 : 9+binary.BigEndian.Uint32(reply[5:9])])
	if e = s.root.Rename("before", "after"); e != nil {
		t.Fatal(e)
	}
	if e = s.root.WriteFile("before", nil, 0644); e != nil {
		t.Fatal(e)
	}
	p = append(wireUint(2), wireString(h)...)
	p = append(p, wireUint(8)...)
	p = append(p, wireUint(123456)...)
	p = append(p, wireUint(123457)...)
	reply = wirePacket(t, b, 10, p)
	if reply[0] != 101 || binary.BigEndian.Uint32(reply[5:9]) != 0 {
		t.Fatalf("fsetstat: %x", reply)
	}
	st, e := s.root.Stat("after")
	if e != nil || st.ModTime().Unix() != 123457 {
		t.Fatalf("wrong original mtime: %v %v", st, e)
	}
	st, e = s.root.Stat("before")
	if e != nil || st.ModTime().Unix() == 123457 {
		t.Fatalf("changed replacement mtime: %v %v", st, e)
	}
}

func TestV2ConcurrentConnections(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, b := net.Pipe()
			defer func() { _ = a.Close() }()
			defer func() { _ = b.Close() }()
			done := make(chan error, 1)
			go func() { done <- s.Serve(a) }()
			c, e := clientv1.NewClientPipe(b, b)
			if e != nil {
				errs <- e
				return
			}
			name := fmt.Sprintf("/%d", i)
			f, e := c.Create(name)
			if e != nil {
				errs <- e
				return
			}
			data := strings.Repeat(fmt.Sprintf("%d", i), 40000)
			_, e = io.WriteString(f, data)
			if e == nil {
				e = f.Sync()
			}
			if e != nil {
				errs <- e
			}
			_ = f.Close()
			_ = c.Close()
			<-done
			actual, e := s.root.ReadFile(name[1:])
			if e != nil || string(actual) != data {
				errs <- fmt.Errorf("connection %d: bad content: %v", i, e)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestV2MalformedPacketCannotMutateShare(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	b, _ := rawServer(t, s)
	wirePacket(t, b, 1, wireUint(3))
	// Missing mode in a MKDIR with permission flag: reject without creating it.
	p := append(wireUint(1), wireString("/malformed")...)
	p = append(p, wireUint(4)...)
	wire := append(wireUint(uint32(len(p)+1)), byte(14))
	wire = append(wire, p...)
	if _, e = b.Write(wire); e != nil {
		t.Fatal(e)
	}
	var hdr [4]byte
	if _, e = io.ReadFull(b, hdr[:]); e == nil {
		reply := make([]byte, binary.BigEndian.Uint32(hdr[:]))
		if _, e = io.ReadFull(b, reply); e != nil {
			t.Fatal(e)
		}
		if reply[0] != 101 || binary.BigEndian.Uint32(reply[5:9]) == 0 {
			t.Fatalf("accepted malformed mkdir: %x", reply)
		}
	} else if e != io.EOF {
		t.Fatal(e)
	}
	_ = b.Close()
	if _, e = s.root.Stat("malformed"); !os.IsNotExist(e) {
		t.Fatalf("malformed request mutated share: %v", e)
	}
	b, _ = rawServer(t, s)
	wirePacket(t, b, 1, wireUint(3))
	p = append(wireUint(2), wireString("/valid")...)
	p = append(p, wireUint(4)...)
	p = append(p, wireUint(0700)...)
	reply := wirePacket(t, b, 14, p)
	if reply[0] != 101 || binary.BigEndian.Uint32(reply[5:9]) != 0 {
		t.Fatalf("valid request after malformed: %x", reply)
	}
}

func TestV2PacketLimitClosesConnection(t *testing.T) {
	s, e := Open(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	b, done := rawServer(t, s)
	wirePacket(t, b, 1, wireUint(3))
	if _, e = b.Write(wireUint(16 * 1024 * 1024)); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("no oversize error")
		}
	case <-time.After(time.Second):
		t.Fatal("unbounded packet read")
	}
}

func TestV2ForeignOwnershipRejected(t *testing.T) {
	c, root := client(t, true)
	f, e := c.Create("/file")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = f.Close() }()
	if e = f.Chown(os.Getuid()+1, os.Getgid()); e == nil {
		t.Fatal("allowed foreign uid")
	}
	if e = c.Chown("/file", os.Getuid(), os.Getgid()+1); e == nil {
		t.Fatal("allowed foreign gid")
	}
	st, e := os.Stat(filepath.Join(root, "file"))
	if e != nil || st.Size() != 0 {
		t.Fatalf("file after denied chown: %v %v", st, e)
	}
}

func TestV2AllReadOnlyPathsRejectMutation(t *testing.T) {
	s, e := Open(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	if e = s.root.WriteFile("file", []byte("stay"), 0644); e != nil {
		t.Fatal(e)
	}
	for _, flags := range []uint32{fx.FlagRead | fx.FlagTruncate, fx.FlagRead | fx.FlagCreate, fx.FlagRead | fx.FlagAppend, fx.FlagWrite, fx.FlagWrite | fx.FlagExclusive} {
		if f, e := s.Open(context.Background(), &fx.OpenPacket{Filename: "/file", PFlags: flags}); e == nil {
			_ = f.Close()
			t.Errorf("allowed flags %d", flags)
		}
	}
}
