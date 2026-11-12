package mermaid

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const (
	WorkerArgument = "__mermaid-worker"
	MaxDrawing     = 256 << 10
	MaxRows        = 120
	// maxRequest bounds one request frame (a transport bound, not a diagram
	// limit: chat and follow inputs are far smaller).
	maxRequest  = 1 << 20
	maxResponse = MaxDrawing + 4096
	protocol    = 1
)

type Status string

const (
	OK          Status = "ok"
	TooWide     Status = "too_wide"
	TooTall     Status = "too_tall"
	ParseError  Status = "parse_error"
	Timeout     Status = "timeout"
	Failed      Status = "failed"
	Busy        Status = "busy"
	Unavailable Status = "unavailable"
	Internal    Status = "internal"
)

// Result contains display data only; it never carries source paths or raw diagnostics.
type Result struct {
	Status   Status   `json:"status"`
	Lines    []string `json:"lines,omitempty"`
	NeedCols int      `json:"need_cols,omitempty"`
}

func (r Result) Note() string {
	switch r.Status {
	case OK:
		return ""
	case TooWide:
		return fmt.Sprintf("needs %d columns", r.NeedCols)
	case TooTall:
		return fmt.Sprintf("taller than %d rows", MaxRows)
	case ParseError:
		return "parse error"
	case Timeout:
		return "renderer timeout"
	case Failed:
		return "renderer failed"
	case Busy:
		return "renderer busy"
	case Unavailable:
		return "renderer unavailable"
	default:
		return "renderer internal error"
	}
}

type request struct {
	Version  int    `json:"v"`
	Artifact string `json:"artifact"`
	Width    int    `json:"width"`
	Method   string `json:"method"`
	Source   string `json:"source"`
}

type response struct {
	Version  int    `json:"v"`
	Artifact string `json:"artifact"`
	Result   Result `json:"result"`
}

var moduleHash = sync.OnceValue(func() string {
	h := sha256.Sum256(artifact)
	return hex.EncodeToString(h[:])
})

func widthMethod(name string) (ansi.Method, bool) {
	switch name {
	case "grapheme":
		return ansi.GraphemeWidth, true
	case "wcwidth":
		return ansi.WcWidth, true
	default:
		return 0, false
	}
}

func methodName(method ansi.Method) string {
	if method == ansi.GraphemeWidth {
		return "grapheme"
	}
	if method == ansi.WcWidth {
		return "wcwidth"
	}
	return ""
}

func validSource(source string) bool {
	if len(source) == 0 || !utf8.ValidString(source) {
		return false
	}
	for _, r := range source {
		if r != '\n' && unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (r request) valid() bool {
	_, method := widthMethod(r.Method)
	return r.Version == protocol && r.Artifact == moduleHash() &&
		r.Width > 0 && r.Width <= 4096 && method && validSource(r.Source)
}

var errFrameSize = errors.New("invalid mermaid frame size")

func readFrame(r io.Reader, limit uint32) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > limit {
		return nil, errFrameSize
	}
	p := make([]byte, n)
	_, err := io.ReadFull(r, p)
	return p, err
}

func writeFrame(w io.Writer, value any, limit int) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if encoded.Len() == 0 || encoded.Len() > limit {
		return errors.New("mermaid reply exceeds the frame limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(encoded.Len()))
	if _, err := io.Copy(w, bytes.NewReader(header[:])); err != nil {
		return err
	}
	_, err := io.Copy(w, &encoded)
	return err
}

func strictJSON(p []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(p))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing Mermaid frame data")
	}
	return nil
}

func verifyDrawing(p string, width int, method ansi.Method) Result {
	if len(p) == 0 || len(p) > MaxDrawing || !utf8.ValidString(p) {
		return Result{Status: Failed}
	}
	for _, r := range p {
		if r != '\n' && unicode.IsControl(r) {
			return Result{Status: Failed}
		}
	}
	lines := strings.Split(strings.TrimSuffix(p, "\n"), "\n")
	if len(lines) > MaxRows {
		return Result{Status: TooTall}
	}
	needed := 0
	for _, line := range lines {
		needed = max(needed, method.StringWidth(line))
	}
	if needed > width {
		return Result{Status: TooWide, NeedCols: needed}
	}
	return Result{Status: OK, Lines: lines}
}

func (r Result) valid(width int, method ansi.Method) bool {
	switch r.Status {
	case OK:
		if r.NeedCols != 0 || len(r.Lines) == 0 || len(r.Lines) > MaxRows {
			return false
		}
		for _, line := range r.Lines {
			if strings.ContainsRune(line, '\n') {
				return false
			}
		}
		return verifyDrawing(strings.Join(r.Lines, "\n"), width, method).Status == OK
	case TooWide:
		return len(r.Lines) == 0 && r.NeedCols > width && r.NeedCols <= MaxDrawing*2
	case ParseError, Timeout, Failed, TooTall:
		return len(r.Lines) == 0 && r.NeedCols == 0
	default:
		return false
	}
}
