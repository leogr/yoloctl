package prettylog

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Default limits, in bytes.
const (
	DefaultMessageLimit = 1 << 10
	DefaultFieldsLimit  = 4 << 10
	// MaxWire is the largest encoded record; Limits.Wire may only lower it.
	MaxWire = 8 << 10
)

// Limits bounds what one record may print or encode. Zero values take the
// defaults.
type Limits struct{ Message, Fields, Wire int }

func (l Limits) message() int {
	if l.Message > 0 {
		return l.Message
	}
	return DefaultMessageLimit
}

func (l Limits) fields() int {
	if l.Fields > 0 {
		return l.Fields
	}
	return DefaultFieldsLimit
}

func (l Limits) wire() int {
	if l.Wire > 0 && l.Wire < MaxWire {
		return l.Wire
	}
	return MaxWire
}

// escape neutralises terminal controls: C0/C1 controls, DEL and invalid UTF-8
// bytes render as \xNN and TAB as one space. A newline is kept only when
// newline is true (message continuation lines).
func escape(s string, newline bool) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c >= 0x80 {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\t':
			b.WriteByte(' ')
		case r == '\n' && newline:
			b.WriteByte('\n')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// cut shortens s to at most n bytes at a rune boundary and appends how many
// bytes were cut.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 0 {
		n = 0
	}
	keep := n
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + "…(+" + strconv.Itoa(len(s)-keep) + " bytes)"
}

func roundDuration(d time.Duration) time.Duration {
	if d >= time.Second || d <= -time.Second {
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Millisecond)
}

// valueText is a field value as text, before quoting.
func valueText(v slog.Value) string {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindDuration:
		return roundDuration(v.Duration()).String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return err.Error()
		}
	}
	return v.String()
}

func quoteValue(s string) string {
	if s == "" {
		return `""`
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c == '"' || c == '=' || c == 0x7f || c >= 0x80 && !utf8.ValidString(s) {
			return strconv.Quote(s)
		}
	}
	for _, r := range s {
		if r >= 0x80 && r <= 0x9f {
			return strconv.Quote(s)
		}
	}
	return s
}

// field is one printable attribute after flattening groups.
type field struct {
	key string
	val slog.Value
}

// flatten appends a's printable leaves with dotted group keys.
func flatten(dst []field, prefix string, a slog.Attr) []field {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p = prefix + a.Key + "."
		}
		for _, c := range v.Group() {
			dst = flatten(dst, p, c)
		}
		return dst
	}
	if a.Key == "" && v.Equal(slog.Value{}) {
		return dst
	}
	return append(dst, field{key: prefix + a.Key, val: v})
}

// shownFields returns the record's non-reserved fields that the verbosity
// shows, in order.
func (l *Logger) shownFields(r slog.Record) []field {
	all := l.o.Verbosity >= Verbose
	var out []field
	eachAttr(r, func(a slog.Attr, role attrRole) bool {
		if role != roleField {
			return true
		}
		for _, f := range flatten(nil, "", a) {
			if all || l.fieldShown(f.key) {
				out = append(out, f)
			}
		}
		return true
	})
	return out
}

func (l *Logger) fieldShown(key string) bool {
	for _, k := range l.o.Fields {
		if k == key {
			return true
		}
	}
	return false
}

// fieldsText renders fields as key=value pairs within the fields budget.
func (l *Logger) fieldsText(fs []field) string {
	budget := l.o.Limits.fields()
	parts := make([]string, 0, len(fs))
	for i, f := range fs {
		if budget <= 0 {
			parts = append(parts, "…(+"+strconv.Itoa(len(fs)-i)+" fields)")
			break
		}
		v := valueText(f.val)
		if len(v) > budget {
			v = cut(v, budget)
		}
		budget -= len(f.key) + len(v)
		parts = append(parts, escape(f.key, false)+"="+quoteValue(v))
	}
	return strings.Join(parts, " ")
}

// style wraps text in a colour when colour is on.
func (l *Logger) style(text string, c Color, bold bool) string {
	if !l.o.Color || text == "" {
		return text
	}
	if s := sgr(c, bold); s != "" {
		return s + text + sgrReset
	}
	return text
}

func (l *Logger) role(k Kind) (glyph, text Color, bold bool) {
	p := l.pal
	switch k {
	case KindError:
		return p.Error, p.Error, true
	case KindWarn:
		return p.Warn, p.Warn, true
	case KindNotice:
		return p.Notice, p.Notice, false
	case KindInfo:
		return p.Info, p.Text, false
	case KindOK:
		return p.OK, p.OK, false
	case KindUnknown:
		return p.Warn, p.Warn, true
	case KindSkipped:
		return p.Dim, p.Dim, false
	}
	return p.Debug, p.Debug, false
}

func (l *Logger) timestamp(t time.Time) string {
	switch l.o.Time {
	case TimeClock:
		return t.Local().Format("15:04:05") + "  "
	case TimeRFC3339:
		return t.UTC().Format("2006-01-02T15:04:05.000Z") + " "
	}
	return ""
}

// line is one rendered record and the column where its message starts.
type line struct {
	text string
	col  int
}

// render formats a record that passed the filter. depth is its step depth.
func (l *Logger) render(r slog.Record, st Structure, depth int) line {
	t := r.Time
	if t.IsZero() {
		t = l.now()
	}
	ts := l.timestamp(t)
	indent := strings.Repeat("  ", depth)
	msg := escape(cut(r.Message, l.o.Limits.message()), true)
	if st.Detail && !st.transition() {
		dim := l.pal.Dim
		lines := strings.Split(msg, "\n")
		for i := range lines {
			lines[i] = ts + indent + l.style(lines[i], dim, false)
		}
		return line{text: strings.Join(lines, "\n"), col: ansi.StringWidth(ts) + len(indent)}
	}
	child := st.Parent != ""
	actor := ""
	if !child && st.Actor != "" {
		actor = escape(st.Actor, false)
		if st.Executor != "" {
			sep := " · "
			if l.o.Format == FormatPlain {
				sep = "/"
			}
			actor += sep + escape(st.Executor, false)
		}
	}
	fields := l.fieldsText(l.shownFields(r))
	stepEnd := st.Step != "" && st.State != "" && st.State != string(StepRunning)
	var took string
	if st.Took != 0 || (stepEnd && st.State != string(StepSkipped) && st.State != string(StepUnknown)) {
		took = roundDuration(st.Took).String()
	}
	var errLines []string
	if st.Err != "" {
		errLines = strings.Split(escape(st.Err, true), "\n")
	}
	if l.o.Format == FormatPlain {
		return l.renderPlain(r, st, ts, indent, actor, msg, fields, took, errLines)
	}
	glyphColor, textColor, bold := l.role(st.Kind)
	g := l.o.Glyphs.For(st.Kind)
	head := ts + indent + l.style(g, glyphColor, bold) + " "
	col := ansi.StringWidth(ts) + len(indent) + ansi.StringWidth(g) + 1
	if actor != "" {
		w := ansi.StringWidth(actor)
		pad := ""
		if w < l.o.ActorWidth {
			pad = strings.Repeat(" ", l.o.ActorWidth-w)
		}
		head += l.style(actor, l.pal.Actor, false) + pad + "  "
		col += w + len(pad) + 2
	}
	msgLines := strings.Split(msg, "\n")
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(l.style(msgLines[0], textColor, bold))
	if st.State == string(StepSkipped) && st.Reason != "" {
		b.WriteString("  " + l.style(escape(st.Reason, false), l.pal.Dim, false))
	}
	if fields != "" {
		b.WriteString("  " + l.style(fields, l.pal.Dim, false))
	}
	if took != "" {
		b.WriteString("  " + l.style(took, l.pal.Dim, false))
	}
	cont := strings.Repeat(" ", col)
	for _, m := range msgLines[1:] {
		b.WriteString("\n" + cont + l.style(m, textColor, bold))
	}
	for _, e := range errLines {
		b.WriteString("\n" + cont + l.style(e, textColor, false))
	}
	return line{text: b.String(), col: col}
}

func (l *Logger) renderPlain(r slog.Record, st Structure, ts, indent, actor, msg, fields, took string, errLines []string) line {
	prefix := PlainPrefix(st.Kind)
	if st.Kind == KindSkipped || (st.Step != "" && st.Kind == KindOK) {
		prefix = ""
	}
	if prefix != "" {
		gc, _, bold := l.role(st.Kind)
		prefix = l.style(strings.TrimSuffix(prefix, " "), gc, bold) + " "
	}
	head := ts + indent + prefix
	if actor != "" {
		head += actor + ": "
	}
	msgLines := strings.Split(msg, "\n")
	text := msgLines[0]
	if st.Step != "" {
		switch StepState(st.State) {
		case StepOK:
			text += ": done"
		case StepFailed:
			text += ": failed"
		case StepSkipped:
			text += ": skipped"
			if st.Reason != "" {
				text += ": " + escape(st.Reason, false)
			}
		}
	}
	var b strings.Builder
	b.WriteString(head + text)
	if fields != "" {
		b.WriteString(" " + l.style(fields, l.pal.Dim, false))
	}
	if took != "" {
		b.WriteString(" (" + took + ")")
	}
	if len(errLines) > 0 {
		b.WriteString(": " + errLines[0])
		errLines = errLines[1:]
	}
	cont := ts + indent + "  "
	for _, m := range msgLines[1:] {
		b.WriteString("\n" + cont + m)
	}
	for _, e := range errLines {
		b.WriteString("\n" + cont + e)
	}
	return line{text: b.String(), col: len(ts) + len(indent) + 2}
}

// summary renders a fold summary under a run's first line.
func (l *Logger) summary(run *foldRun) string {
	text := l.o.Glyphs.Repeat + " repeated " + strconv.Itoa(run.n) + " more " + plural(run.n, "time", "times") +
		" until " + run.last.Local().Format("15:04:05")
	ts := l.timestamp(run.last)
	if l.o.Format == FormatPlain {
		return ts + "  " + l.style(text, l.pal.Dim, false)
	}
	pad := run.col - ansi.StringWidth(ts)
	if pad < 0 {
		pad = 0
	}
	return ts + strings.Repeat(" ", pad) + l.style(text, l.pal.Dim, false)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
