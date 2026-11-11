package prettylog

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"
)

// Opaque is a decoded value that was neither a string nor a number, bool,
// duration or time where it was logged (an error, a Stringer, a struct). It
// renders as its text but is never a string, so it cannot satisfy a reserved
// key that requires one.
type Opaque string

// String returns the value's text.
func (o Opaque) String() string { return string(o) }

// Wire errors.
var (
	// ErrUnrepresentable means a record cannot be encoded within the wire
	// limit even after optional attributes are dropped and diagnostic text is
	// cut. Such a record is not emitted in any form.
	ErrUnrepresentable = errors.New("prettylog: record cannot be encoded within the wire limit")
	// ErrIncompleteRecord means the stream ended inside a record.
	ErrIncompleteRecord = errors.New("prettylog: incomplete log record")
)

// structuralLimit is the longest structural (non-diagnostic) reserved value.
const structuralLimit = 256

type wireRecord struct {
	T string     `json:"t"`
	L int        `json:"l"`
	M string     `json:"m"`
	A []wireAttr `json:"a,omitempty"`
}

type wireAttr struct {
	K string          `json:"k"`
	V json.RawMessage `json:"v"`
}

type wireValue struct {
	A  *string  `json:"a,omitempty"`
	I  *int64   `json:"i,omitempty"`
	U  *uint64  `json:"u,omitempty"`
	F  *float64 `json:"f,omitempty"`
	D  *int64   `json:"d,omitempty"`
	TS *string  `json:"ts,omitempty"`
}

func encodeValue(v slog.Value) json.RawMessage {
	var x any
	switch v.Kind() {
	case slog.KindString:
		x = v.String()
	case slog.KindBool:
		x = v.Bool()
	case slog.KindInt64:
		n := v.Int64()
		x = wireValue{I: &n}
	case slog.KindUint64:
		n := v.Uint64()
		x = wireValue{U: &n}
	case slog.KindFloat64:
		f := v.Float64()
		x = wireValue{F: &f}
	case slog.KindDuration:
		d := int64(v.Duration())
		x = wireValue{D: &d}
	case slog.KindTime:
		ts := v.Time().Format(time.RFC3339Nano)
		x = wireValue{TS: &ts}
	default:
		// Any other value travels as opaque text, never as a string: a
		// non-string value under a reserved key stays an ordinary field
		// after decoding instead of becoming valid structure.
		text := valueText(v)
		x = wireValue{A: &text}
	}
	b, err := json.Marshal(x)
	if err != nil { // a float NaN or ±Inf: opaque text, never a string
		text := v.String()
		b, _ = json.Marshal(wireValue{A: &text})
	}
	return b
}

// MarshalRecord encodes r as one JSON object of at most lim.Wire bytes.
// Reserved attributes are kept whole (the last occurrence of each); when the
// record is too large, optional attributes are dropped from the end (adding
// truncated=true), then err, reason and the message are cut. If it still
// does not fit, it returns ErrUnrepresentable.
func MarshalRecord(r slog.Record, lim Limits) ([]byte, error) {
	limit := lim.wire()
	var res, opt []field
	seen := map[string]int{}
	eachAttr(r, func(a slog.Attr, role attrRole) bool {
		switch role {
		case roleReserved:
			seen[a.Key] = len(res)
			res = append(res, field{a.Key, a.Value.Resolve()})
		case roleField:
			opt = flatten(opt, "", a)
		}
		return true
	})
	for _, f := range res {
		if f.key != KeyErr && f.key != KeyReason && f.val.Kind() == slog.KindString && len(f.val.String()) > structuralLimit {
			return nil, ErrUnrepresentable
		}
	}
	msg := r.Message
	encode := func(opt []field, truncated bool) []byte {
		w := wireRecord{T: r.Time.Format(time.RFC3339Nano), L: int(r.Level), M: msg}
		for _, f := range res {
			w.A = append(w.A, wireAttr{f.key, encodeValue(f.val)})
		}
		for _, f := range opt {
			w.A = append(w.A, wireAttr{f.key, encodeValue(f.val)})
		}
		if truncated {
			w.A = append(w.A, wireAttr{KeyTruncated, json.RawMessage("true")})
		}
		b, _ := json.Marshal(w)
		return b
	}
	b := encode(opt, false)
	if len(b) <= limit {
		return b, nil
	}
	for n := len(opt) - 1; n >= 0; n-- {
		if b = encode(opt[:n], true); len(b) <= limit {
			return b, nil
		}
	}
	// Cut diagnostic text, always from its original, until the record fits.
	// Each pass removes at least as many raw bytes as the encoding is over,
	// so keep strictly decreases and the loop ends.
	shrink := func(orig string, set func(string)) bool {
		keep := len(orig)
		for {
			if b = encode(nil, true); len(b) <= limit {
				return true
			}
			if keep == 0 {
				return false
			}
			keep = max(keep-(len(b)-limit)-24, 0)
			if keep == 0 {
				set("")
			} else {
				set(cut(orig, keep))
			}
		}
	}
	for _, key := range []string{KeyErr, KeyReason} {
		if i, ok := seen[key]; ok && res[i].val.Kind() == slog.KindString {
			if shrink(res[i].val.String(), func(s string) { res[i].val = slog.StringValue(s) }) {
				return b, nil
			}
		}
	}
	if shrink(msg, func(s string) { msg = s }) {
		return b, nil
	}
	return nil, ErrUnrepresentable
}

// UnmarshalRecord decodes one MarshalRecord object. Unknown keys and
// malformed values are rejected.
func UnmarshalRecord(b []byte) (slog.Record, error) {
	var w wireRecord
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return slog.Record{}, fmt.Errorf("prettylog: decode record: %w", err)
	}
	if err := atEOF(dec); err != nil {
		return slog.Record{}, fmt.Errorf("prettylog: decode record: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, w.T)
	if err != nil {
		return slog.Record{}, fmt.Errorf("prettylog: decode record time: %w", err)
	}
	r := slog.NewRecord(t, slog.Level(w.L), w.M, 0)
	for _, a := range w.A {
		v, err := decodeValue(a.V)
		if err != nil {
			return slog.Record{}, fmt.Errorf("prettylog: decode attribute %q: %w", a.K, err)
		}
		r.AddAttrs(slog.Attr{Key: a.K, Value: v})
	}
	return r, nil
}

func decodeValue(raw json.RawMessage) (slog.Value, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return slog.Value{}, errors.New("missing value")
	}
	switch raw[0] {
	case '"':
		var s string
		err := json.Unmarshal(raw, &s)
		return slog.StringValue(s), err
	case 't', 'f':
		var v bool
		err := json.Unmarshal(raw, &v)
		return slog.BoolValue(v), err
	case '{':
		var w wireValue
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&w); err != nil {
			return slog.Value{}, err
		}
		if err := atEOF(dec); err != nil {
			return slog.Value{}, err
		}
		set := 0
		for _, p := range []bool{w.A != nil, w.I != nil, w.U != nil, w.F != nil, w.D != nil, w.TS != nil} {
			if p {
				set++
			}
		}
		if set != 1 {
			return slog.Value{}, errors.New("typed value must have exactly one member")
		}
		switch {
		case w.A != nil:
			return slog.AnyValue(Opaque(*w.A)), nil
		case w.I != nil:
			return slog.Int64Value(*w.I), nil
		case w.U != nil:
			return slog.Uint64Value(*w.U), nil
		case w.F != nil:
			return slog.Float64Value(*w.F), nil
		case w.D != nil:
			return slog.DurationValue(time.Duration(*w.D)), nil
		}
		t, err := time.Parse(time.RFC3339Nano, *w.TS)
		return slog.TimeValue(t), err
	}
	return slog.Value{}, errors.New("unsupported value")
}

// atEOF requires that nothing but whitespace follows the decoded value.
func atEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// writeWire writes one framed record. A record that cannot be encoded is
// replaced by one ordinary warning without reserved attributes; if even that
// cannot be encoded, only the error is remembered. Called with l.mu held.
func (l *Logger) writeWire(r slog.Record) error {
	b, err := MarshalRecord(r, l.o.Limits)
	if err != nil {
		if l.err == nil {
			l.err = err
		}
		st := StructureOf(r)
		w := slog.NewRecord(r.Time, LevelWarn, "log record dropped: too large to encode", 0)
		if st.Step != "" {
			w.AddAttrs(slog.String("dropped_step", cut(st.Step, 64)))
		}
		if b, err = MarshalRecord(w, l.o.Limits); err != nil {
			return l.err
		}
	}
	frame := make([]byte, 4, 4+len(b))
	binary.BigEndian.PutUint32(frame, uint32(len(b)))
	if _, err := l.w.Write(append(frame, b...)); err != nil && l.err == nil {
		l.err = err
	}
	return l.err
}

// WireOptions configures NewWireHandler.
type WireOptions struct {
	Verbosity Verbosity
	Limits    Limits
}

// NewWireHandler is the worker-side sink: it filters, encodes and frames each
// record and writes it synchronously.
func NewWireHandler(w io.Writer, o WireOptions) slog.Handler {
	return New(w, Options{Format: FormatWire, Verbosity: o.Verbosity, Limits: o.Limits}).Handler()
}

// WireReader reads the framed records NewWireHandler writes.
type WireReader struct{ r io.Reader }

// NewWireReader reads framed records from r.
func NewWireReader(r io.Reader) *WireReader { return &WireReader{r: r} }

// Next returns the next record: io.EOF at a clean end of stream,
// ErrIncompleteRecord when the stream ends inside a record.
func (wr *WireReader) Next() (slog.Record, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(wr.r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return slog.Record{}, ErrIncompleteRecord
		}
		return slog.Record{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxWire {
		return slog.Record{}, errors.New("prettylog: corrupt log stream: record length " + strconv.FormatUint(uint64(n), 10))
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(wr.r, b); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return slog.Record{}, ErrIncompleteRecord
		}
		return slog.Record{}, err
	}
	return UnmarshalRecord(b)
}
