package boxuse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/session"
)

type request struct {
	ID        string   `json:"id"`
	Op        string   `json:"op"`
	Since     uint64   `json:"since"`
	TimeoutMS *int     `json:"timeout_ms"`
	SettleMS  *int     `json:"settle_ms"`
	Text      string   `json:"text"`
	Key       string   `json:"key"`
	Modifiers []string `json:"modifiers"`
	Offset    int      `json:"offset"`
	Limit     int      `json:"limit"`
}

type response struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type"`
	Version  int          `json:"version,omitempty"`
	Session  string       `json:"session,omitempty"`
	Client   string       `json:"client,omitempty"`
	Screen   *observation `json:"screen,omitempty"`
	TimedOut bool         `json:"timed_out,omitempty"`
	History  *history     `json:"history,omitempty"`
	Error    string       `json:"error,omitempty"`
	Status   *int         `json:"status,omitempty"`
}

type history struct {
	Total     int      `json:"total"`
	Start     int      `json:"start"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

func parseRequest(p []byte) (request, error) {
	var r request
	// Reject duplicate keys before decoding into the typed request. In
	// particular, two op/text fields must never silently change the action.
	fields := make(map[string]json.RawMessage)
	keys := json.NewDecoder(bytes.NewReader(p))
	start, err := keys.Token()
	if err != nil || start != json.Delim('{') {
		return r, errors.New("expected a JSON request object")
	}
	for keys.More() {
		token, err := keys.Token()
		if err != nil {
			return r, err
		}
		name, ok := token.(string)
		if !ok {
			return r, errors.New("invalid request key")
		}
		if _, exists := fields[name]; exists {
			return r, fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return r, err
		}
		if bytes.Equal(value, []byte("null")) {
			return r, fmt.Errorf("field %q cannot be null", name)
		}
		fields[name] = value
	}
	d := json.NewDecoder(bytes.NewReader(p))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, err
	}
	if d.Decode(new(any)) != io.EOF {
		return r, errors.New("expected one JSON object per line")
	}
	if r.ID == "" || len(r.ID) > 128 {
		return r, errors.New("id must contain 1..128 bytes")
	}
	allowed := " id op "
	switch r.Op {
	case "screen":
		allowed += "since "
	case "wait":
		allowed += "since timeout_ms settle_ms "
	case "text":
		allowed += "text "
	case "key":
		allowed += "key modifiers "
	case "history":
		allowed += "offset limit "
	case "detach":
	default:
		return r, errors.New("unknown op; use screen, wait, text, key, history or detach")
	}
	for name := range fields {
		if !strings.Contains(allowed, " "+name+" ") {
			return r, fmt.Errorf("field %q is not valid for %s", name, r.Op)
		}
	}
	if r.TimeoutMS != nil && (*r.TimeoutMS < 0 || *r.TimeoutMS > 60000) {
		return r, errors.New("timeout_ms must be 0..60000")
	}
	if r.SettleMS != nil && (*r.SettleMS < 0 || *r.SettleMS > 1000) {
		return r, errors.New("settle_ms must be 0..1000")
	}
	if r.Offset < 0 || r.Limit < 0 || r.Limit > maxRows {
		return r, errors.New("offset must be nonnegative and limit must be 1..200")
	}
	if _, set := fields["limit"]; set && r.Limit == 0 {
		return r, errors.New("limit must be 1..200")
	}
	return r, nil
}

type incoming struct {
	request request
	err     error
}

// A blocked output consumer cannot prevent detach or stall managed event
// consumption. Only one bounded write is outstanding; the caller owns closing
// a blocked output descriptor after Run returns, just as it owns input reads.
func responseEncoder(ctx context.Context, output io.Writer) func(response) error {
	packets := make(chan response)
	result := make(chan error, 1)
	go func() {
		encoder := json.NewEncoder(output)
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-packets:
				result <- encoder.Encode(r)
			}
		}
	}()
	return func(r response) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case packets <- r:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-result:
			return err
		}
	}
}

// Run uses a trusted private session stream. send is the original ordered
// command stdin route, never the JSON request stream. Callers must unblock input
// and send on cancellation. EOF/detach close only this attachment. Request IDs
// correlate responses; they are not replay or exactly-once input guarantees.
// sent means the holder queued every byte written through send by this
// attachment; it does not mean the application read or executed it.
func Run(parent context.Context, transport io.ReadWriteCloser, input io.Reader, output io.Writer, send io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s := newState()
	defer s.close()
	ready, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var attachErr error
	go func() {
		defer close(done)
		attachErr = session.Attach(ctx, transport, session.AttachIO{OnEvent: func(e session.ViewEvent) error {
			if err := s.apply(ctx, e); err != nil {
				return err
			}
			once.Do(func() { close(ready) })
			return nil
		}})
		s.end()
	}()
	defer func() { cancel(); _ = transport.Close(); <-done }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		select {
		case <-ready:
		default:
			return attachErr
		}
	case <-ready:
	}
	encode := responseEncoder(ctx, output)
	s.mu.Lock()
	hello := response{Type: "ready", Version: 1, Session: s.session, Client: s.client}
	s.mu.Unlock()
	if err := encode(hello); err != nil {
		return err
	}
	requests := make(chan incoming, 8)
	go func() {
		defer close(requests)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			r, err := parseRequest(scanner.Bytes())
			select {
			case requests <- incoming{r, err}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case requests <- incoming{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	var previous observation
	var submitted uint64
	finish := func() error {
		view, _ := s.observe(0, observation{})
		result := response{Type: "exit", Screen: &view}
		status := 0
		var exit *session.ExitError
		if errors.As(attachErr, &exit) {
			status = exit.Status
		}
		if attachErr == nil || exit != nil {
			result.Status = &status
		} else {
			result.Type, result.Error = "disconnected", attachErr.Error()
		}
		if err := encode(result); err != nil {
			return err
		}
		return attachErr
	}
	for {
		select {
		case <-done:
			return finish()
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return finish()
		case item, ok := <-requests:
			select {
			case <-done:
				return finish()
			default:
			}
			if !ok {
				return encode(response{Type: "detached"})
			}
			r := item.request
			result := response{ID: r.ID, Type: r.Op}
			if item.err != nil {
				result.Type, result.Error = "error", item.err.Error()
			} else {
				var err error
				switch r.Op {
				case "screen", "wait":
					if r.Op == "wait" {
						timeout, settle := 30000, 100
						if r.TimeoutMS != nil {
							timeout = *r.TimeoutMS
						}
						if r.SettleMS != nil {
							settle = *r.SettleMS
						}
						result.TimedOut, err = s.wait(ctx, r.Since, time.Duration(timeout)*time.Millisecond, time.Duration(settle)*time.Millisecond)
					}
					if err == nil {
						var view observation
						view, previous = s.observe(r.Since, previous)
						result.Screen = &view
					}
				case "history":
					result.History, err = s.history(r)
				case "text", "key":
					var p []byte
					p, err = s.encode(r)
					if err == nil {
						n, writeErr := send.Write(p)
						submitted += uint64(n)
						if writeErr == nil && n != len(p) {
							writeErr = io.ErrShortWrite
						}
						if writeErr == nil {
							writeErr = s.awaitInput(ctx, submitted)
						}
						if writeErr != nil {
							result.Type, result.Error = "input_uncertain", "input may be partially delivered; inspect before sending again: "+writeErr.Error()
							// Report uncertainty even if cancellation stops output; it
							// must never unwrap to an ordinary canceled frontend.
							_ = encode(result)
							return errors.New("box-use " + result.Error)
						}
						result.Type = "sent"
					}
				case "detach":
					result.Type = "detached"
				}
				if err != nil {
					result.Type, result.Error = "error", err.Error()
				}
			}
			if err := encode(result); err != nil {
				return err
			}
			if r.Op == "detach" && item.err == nil {
				return nil
			}
		}
	}
}

func (s *state) history(r request) (*history, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.vt.Get(9)
	if r.Offset > total {
		return nil, errors.New("offset exceeds available history")
	}
	limit := r.Limit
	if limit == 0 {
		limit = 100
	}
	end := total - r.Offset
	start := max(0, end-limit)
	lines, err := s.vt.Lines(true, true, start, end-start)
	if err != nil {
		return nil, err
	}
	lines, truncated := boundLines(lines)
	return &history{Total: total, Start: start, Lines: lines, Truncated: truncated || start > 0}, nil
}
