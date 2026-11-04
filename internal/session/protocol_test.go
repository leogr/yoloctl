package session

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, frameData, []byte("hello \x1b[31mworld\x00")); err != nil {
		t.Fatal(err)
	}
	size := terminal.Geometry{Cols: 120, Rows: 32, CellWidth: 19, CellHeight: 42}
	payload, err := encodeResize(size)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte{0, 120, 0, 32, 0, 19, 0, 42}) {
		t.Fatalf("resize wire bytes = %x", payload)
	}
	if err := writeFrame(&buf, frameResize, payload); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(&buf, frameExit, encodeExit(-1)); err != nil {
		t.Fatal(err)
	}

	f, err := readFrame(&buf)
	if err != nil || f.Type != frameData || string(f.Payload) != "hello \x1b[31mworld\x00" {
		t.Fatalf("data frame = %+v, %v", f, err)
	}
	f, err = readFrame(&buf)
	if err != nil || f.Type != frameResize {
		t.Fatalf("resize frame = %+v, %v", f, err)
	}
	gotSize, err := decodeResize(f.Payload)
	if err != nil || gotSize != size {
		t.Fatalf("resize = %+v, %v", gotSize, err)
	}
	f, err = readFrame(&buf)
	if err != nil || f.Type != frameExit {
		t.Fatalf("exit frame = %+v, %v", f, err)
	}
	status, err := decodeExit(f.Payload)
	if err != nil || status != -1 {
		t.Fatalf("exit status = %d, %v", status, err)
	}

	if _, err := readFrame(&buf); err != io.EOF {
		t.Fatalf("end of stream = %v, want io.EOF", err)
	}
}

func TestWriteFrameRejectsOversizePayload(t *testing.T) {
	if err := writeFrame(io.Discard, frameData, make([]byte, maxPayload+1)); err == nil {
		t.Fatal("oversize payload accepted")
	}
}

func TestWriteDataFramesChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), maxPayload*2+100)
	var buf bytes.Buffer
	if err := writeDataFrames(&buf, payload); err != nil {
		t.Fatal(err)
	}
	var got []byte
	frames := 0
	for {
		f, err := readFrame(&buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.Type != frameData {
			t.Fatalf("frame type %q", f.Type)
		}
		got = append(got, f.Payload...)
		frames++
	}
	if frames != 3 {
		t.Fatalf("frames = %d, want 3", frames)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("reassembled payload differs")
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, frameData, []byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	trunc := bytes.NewReader(buf.Bytes()[:5]) // header + 2 of 6 payload bytes
	if _, err := readFrame(trunc); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated read = %v, want ErrUnexpectedEOF", err)
	}
}

func TestDecodeErrors(t *testing.T) {
	if _, err := decodeResize([]byte{1, 2, 3}); err == nil {
		t.Fatal("short resize accepted")
	}
	if _, err := decodeExit([]byte{1}); err == nil {
		t.Fatal("short exit accepted")
	}
}

func TestResizeRejectsMalformedGeometry(t *testing.T) {
	for _, wire := range [][]byte{
		{0, 80, 0, 24}, // old protocol cannot silently lose pixel dimensions
		{0, 0, 0, 24, 0, 0, 0, 0},
		{0, 80, 0, 24, 0, 19, 0, 0},
		{0, 80, 0, 24, 0, 0, 0, 42},
		{0, 80, 0, 24, 16, 1, 0, 42},
		{16, 0, 16, 0, 0, 0, 0, 0},
	} {
		if _, err := decodeResize(wire); err == nil {
			t.Fatalf("accepted malformed resize %x", wire)
		}
	}
	for _, size := range []terminal.Geometry{{}, {Cols: -1, Rows: 24}, {Cols: 80, Rows: 24, CellWidth: 19}} {
		if _, err := encodeResize(size); err == nil {
			t.Fatalf("accepted invalid size %+v", size)
		}
	}
}
