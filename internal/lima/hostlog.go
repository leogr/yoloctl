package lima

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/rotatelog"
)

// HostDiagnostic is the private host.log grammar, not arbitrary child output.
// Old records lack Space; they are available only in the unfiltered view.
type HostDiagnostic struct {
	Time                   time.Time
	Operation, Text, Space string
}

func ParseHostDiagnostic(line string) (HostDiagnostic, bool) {
	var r HostDiagnostic
	stamp, rest, ok := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
	if !ok {
		return r, false
	}
	var err error
	if r.Time, err = time.Parse(time.RFC3339Nano, stamp); err != nil {
		return r, false
	}
	quoted := func() (string, bool) {
		rest = strings.TrimLeft(rest, " ")
		q, e := strconv.QuotedPrefix(rest)
		if e != nil || !strings.HasPrefix(q, "\"") {
			return "", false
		}
		v, e := strconv.Unquote(q)
		rest = rest[len(q):]
		return v, e == nil
	}
	if r.Operation, ok = quoted(); !ok {
		return HostDiagnostic{}, false
	}
	if !strings.HasPrefix(rest, " ") {
		return HostDiagnostic{}, false
	}
	if r.Text, ok = quoted(); !ok {
		return HostDiagnostic{}, false
	}
	if rest == "" {
		return r, true
	}
	if !strings.HasPrefix(rest, " space=") {
		return HostDiagnostic{}, false
	}
	rest = strings.TrimPrefix(rest, " space=")
	if r.Space, ok = quoted(); !ok || rest != "" {
		return HostDiagnostic{}, false
	}
	return r, true
}

// TailHostDiagnostics selects the last lines matching an optional explicit
// space, across both retained generations. Follow reads the opened generation
// to EOF and follows the current name after rotation, as the VM tail view does.
// emit receives the original record bytes, including their original newline.
func TailHostDiagnostics(ctx context.Context, space string, lines int, follow bool, emit func([]byte) error) error {
	path, err := layout.HostDiagnosticsLogPath()
	if err != nil {
		return err
	}
	if lines < 0 {
		return errors.New("--lines must not be negative")
	}
	current, history, err := hostLogSnapshot(path)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	selected := make([][]byte, 0)
	var pending []byte
	parts := bytes.SplitAfter(history, []byte{'\n'})
	for _, raw := range parts {
		if len(raw) == 0 {
			continue
		}
		for len(raw) > hostLogLimit {
			if hostDiagnosticMatches(raw[:hostLogLimit], space) {
				selected = append(selected, raw[:hostLogLimit])
			}
			raw = raw[hostLogLimit:]
		}
		if follow && raw[len(raw)-1] != '\n' {
			pending = append(pending, raw...)
			continue
		}
		if hostDiagnosticMatches(raw, space) {
			selected = append(selected, raw)
		}
	}
	for _, raw := range selected[max(0, len(selected)-lines):] {
		if err := emit(raw); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}
	ticker := time.NewTicker(time.Second) // same ordinary polling cadence as tail -F
	defer ticker.Stop()
	return followHostLog(ctx, path, &current, pending, space, emit, ticker.C)
}

func hostDiagnosticMatches(raw []byte, space string) bool {
	if space == "" {
		return true
	}
	r, ok := ParseHostDiagnostic(string(raw))
	return ok && r.Space == space
}

func readHostGeneration(f *os.File) ([]byte, int64, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	// Existing rotation bounds each generation. For an oversized older file,
	// read its bounded suffix, like the shared private-log tail reader.
	start := max(st.Size()-hostLogLimit, 0)
	raw, err := io.ReadAll(io.NewSectionReader(f, start, st.Size()-start))
	return raw, st.Size(), err
}

func hostLogSnapshot(path string) (*os.File, []byte, error) {
	for range rotatelog.Attempts {
		current, err := rotatelog.OpenRead(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				previous, e := rotatelog.OpenRead(path + ".1")
				if e == nil {
					_ = previous.Close()
					continue
				}
				if !errors.Is(e, os.ErrNotExist) {
					return nil, nil, e
				}
			}
			return nil, nil, err
		}
		currentInfo, err := current.Stat()
		var history []byte
		if err == nil {
			previous, e := rotatelog.OpenRead(path + ".1")
			if e == nil {
				st, e := previous.Stat()
				if e == nil && !os.SameFile(st, currentInfo) {
					history, _, e = readHostGeneration(previous)
				}
				_ = previous.Close()
				err = e
			} else if !errors.Is(e, os.ErrNotExist) {
				err = e
			}
		}
		var raw []byte
		var size int64
		if err == nil {
			raw, size, err = readHostGeneration(current)
		}
		if err == nil {
			_, err = current.Seek(size, io.SeekStart)
		}
		if err == nil {
			named, e := rotatelog.OpenRead(path)
			if e == nil {
				st, e := named.Stat()
				_ = named.Close()
				if e == nil && os.SameFile(st, currentInfo) {
					return current, append(history, raw...), nil
				}
				err = e
			} else if !errors.Is(e, os.ErrNotExist) {
				err = e
			}
		}
		_ = current.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, fmt.Errorf("host diagnostic log kept changing while reading %s", path)
}

func followHostLog(ctx context.Context, path string, current **os.File, pending []byte, space string, emit func([]byte) error, ticks <-chan time.Time) error {
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		raw := pending
		pending = nil
		if hostDiagnosticMatches(raw, space) {
			return emit(raw)
		}
		return nil
	}
	push := func(raw []byte) error {
		for len(raw) != 0 {
			n := len(raw)
			if at := bytes.IndexByte(raw, '\n'); at >= 0 {
				n = at + 1
			}
			n = min(n, hostLogLimit-len(pending))
			pending = append(pending, raw[:n]...)
			raw = raw[n:]
			if len(pending) == hostLogLimit || pending[len(pending)-1] == '\n' {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, flush())
		}
		n, err := (*current).Read(buf)
		if n != 0 {
			if e := push(buf[:n]); e != nil {
				return e
			}
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return errors.Join(err, flush())
		}
		named, err := rotatelog.OpenRead(path)
		if err == nil {
			oldInfo, e := (*current).Stat()
			newInfo, e2 := named.Stat()
			if e != nil || e2 != nil {
				_ = named.Close()
				return errors.Join(e, e2)
			}
			if !os.SameFile(oldInfo, newInfo) {
				// Drain any append that landed on the retired descriptor after EOF.
				for {
					if e := ctx.Err(); e != nil {
						_ = named.Close()
						return errors.Join(e, flush())
					}
					n, e := (*current).Read(buf)
					if n != 0 {
						if e2 := push(buf[:n]); e2 != nil {
							_ = named.Close()
							return e2
						}
					}
					if errors.Is(e, io.EOF) {
						break
					}
					if e != nil {
						_ = named.Close()
						return e
					}
				}
				if e := flush(); e != nil {
					_ = named.Close()
					return e
				}
				_ = (*current).Close()
				*current = named
				continue
			}
			_ = named.Close()
			pos, e := (*current).Seek(0, io.SeekCurrent)
			if e != nil {
				return e
			}
			if newInfo.Size() < pos {
				if e := flush(); e != nil {
					return e
				}
				if _, e := (*current).Seek(0, io.SeekStart); e != nil {
					return e
				}
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.Join(err, flush())
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), flush())
		case <-ticks:
		}
	}
}
