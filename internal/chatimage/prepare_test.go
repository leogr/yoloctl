package chatimage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func pngOf(t *testing.T, w, h int, fill color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = fill.R, fill.G, fill.B, fill.A
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPrepareFitsThePixelAndCellBudgets(t *testing.T) {
	red := color.NRGBA{255, 0, 0, 255}
	for name, c := range map[string]struct {
		w, h, cellW, cellH, cols, rows int
		wantW, wantH                   int
	}{
		"wide, pixel bound":   {1600, 400, 10, 20, 60, 12, 512, 128},
		"tall, cell bound":    {300, 900, 10, 20, 60, 12, 80, 240},
		"small, not enlarged": {30, 20, 10, 20, 60, 12, 30, 20},
		"huge cells, 512 cap": {2000, 2000, 40, 80, 60, 12, 512, 512},
	} {
		p, err := Prepare(pngOf(t, c.w, c.h, red), c.cellW, c.cellH, c.cols, c.rows)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(p.PNG))
		if err != nil || cfg.Width != c.wantW || cfg.Height != c.wantH {
			t.Errorf("%s: %dx%d, %v; want %dx%d", name, cfg.Width, cfg.Height, err, c.wantW, c.wantH)
		}
		if p.Cols != (c.wantW+c.cellW-1)/c.cellW || p.Rows != (c.wantH+c.cellH-1)/c.cellH || p.Cols > c.cols || p.Rows > c.rows {
			t.Errorf("%s: %d×%d cells", name, p.Cols, p.Rows)
		}
		if len(p.PNG) > MaxPreviewBytes {
			t.Errorf("%s: %d bytes", name, len(p.PNG))
		}
	}
}

func TestPrepareRefusesBadInputBeforeDecoding(t *testing.T) {
	if _, err := Prepare([]byte("GIF89a not a png"), 10, 20, 60, 12); !errors.Is(err, ErrNotPNG) {
		t.Fatalf("non-PNG: %v", err)
	}
	// A header that claims 5000×5000 pixels is refused from its config.
	small := pngOf(t, 1, 1, color.NRGBA{A: 255})
	ihdr := small[8+8 : 8+8+13]
	binary.BigEndian.PutUint32(ihdr[0:4], 5000)
	binary.BigEndian.PutUint32(ihdr[4:8], 5000)
	binary.BigEndian.PutUint32(small[8+8+13:], crc32.ChecksumIEEE(small[8+4:8+8+13]))
	if _, err := Prepare(small, 10, 20, 60, 12); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized header: %v", err)
	}
	if _, err := Prepare(pngOf(t, 2, 2, color.NRGBA{A: 255}), 0, 20, 60, 12); err == nil {
		t.Fatal("unknown cell size accepted")
	}
}

func TestPrepareAveragesAlphaWithoutDarkening(t *testing.T) {
	// Half transparent black, half opaque white, averaged to one pixel: the
	// colour stays white, alpha halves.
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Pix = []uint8{0, 0, 0, 0, 255, 255, 255, 255}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	p, err := Prepare(b.Bytes(), 1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := png.Decode(bytes.NewReader(p.PNG))
	c := color.NRGBAModel.Convert(out.At(0, 0)).(color.NRGBA)
	if c.R != 255 || c.G != 255 || c.B != 255 || c.A < 126 || c.A > 128 {
		t.Fatalf("averaged pixel %+v", c)
	}
}

func TestTransmitAndDeleteSequences(t *testing.T) {
	p := Prepared{PNG: bytes.Repeat([]byte{7}, 5000), Cols: 3, Rows: 2} // > one 4096-char chunk
	seq := TransmitSequence(0x010203, p)
	chunks := strings.Split(strings.TrimSuffix(seq, "\x1b\\"), "\x1b\\")
	if len(chunks) < 2 {
		t.Fatalf("%d chunks", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "\x1b_Ga=T,f=100,t=d,q=2,U=1,i=66051,c=3,r=2,m=1;") {
		t.Fatalf("first chunk %q", chunks[0][:60])
	}
	for _, c := range chunks[1 : len(chunks)-1] {
		if !strings.HasPrefix(c, "\x1b_Gm=1;") {
			t.Fatalf("middle chunk %q", c[:12])
		}
	}
	if !strings.HasPrefix(chunks[len(chunks)-1], "\x1b_Gm=0;") {
		t.Fatalf("last chunk %q", chunks[len(chunks)-1][:12])
	}
	if got := DeleteSequence(9); got != "\x1b_Ga=d,d=I,q=2,i=9\x1b\\" {
		t.Fatalf("delete %q", got)
	}
}
