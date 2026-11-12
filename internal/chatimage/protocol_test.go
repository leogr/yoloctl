package chatimage

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

const (
	idA  = "0123456789abcdef0123456789abcdef"
	uuid = "01234567-89ab-cdef-0123-456789abcdef"
	hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func preview() Frame {
	return Frame{Op: OpPreview, Generation: 1, Request: 7, RoomID: idA, DiskID: idA, HumanID: idA, ChannelID: idA,
		Channel: "#space", MessageID: uuid, Artifact: uuid, Bytes: 1024, SHA256: hash, Chip: "[file a.png · 1 KiB]"}
}

func pair(t *testing.T) (host, child *Conn, hostFile, childFile *os.File) {
	t.Helper()
	h, c, err := ChannelPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close(); _ = c.Close() })
	return NewConn(h, true), NewConn(c, false), h, c
}

func TestControlFramesRoundTripInTheirDirection(t *testing.T) {
	host, child, _, _ := pair(t)
	for _, f := range []Frame{
		{Op: OpCapability, Generation: 3, Supported: true, MaxCols: 60, MaxRows: 12},
		{Op: OpCapability, Generation: 4},
		{Op: OpReady, Generation: 3, Request: 7, Token: "tok_1-A", ID: 0x010203, Cols: 20, Rows: 6},
		{Op: OpError, Generation: 3, Request: 7, Code: "busy"},
	} {
		if err := host.Send(f); err != nil {
			t.Fatalf("%s: %v", f.Op, err)
		}
		got, err := child.Receive()
		f.V = ProtocolVersion
		if err != nil || got != f {
			t.Fatalf("%s: got %+v, %v", f.Op, got, err)
		}
	}
	for _, f := range []Frame{preview(), {Op: OpRelease, Token: "tok_1-A"}} {
		if err := child.Send(f); err != nil {
			t.Fatalf("%s: %v", f.Op, err)
		}
		got, err := host.Receive()
		f.V = ProtocolVersion
		if err != nil || got != f {
			t.Fatalf("%s: got %+v, %v", f.Op, got, err)
		}
	}
	// Neither end may send the other's operations.
	if host.Send(preview()) == nil || child.Send(Frame{Op: OpError, Generation: 1, Request: 1, Code: "busy"}) == nil {
		t.Fatal("wrong direction sent")
	}
}

func TestControlFrameValidation(t *testing.T) {
	bad := map[string]Frame{
		"unknown op":         {V: 1, Op: "fetch"},
		"version":            {V: 2, Op: OpRelease, Token: "t"},
		"capability no gen":  {V: 1, Op: OpCapability, Supported: true, MaxCols: 1, MaxRows: 1},
		"capability size":    {V: 1, Op: OpCapability, Generation: 1, Supported: true},
		"ready id too big":   {V: 1, Op: OpReady, Generation: 1, Request: 1, Token: "t", ID: 1 << 24, Cols: 1, Rows: 1},
		"ready bad token":    {V: 1, Op: OpReady, Generation: 1, Request: 1, Token: "a/b", ID: 1, Cols: 1, Rows: 1},
		"error code":         {V: 1, Op: OpError, Generation: 1, Request: 1, Code: "exploded"},
		"release extra":      {V: 1, Op: OpRelease, Token: "t", Code: "busy"},
		"release long token": {V: 1, Op: OpRelease, Token: strings.Repeat("a", 65)},
	}
	mutate := func(edit func(*Frame)) Frame { f := preview(); f.V = 1; edit(&f); return f }
	bad["preview room"] = mutate(func(f *Frame) { f.RoomID = "room" })
	bad["preview channel"] = mutate(func(f *Frame) { f.Channel = "space" })
	bad["preview message"] = mutate(func(f *Frame) { f.MessageID = "m:abc" })
	bad["preview hash"] = mutate(func(f *Frame) { f.SHA256 = strings.ToUpper(hash) })
	bad["preview too big"] = mutate(func(f *Frame) { f.Bytes = MaxArtifactBytes + 1 })
	bad["preview empty"] = mutate(func(f *Frame) { f.Bytes = 0 })
	bad["preview token"] = mutate(func(f *Frame) { f.Token = "t" })
	bad["preview no chip"] = mutate(func(f *Frame) { f.Chip = "" })
	bad["preview chip control"] = mutate(func(f *Frame) { f.Chip = "a\x1b[31m" })
	bad["preview chip placeholder"] = mutate(func(f *Frame) { f.Chip = "a\U0010EEEE" })
	for name, f := range bad {
		if f.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestReceiveRejectsMalformedFrames(t *testing.T) {
	raw := func(payload string) []byte {
		b := make([]byte, 4, 4+len(payload))
		binary.BigEndian.PutUint32(b, uint32(len(payload)))
		return append(b, payload...)
	}
	for name, data := range map[string][]byte{
		"unknown field":   raw(`{"v":1,"op":"release","token":"t","path":"/etc"}`),
		"trailing data":   raw(`{"v":1,"op":"release","token":"t"} {}`),
		"wrong direction": raw(`{"v":1,"op":"error","generation":1,"request":1,"code":"busy"}`),
		"invalid":         raw(`{"v":1,"op":"release"}`),
		"not json":        raw(`release`),
		"zero length":     {0, 0, 0, 0},
		"oversized":       {0, 1, 0, 1},
		"truncated":       raw(`{"v":1,"op":"release","token":"t"}`)[:10],
	} {
		host, _, _, childFile := pair(t)
		if _, err := childFile.Write(data); err != nil {
			t.Fatal(err)
		}
		_ = childFile.Close()
		if _, err := host.Receive(); !errors.Is(err, ErrFraming) {
			t.Errorf("%s: %v", name, err)
		}
	}
	host, child, _, childFile := pair(t)
	_ = child
	_ = childFile.Close()
	if _, err := host.Receive(); !errors.Is(err, io.EOF) {
		t.Fatalf("clean close: %v", err)
	}
}

func TestInheritedChannelClaimsAndClearsTheVariable(t *testing.T) {
	_, c, err := ChannelPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	t.Setenv(ChannelFDEnv, "notanumber")
	if f, err := InheritedChannel(); err == nil || f != nil {
		t.Fatal("invalid descriptor accepted")
	}
	if _, ok := os.LookupEnv(ChannelFDEnv); ok {
		t.Fatal("variable left for descendants")
	}
	if f, err := InheritedChannel(); f != nil || err != nil {
		t.Fatalf("absent channel: %v %v", f, err)
	}
}
