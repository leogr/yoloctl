package main

import (
	"bytes"
	"io"
)

// rawTerminalStderr supplies the output processing disabled by MakeRaw for
// controller diagnostics only. Keep CR state across frames so existing CRLF
// and carriage-return progress updates survive arbitrary transport chunking.
type rawTerminalStderr struct {
	out    io.Writer
	lastCR bool
}

func (w *rawTerminalStderr) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			end = len(p)
		}
		if end > 0 {
			n, err := w.out.Write(p[:end])
			if n > 0 {
				w.lastCR = p[n-1] == '\r'
			}
			total += n
			p = p[n:]
			if err != nil {
				return total, err
			}
			if n < end {
				return total, io.ErrShortWrite
			}
		}
		if len(p) == 0 {
			break
		}
		if !w.lastCR {
			n, err := w.out.Write([]byte{'\r'})
			w.lastCR = n == 1
			if err != nil {
				return total, err
			}
			if n != 1 {
				return total, io.ErrShortWrite
			}
		}
		n, err := w.out.Write(p[:1]) // the original LF
		if n == 1 {
			total++
			p = p[1:]
			w.lastCR = false
		}
		if err != nil {
			return total, err
		}
		if n != 1 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
