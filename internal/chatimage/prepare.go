package chatimage

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
)

// Preview bounds (plan §3C): a re-encoded PNG of at most 512×512 pixels and
// 2 MiB, decoded from at most 16 Mi source pixels, filling at most
// PreviewCols×PreviewRows cells.
const (
	PreviewCols        = 60
	PreviewRows        = 12
	MaxPreviewPixels   = 512
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

// Prepare checks the dimensions before a single full decode, scales the image
// to fit both MaxPreviewPixels and the cell budget at the measured cell size
// (preserving aspect, never enlarging) and re-encodes it with Go's encoder;
// the uploader's bytes never reach the terminal.
func Prepare(data []byte, cellW, cellH, maxCols, maxRows int) (Prepared, error) {
	if cellW < 1 || cellH < 1 || maxCols < 1 || maxRows < 1 {
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
	w, h := cfg.Width, cfg.Height
	maxW, maxH := min(MaxPreviewPixels, maxCols*cellW), min(MaxPreviewPixels, maxRows*cellH)
	tw, th := w, h
	if tw > maxW || th > maxH {
		// Scale by the tighter bound, rounding down so both limits hold.
		if w*maxH > h*maxW { // width-bound
			tw, th = maxW, max(1, h*maxW/w)
		} else {
			tw, th = max(1, w*maxH/h), maxH
		}
	}
	out := downscale(src, tw, th)
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return Prepared{}, err
	}
	if buf.Len() > MaxPreviewBytes {
		return Prepared{}, ErrTooLarge
	}
	return Prepared{PNG: buf.Bytes(), Cols: (tw + cellW - 1) / cellW, Rows: (th + cellH - 1) / cellH}, nil
}

// downscale averages each target pixel's source area (premultiplied), so
// alpha edges do not darken. tw ≤ width and th ≤ height.
func downscale(src image.Image, tw, th int) *image.NRGBA {
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()
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
