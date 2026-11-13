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

// A preview fills the requested width; its height follows the aspect ratio.
// The PNG has the pixels of that area, never more than the source's, so a
// small image is scaled up by the terminal, not resampled here.
func TestPrepareFillsTheWidthKeepingTheAspect(t *testing.T) {
	red := color.NRGBA{255, 0, 0, 255}
	for name, c := range map[string]struct {
		w, h, cellW, cellH, cols int
		wantW, wantH             int
		wantCols, wantRows       int
	}{
		"wide screenshot":         {1750, 200, 10, 20, 120, 1200, 137, 120, 7},
		"retina cells":            {1750, 200, 20, 40, 120, 1750, 200, 120, 7},
		"tall, height follows":    {300, 900, 10, 20, 60, 300, 900, 60, 90},
		"small, not resampled up": {30, 20, 10, 20, 80, 30, 20, 80, 27},
		"wider than the cells":    {4000, 100, 10, 20, 300, 2560, 64, 256, 4},
		"taller than the cells":   {100, 4000, 10, 20, 100, 100, 4000, 13, 256},
	} {
		p, err := Prepare(pngOf(t, c.w, c.h, red), c.cellW, c.cellH, c.cols)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(p.PNG))
		if err != nil || cfg.Width != c.wantW || cfg.Height != c.wantH {
			t.Errorf("%s: %dx%d, %v; want %dx%d", name, cfg.Width, cfg.Height, err, c.wantW, c.wantH)
		}
		if p.Cols != c.wantCols || p.Rows != c.wantRows {
			t.Errorf("%s: %d×%d cells, want %d×%d", name, p.Cols, p.Rows, c.wantCols, c.wantRows)
		}
	}
}

// A picture whose PNG would exceed MaxPreviewBytes at the area's pixels is
// sent with fewer pixels in the same cells, instead of being refused.
func TestPrepareShrinksPixelsToTheByteBound(t *testing.T) {
	const w, h = 2400, 1600
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223 // noise does not compress
		img.Pix[i] = uint8(seed >> 24)
	}
	var src bytes.Buffer
	if err := png.Encode(&src, img); err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(src.Bytes(), 10, 20, 240)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := png.DecodeConfig(bytes.NewReader(p.PNG))
	if len(p.PNG) > MaxPreviewBytes || cfg.Width >= w || p.Cols != 240 || p.Rows != 80 {
		t.Fatalf("%d bytes, %dx%d pixels on %d×%d cells", len(p.PNG), cfg.Width, cfg.Height, p.Cols, p.Rows)
	}
}

func TestPrepareRefusesBadInputBeforeDecoding(t *testing.T) {
	if _, err := Prepare([]byte("GIF89a not a png"), 10, 20, 60); !errors.Is(err, ErrNotPNG) {
		t.Fatalf("non-PNG: %v", err)
	}
	// A header that claims 5000×5000 pixels is refused from its config.
	small := pngOf(t, 1, 1, color.NRGBA{A: 255})
	ihdr := small[8+8 : 8+8+13]
	binary.BigEndian.PutUint32(ihdr[0:4], 5000)
	binary.BigEndian.PutUint32(ihdr[4:8], 5000)
	binary.BigEndian.PutUint32(small[8+8+13:], crc32.ChecksumIEEE(small[8+4:8+8+13]))
	if _, err := Prepare(small, 10, 20, 60); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized header: %v", err)
	}
	if _, err := Prepare(pngOf(t, 2, 2, color.NRGBA{A: 255}), 0, 20, 60); err == nil {
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
	p, err := Prepare(b.Bytes(), 1, 1, 1)
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
