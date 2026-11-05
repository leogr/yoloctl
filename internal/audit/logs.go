package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

type logState struct {
	Last         uint64 `json:"last"`
	Drops        uint64 `json:"drops"`
	MetadataGaps uint64 `json:"metadata_gaps"`
}

type Store struct {
	Root string
	Now  func() time.Time
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Store) state() (logState, error) {
	var state logState
	raw, err := safepath.ReadFile(s.Root, "state.json", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	return state, err
}
func (s Store) save(state logState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return safepath.WriteFile(s.Root, "state.json", raw, 0600)
}

type segment struct {
	name     string
	first    uint64
	size     int64
	modified time.Time
}

func (s Store) segments() ([]segment, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []segment
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		first, parse := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ".jsonl"), 10, 64)
		info, err := entry.Info()
		if parse != nil || first == 0 || len(entry.Name()) != 26 || err != nil || !info.Mode().IsRegular() || info.Size() > SegmentBytes+MaxEvent {
			return nil, errors.New("invalid audit log segment")
		}
		result = append(result, segment{entry.Name(), first, info.Size(), info.ModTime()})
	}
	if len(result) > 4096 {
		return nil, errors.New("audit log inventory exceeds retention bounds")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].first < result[j].first })
	return result, nil
}

// Scan fixes an upper cursor and time before reading. Collection never waits
// on a reader. Concurrent retention is reported as a gap, never hidden.
func (s Store) Scan(q Query, emit func(Event) error) (Summary, error) {
	if q.Tail {
		if q.Limit < 1 || q.Limit > 1000 {
			return Summary{}, errors.New("invalid audit tail limit")
		}
		limit := q.Limit
		q.Limit = 0
		q.Tail = false
		tail := make([]Event, 0, limit)
		next := 0
		summary, err := s.Scan(q, func(e Event) error {
			if len(tail) < limit {
				tail = append(tail, e)
			} else {
				tail[next] = e
				next = (next + 1) % limit
			}
			return nil
		})
		if err != nil {
			return summary, err
		}
		start := 0
		if len(tail) == limit {
			start = next
		}
		for i := range tail {
			event := tail[(start+i)%len(tail)]
			if err := emit(event); err != nil {
				return summary, err
			}
		}
		summary.Count = len(tail)
		return summary, nil
	}

	var summary Summary
	if q.Limit < 0 || q.Limit > 1000 || (!q.Until.IsZero() && q.Since.After(q.Until)) {
		return summary, errors.New("invalid audit history bounds")
	}
	snapshotNow := s.now()
	cutoff := snapshotNow.Add(-RetainAge)
	state, err := s.state()
	if err != nil {
		return summary, err
	}
	files, err := s.segments()
	if err != nil {
		return summary, err
	}
	summary.Upper, summary.Drops, summary.MetadataGaps = state.Last, state.Drops, state.MetadataGaps
	if q.Upper != 0 && q.Upper < summary.Upper {
		summary.Upper = q.Upper
	}
	summary.Since, summary.Until = q.Since, q.Until
	if summary.Since.IsZero() {
		summary.Since = cutoff
	}
	if summary.Until.IsZero() {
		summary.Until = snapshotNow
	}
	if !q.Since.IsZero() && q.Since.After(summary.Until) {
		return summary, errors.New("audit start time is after its fixed end time")
	}
	if summary.Since.After(summary.Until) {
		summary.Since = summary.Until
	}
	if len(files) != 0 {
		summary.First = files[0].first
	}
	summary.Gap = (!q.Since.IsZero() && q.Since.Before(cutoff)) || summary.Until.Before(cutoff) || (len(files) == 0 && summary.Upper > q.After) || (q.After != 0 && summary.First > 0 && q.After < summary.First-1) || (q.After == 0 && summary.First > 1)
	var previous uint64
	for _, file := range files {
		if file.first > summary.Upper {
			break
		}
		raw, err := safepath.ReadFile(s.Root, file.name, SegmentBytes+MaxEvent)
		if errors.Is(err, os.ErrNotExist) {
			summary.Gap = true
			continue
		}
		if err != nil {
			return summary, err
		}
		lines := bytes.Split(raw, []byte{'\n'})
		for i, line := range lines {
			if len(line) == 0 {
				continue
			}
			var e Event
			if i == len(lines)-1 || len(line) > MaxEvent || json.Unmarshal(line, &e) != nil || e.Cursor == 0 || e.Cursor <= previous {
				summary.Gap = true
				continue
			}
			if previous == 0 {
				summary.First = e.Cursor
				if (q.After == 0 && e.Cursor > 1) || (q.After != 0 && e.Cursor > 0 && q.After < e.Cursor-1) {
					summary.Gap = true
				}
			}
			if previous != 0 && e.Cursor != previous+1 {
				summary.Gap = true
			}
			previous = e.Cursor
			if e.Cursor > summary.Upper {
				summary.Last = summary.Upper
				return summary, nil
			}
			if e.Cursor <= q.After {
				continue
			}
			// Last advances over filtered records too, so follow cannot loop forever.
			summary.Last = e.Cursor
			if e.Time.Before(q.Since) || e.Time.Before(cutoff) || e.Time.After(summary.Until) || (q.Box != "" && e.Box != q.Box) {
				continue
			}
			if err := emit(e); err != nil {
				return summary, err
			}
			summary.Count++
			if q.Limit != 0 && summary.Count >= q.Limit {
				return summary, nil
			}
		}
	}
	summary.Last = summary.Upper
	return summary, nil
}

// Writer has one owner: the VM-native audit collector. Readers use Store and
// never share its descriptor, lock or output path.
type Writer struct {
	Store
	state logState
	file  *os.File
	size  int64
	name  string
}

func OpenWriter(s Store) (*Writer, error) {
	if err := safepath.WriteFile(filepath.Dir(s.Root), filepath.Base(s.Root)+"/.format", []byte("yoloctl-audit/v1\n"), 0600); err != nil {
		return nil, err
	}
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	// A crash after append but before metadata fsync must not reuse cursors.
	files, err := s.segments()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.first > state.Last {
			state.Last = f.first
		} // Reserve even a torn first record.
		raw, err := safepath.ReadFile(s.Root, f.name, SegmentBytes+MaxEvent)
		if err != nil {
			return nil, err
		}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			var e Event
			if json.Unmarshal(line, &e) == nil && e.Cursor > state.Last {
				state.Last = e.Cursor
			}
		}
	}
	w := &Writer{Store: s, state: state}
	if len(files) > 0 {
		last := files[len(files)-1]
		body, err := safepath.ReadFile(s.Root, last.name, SegmentBytes+MaxEvent)
		if err != nil {
			return nil, err
		}
		if len(body) == 0 || body[len(body)-1] == '\n' {
			fd, err := unix.Open(filepath.Join(s.Root, last.name), unix.O_WRONLY|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
			if err != nil {
				return nil, err
			}
			w.file, w.name, w.size = os.NewFile(uintptr(fd), last.name), last.name, last.size
		}
	}
	if err := w.rotate(); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := s.save(state); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}
func (w *Writer) Close() error {
	if w.file == nil {
		return nil
	}
	err := errors.Join(w.file.Sync(), w.file.Close())
	w.file = nil
	return err
}
func (w *Writer) Dropped() error { w.state.Drops++; return w.save(w.state) }
func (w *Writer) Append(e Event) error {
	if w.state.Last == ^uint64(0) {
		return errors.New("audit cursor exhausted")
	}
	e.Cursor = w.state.Last + 1
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxEvent {
		return w.Dropped()
	}
	if w.file == nil || w.size+int64(len(raw)) > SegmentBytes {
		if err := w.Close(); err != nil {
			return err
		}
		w.name = fmt.Sprintf("%020d.jsonl", e.Cursor)
		fd, err := unix.Open(filepath.Join(w.Root, w.name), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err != nil {
			return err
		}
		w.file, w.size = os.NewFile(uintptr(fd), w.name), 0
		if err := atomicfile.SyncDir(w.Root); err != nil {
			return err
		}
	}
	n, err := w.file.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	w.size += int64(n)
	if err := w.file.Sync(); err != nil {
		return err
	}
	w.state.Last = e.Cursor
	if !e.MetadataAvailable {
		w.state.MetadataGaps++
	}
	if err := w.save(w.state); err != nil {
		return err
	}
	return w.rotate()
}
func (w *Writer) rotate() error {
	files, err := w.segments()
	if err != nil {
		return err
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	for _, f := range files {
		if f.name == w.name {
			continue
		}
		if total <= RetainBytes && !f.modified.Before(w.now().Add(-RetainAge)) {
			continue
		}
		if err := safepath.RemoveFile(w.Root, f.name); err != nil {
			return err
		}
		total -= f.size
	}
	return nil
}

func (s Store) Counters() (Summary, error) {
	state, err := s.state()
	return Summary{Upper: state.Last, Drops: state.Drops, MetadataGaps: state.MetadataGaps}, err
}

// Prune expires individual records, including a quiet active segment. Only its
// writer calls this method; native maintenance may open a writer after proving
// the service inactive. Readers never acquire the writer's guard.
func (w *Writer) Prune(ctx context.Context) error {
	files, err := w.segments()
	if err != nil {
		return err
	}
	cutoff := w.now().Add(-RetainAge)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := safepath.ReadFile(w.Root, file.name, SegmentBytes+MaxEvent)
		if err != nil {
			return err
		}
		var keep bytes.Buffer
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var event Event
			if json.Unmarshal(line, &event) == nil && !event.Time.Before(cutoff) {
				keep.Write(line)
				keep.WriteByte('\n')
			}
		}
		if bytes.Equal(raw, keep.Bytes()) {
			continue
		}
		active := w.file != nil && w.name == file.name
		if active {
			if err := w.Close(); err != nil {
				return err
			}
		}
		if keep.Len() == 0 {
			err = safepath.RemoveFile(w.Root, file.name)
		} else {
			err = safepath.WriteFile(w.Root, file.name, keep.Bytes(), 0600)
		}
		if err != nil {
			return err
		}
		if active && keep.Len() != 0 {
			fd, err := unix.Open(filepath.Join(w.Root, file.name), unix.O_WRONLY|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
			if err != nil {
				return err
			}
			w.file, w.size = os.NewFile(uintptr(fd), file.name), int64(keep.Len())
		}
	}
	return nil
}
