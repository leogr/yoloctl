package chatimage

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"
)

// Preview bounds: a preview spans at most MaxPreviewCells columns and rows,
// since every placeholder cell names its row and column with one of the
// Kitty diacritics and a ready frame carries at most that many rows. The
// re-encoded PNG is at most 2 MiB, decoded from at most 16 Mi source pixels.
const (
	MaxPreviewCells    = 256
	MaxPreviewBytes    = 2 << 20
	MaxDecodePixels    = 16 << 20
	transmitChunkBytes = 4096 // base64 characters per graphics command
)

// Errors a preparation can end with; the broker maps them to control codes.
var (
	ErrNotPNG   = errors.New("chatimage: not a PNG")
	ErrTooLarge = errors.New("chatimage: image too large")
)

// Prepared is a preview ready to transmit: a host-made PNG and the cell area
// it fills.
type Prepared struct {
	PNG        []byte
	Cols, Rows int
}

// Prepare checks the dimensions before a single full decode and fits the
// image, keeping its aspect ratio, inside at most cols×rows cells (each at
// most MaxPreviewCells) at the measured cell size and inside its own pixel
// size: whichever bound binds first, the area's width, its height or the
// image itself, sets the scale, so a small picture is never enlarged and a
// tall one never overflows the rows it was given. The cell area it fills is
// the scaled picture rounded up to whole cells. It re-encodes the image with
// Go's encoder at that scaled size, and smaller still when the PNG would
// exceed MaxPreviewBytes; the uploader's bytes never reach the terminal.
func Prepare(data []byte, cellW, cellH, cols, rows int) (Prepared, error) {
	if cellW < 1 || cellH < 1 || cols < 1 || rows < 1 {
		return Prepared{}, errors.New("chatimage: unknown cell geometry")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Prepared{}, ErrNotPNG
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxDecodePixels/cfg.Height {
		return Prepared{}, ErrTooLarge
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return Prepared{}, ErrNotPNG
	}
	tw, th := fit(cfg.Width, cfg.Height, min(cols, MaxPreviewCells)*cellW, min(rows, MaxPreviewCells)*cellH)
	cols, rows = (tw+cellW-1)/cellW, (th+cellH-1)/cellH
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	for {
		var buf bytes.Buffer
		if err := png.Encode(&buf, downscale(rgba, tw, th)); err != nil {
			return Prepared{}, err
		}
		if buf.Len() <= MaxPreviewBytes {
			return Prepared{PNG: buf.Bytes(), Cols: cols, Rows: rows}, nil
		}
		if tw == 1 && th == 1 {
			return Prepared{}, ErrTooLarge
		}
		// Fewer pixels in proportion to the excess, at least a tenth fewer.
		f := math.Sqrt(float64(MaxPreviewBytes)/float64(buf.Len())) * 0.9
		tw, th = max(1, int(float64(tw)*f)), max(1, int(float64(th)*f))
	}
}

// fit scales a w×h picture, keeping its aspect ratio, to the largest size
// within boxW×boxH and within w×h itself (never enlarged): the binding bound
// is whichever leaves the smaller scale. Both results are at least 1.
func fit(w, h, boxW, boxH int) (int, int) {
	tw, th := w, h
	// Compare w/boxW with h/boxH without division: the larger ratio binds.
	if tw > boxW || th > boxH {
		if int64(w)*int64(boxH) >= int64(h)*int64(boxW) {
			tw, th = boxW, int(int64(h)*int64(boxW)/int64(w))
		} else {
			tw, th = int(int64(w)*int64(boxH)/int64(h)), boxH
		}
	}
	return max(tw, 1), max(th, 1)
}

// downscale averages each target pixel's source area (premultiplied), so
// alpha edges do not darken. tw ≤ width and th ≤ height.
func downscale(rgba *image.RGBA, tw, th int) *image.NRGBA {
	w, h := rgba.Rect.Dx(), rgba.Rect.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, tw, th))
	for ty := 0; ty < th; ty++ {
		y0, y1 := ty*h/th, max(ty*h/th+1, (ty+1)*h/th)
		for tx := 0; tx < tw; tx++ {
			x0, x1 := tx*w/tw, max(tx*w/tw+1, (tx+1)*w/tw)
			var r, g, bl, a, n uint64
			for y := y0; y < y1; y++ {
				row := rgba.Pix[y*rgba.Stride:]
				for x := x0; x < x1; x++ {
					p := row[x*4 : x*4+4]
					r, g, bl, a = r+uint64(p[0]), g+uint64(p[1]), bl+uint64(p[2]), a+uint64(p[3])
					n++
				}
			}
			o := out.Pix[ty*out.Stride+tx*4:]
			if a == 0 {
				o[0], o[1], o[2], o[3] = 0, 0, 0, 0
				continue
			}
			// Un-premultiply the averaged colour.
			o[0], o[1], o[2], o[3] = uint8(r*255/a), uint8(g*255/a), uint8(bl*255/a), uint8(a/n)
		}
	}
	return out
}

// TransmitSequence is the complete Kitty graphics command sequence that
// transmits p directly as PNG under id and creates a virtual placement of
// p.Cols×p.Rows cells for Unicode placeholders, quietly. It is one string so
// the host writes it without interleaving other output between chunks.
func TransmitSequence(id uint32, p Prepared) string {
	payload := base64.StdEncoding.EncodeToString(p.PNG)
	var b strings.Builder
	first := true
	for len(payload) > 0 {
		chunk := payload[:min(len(payload), transmitChunkBytes)]
		payload = payload[len(chunk):]
		more := "0"
		if len(payload) > 0 {
			more = "1"
		}
		b.WriteString("\x1b_G")
		if first {
			b.WriteString("a=T,f=100,t=d,q=2,U=1,i=" + strconv.FormatUint(uint64(id), 10) +
				",c=" + strconv.Itoa(p.Cols) + ",r=" + strconv.Itoa(p.Rows) + ",")
			first = false
		}
		b.WriteString("m=" + more + ";" + chunk + "\x1b\\")
	}
	return b.String()
}

// DeleteSequence deletes the image with id and frees its data, quietly. It
// names only that ID, never all images.
func DeleteSequence(id uint32) string {
	return "\x1b_Ga=d,d=I,q=2,i=" + strconv.FormatUint(uint64(id), 10) + "\x1b\\"
}
