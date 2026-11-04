package vmshare

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func wireUint(n uint32) []byte   { return binary.BigEndian.AppendUint32(nil, n) }
func wireString(s string) []byte { return append(wireUint(uint32(len(s))), []byte(s)...) }
func wirePacket(t *testing.T, conn net.Conn, kind byte, data []byte) []byte {
	t.Helper()
	wire := append(wireUint(uint32(len(data)+1)), kind)
	wire = append(wire, data...)
	if _, err := conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, binary.BigEndian.Uint32(size[:]))
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func TestSFTPCreateModes(t *testing.T) {
	dir := t.TempDir()
	share, err := Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = share.Close() }()
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	go func() { _ = share.Serve(a) }()
	if reply := wirePacket(t, b, 1, wireUint(3)); reply[0] != 2 {
		t.Fatalf("init: %x", reply)
	}
	// Exact SFTP OPEN/MKDIR fields sent by SSHFS 3.7.3: permissions are in
	// the request itself, without a later chmod operation.
	for _, tc := range []struct {
		name string
		kind byte
		mode uint32
	}{{"private-file", 3, 0600}, {"private-dir", 14, 0700}} {
		data := append(wireUint(1), wireString("/"+tc.name)...)
		if tc.kind == 3 {
			data = append(data, wireUint(2|8|32)...)
		}
		data = append(data, wireUint(4)...)
		data = append(data, wireUint(tc.mode)...)
		reply := wirePacket(t, b, tc.kind, data)
		if tc.kind == 3 && reply[0] != 102 || tc.kind == 14 && (reply[0] != 101 || binary.BigEndian.Uint32(reply[5:]) != 0) {
			t.Fatalf("request failed: %x", reply)
		}
		st, err := os.Stat(filepath.Join(dir, tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != os.FileMode(tc.mode) {
			t.Errorf("%s requested %04o, host stored %04o", tc.name, tc.mode, st.Mode().Perm())
		}
	}
}

func TestSFTPOpenHandleSurvivesRename(t *testing.T) {
	c, dir := client(t, true)
	f, err := c.Create("/before")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Write([]byte("original")); err != nil {
		t.Fatal(err)
	}
	if err = c.Rename("/before", "/after"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "before"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(2); err != nil {
		t.Fatal(err)
	}
	replacement, _ := os.ReadFile(filepath.Join(dir, "before"))
	original, _ := os.ReadFile(filepath.Join(dir, "after"))
	if string(replacement) != "replacement" || string(original) != "or" {
		t.Errorf("ftruncate followed old path: original=%q replacement=%q", original, replacement)
	}
}

func TestSFTPSyncSupported(t *testing.T) {
	c, _ := client(t, true)
	f, err := c.Create("/durable")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Write([]byte("durable")); err != nil {
		t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		t.Fatalf("host share cannot fsync: %v", err)
	}
}
