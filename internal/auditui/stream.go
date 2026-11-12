package auditui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

const retainedRows = 500

type alertRow struct {
	kind   prettylog.EmissionKind
	owner  uint64
	event  *audit.Event // immutable, sanitized snapshot; summaries share its first owner
	repeat int
	until  time.Time
}

// rowSink has no Tea/program pointer. Timer callbacks only retain bounded
// logical rows and signal one wake; the model collects them on its Update path.
type rowSink struct {
	mu     sync.Mutex
	rows   []alertRow
	owners map[uint64]*audit.Event
	last   uint64
	limit  int
	wake   chan struct{}
	done   chan struct{}
	closed bool
}

func newRowSink(limit int) *rowSink {
	return &rowSink{limit: max(1, limit), owners: make(map[uint64]*audit.Event), wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (s *rowSink) Emit(emission prettylog.Emission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	row := alertRow{kind: emission.Kind, owner: emission.Owner}
	switch emission.Kind {
	case prettylog.EmissionRecord:
		if emission.Owner == 0 || emission.Owner <= s.last {
			return errors.New("audit display owner was reused")
		}
		s.last = emission.Owner
		event := eventFromRecord(emission.Record)
		row.event = &event
	case prettylog.EmissionRepeatSummary:
		row.event = s.owners[emission.Owner]
		if row.event == nil || emission.Repeat <= 0 {
			// An evicted or stale owner never resolves to a newer event.
			return nil
		}
		row.repeat, row.until = emission.Repeat, emission.Until
	default:
		return errors.New("invalid audit display emission")
	}
	s.rows = append(s.rows, row)
	if len(s.rows) > s.limit {
		copy(s.rows, s.rows[len(s.rows)-s.limit:])
		clear(s.rows[s.limit:])
		s.rows = s.rows[:s.limit]
	}
	clear(s.owners)
	for _, row := range s.rows {
		s.owners[row.owner] = row.event
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *rowSink) snapshot() []alertRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]alertRow(nil), s.rows...)
}

func (s *rowSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}

// discard releases a replaced filter's snapshots after its stream has drained
// and closed. Older value-model copies (including the caller's initial model)
// may still hold the sink, but must not retain a second event history.
func (s *rowSink) discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows, s.owners = nil, nil
}

func eventFromRecord(r slog.Record) audit.Event {
	e := audit.Event{Time: r.Time, Rule: audit.Clean(r.Message), OutputFields: make(map[string]string)}
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() != slog.KindString {
			return true
		}
		value := audit.Clean(a.Value.String())
		switch a.Key {
		case prettylog.KeyActor, prettylog.KeyExecutor, "origin":
		case "priority":
			e.Severity = value
		default:
			e.OutputFields[a.Key] = value
		}
		return true
	})
	f := e.OutputFields
	e.Executable, e.Container, e.Result, e.Endpoint = f["proc.exepath"], f["container.id"], f["evt.res"], f["fd.rip"]
	e.Box, e.Space, e.Account, e.Agent = f["yolo.box"], f["yolo.space"], f["yolo.account"], f["yolo.agent"]
	e.RuntimeMode, e.NetworkUnconfined = f["yolo.runtime_mode"], f["yolo.network_unconfined"]
	e.MetadataAvailable = f["yolo.metadata_available"] == "true"
	for _, key := range []string{"fs.path.name", "fs.path.target", "fs.path.source", "fd.name"} {
		if e.Path == "" {
			e.Path = f[key]
		}
	}
	e.OutputFields = audit.SelectedFields(e)
	return e
}

type eventStream struct {
	mu         sync.Mutex
	logger     *prettylog.Logger
	closeSink  func()
	key        string
	owner      uint64
	closed     bool
	closedDone chan struct{}
	cancel     context.CancelFunc
}

func newEventStream(ctx context.Context, verbosity prettylog.Verbosity, sink prettylog.EmissionSink, closeSink func()) *eventStream {
	life, cancel := context.WithCancel(ctx)
	s := &eventStream{closeSink: closeSink, cancel: cancel, closedDone: make(chan struct{})}
	s.logger = prettylog.New(io.Discard, prettylog.Options{
		Verbosity: verbosity, Emit: sink,
		Fold: prettylog.FoldOptions{Window: 10 * time.Second, Key: func(slog.Record) (string, bool) {
			// Only add invokes Handle, under s.mu. Timer/Close never call Key.
			return s.key, true
		}},
	})
	// Manual Close cancels life too, releasing the parent registration. No
	// program Send, UI mutation or unbounded event map is owned by this callback.
	context.AfterFunc(life, func() { _ = s.close() })
	return s
}

func (s *eventStream) add(ctx context.Context, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("audit display closed")
	}
	if s.owner == ^uint64(0) {
		return errors.New("audit display owner exhausted")
	}
	s.owner++
	s.key = eventKey(event)
	return s.logger.HandleOwned(ctx, eventRecord(event), s.owner)
}

func (s *eventStream) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logger.Flush()
}

func (s *eventStream) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closedDone
		return s.logger.Err()
	}
	s.closed = true
	s.mu.Unlock()
	err := s.logger.Close() // drains summary into the still-attached sink
	if s.closeSink != nil {
		s.closeSink()
	}
	s.cancel()
	close(s.closedDone)
	return err
}
