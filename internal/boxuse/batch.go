package boxuse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	MaxBatchBytes   = 256 << 10
	MaxBatchActions = 32
)

// Batch is a fully validated finite list of existing box-use requests. It does
// not grant authority or make input atomic against other attached participants.
type Batch struct {
	requests []request
	lines    [][]byte
	autoWait []bool
}

// ReadBatch consumes the complete local input before any attachment is opened.
// A final detach is optional; an earlier detach would silently discard actions.
func ReadBatch(input io.Reader) (Batch, error) {
	var batch Batch
	data, err := io.ReadAll(io.LimitReader(input, MaxBatchBytes+1))
	if err != nil {
		return batch, err
	}
	if len(data) > MaxBatchBytes {
		return batch, errors.New("box-use batch exceeds 256 KiB")
	}
	if !utf8.Valid(data) {
		return batch, errors.New("box-use batch is not valid UTF-8")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	ids := map[string]bool{}
	for scanner.Scan() {
		if len(batch.requests) == MaxBatchActions {
			return Batch{}, errors.New("box-use batch exceeds 32 actions")
		}
		if len(batch.requests) > 0 && batch.requests[len(batch.requests)-1].Op == "detach" {
			return Batch{}, errors.New("detach must be the final batch action")
		}
		r, err := parseRequest(scanner.Bytes())
		if err == nil && (r.Op == "text" || r.Op == "key") {
			err = validateInput(r)
		}
		if err != nil {
			return Batch{}, fmt.Errorf("batch line %d: %w", len(batch.requests)+1, err)
		}
		if ids[r.ID] {
			return Batch{}, fmt.Errorf("duplicate batch id %q", r.ID)
		}
		ids[r.ID] = true
		batch.requests = append(batch.requests, r)
		batch.lines = append(batch.lines, append(append([]byte(nil), scanner.Bytes()...), '\n'))
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(scanner.Bytes(), &fields) // parseRequest already validated it.
		batch.autoWait = append(batch.autoWait, r.Op == "wait" && fields["since"] == nil)
	}
	if err := scanner.Err(); err != nil {
		return Batch{}, fmt.Errorf("box-use batch: %w", err)
	}
	if len(batch.requests) == 0 {
		return Batch{}, errors.New("box-use batch is empty")
	}
	return batch, nil
}

// Run executes requests one at a time through the ordinary managed frontend.
// attach must honor cancellation and close its streams on return. All ready,
// result, peer and terminal records are preserved on output. Input is never
// retried. The existing attach callback owns its controller receipt cleanup.
func (b Batch) Run(ctx context.Context, output io.Writer, attach func(context.Context, io.Reader, io.Writer) error) error {
	if len(b.requests) == 0 {
		return errors.New("box-use batch is empty")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input, send := io.Pipe()
	receive, replies := io.Pipe()
	defer func() { _ = input.Close(); _ = send.Close(); _ = receive.Close(); _ = replies.Close() }()
	done := make(chan error, 1)
	go func() {
		err := attach(ctx, input, replies)
		_ = input.Close()
		_ = replies.Close()
		done <- err
	}()
	stop := context.AfterFunc(ctx, func() { _ = send.Close(); _ = receive.Close() })
	defer stop()
	err := b.exchange(ctx, receive, send, output)
	_ = send.Close()
	// exchange drains detach on an ordinary request rejection. Exceptional
	// framing, cancellation or local output errors close only this attachment.
	if err != nil {
		cancel()
		_ = receive.Close()
	}
	select {
	case attachErr := <-done:
		if err == nil && attachErr != nil {
			return fmt.Errorf("box-use attachment receipt/cleanup failed; printed per-action results remain valid: %w", attachErr)
		}
		return errors.Join(err, attachErr)
	case <-time.After(8 * time.Second):
		cancel()
		return errors.Join(err, errors.New("box-use attachment receipt/cleanup timed out; printed per-action results remain valid; attachment completion is unknown; inspect before retrying"))
	}
}

func (b Batch) exchange(ctx context.Context, receive io.Reader, send *io.PipeWriter, output io.Writer) error {
	scanner := bufio.NewScanner(receive)
	scanner.Buffer(make([]byte, 4096), 512<<10)
	next := func() (response, error) {
		var r response
		if !scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return r, err
			}
			if err := scanner.Err(); err != nil {
				return r, err
			}
			return r, io.ErrUnexpectedEOF
		}
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return r, fmt.Errorf("invalid box-use response: %w", err)
		}
		line := append(append([]byte(nil), scanner.Bytes()...), '\n')
		// Like the managed frontend's encoder, retain at most one outstanding
		// output write. A blocked caller cannot delay cancellation/receipt cleanup.
		written := make(chan error, 1)
		go func() {
			n, err := output.Write(line)
			if err == nil && n != len(line) {
				err = io.ErrShortWrite
			}
			written <- err
		}()
		select {
		case err := <-written:
			if err != nil {
				return r, err
			}
		case <-ctx.Done():
			return r, ctx.Err()
		}
		return r, nil
	}
	hello, err := next()
	if err != nil {
		return fmt.Errorf("box-use ready: %w", err)
	}
	if hello.Type != "ready" || hello.Version != 1 || hello.Session == "" || hello.Client == "" {
		return errors.New("box-use returned no compatible session identity")
	}
	var failure error
	var revision uint64
	for i, request := range b.requests {
		line := b.lines[i]
		if b.autoWait[i] {
			if revision == 0 {
				// Revisions belong to this attachment. Never interpret an earlier
				// invocation's revision as the baseline of a new managed replica.
				id := b.baselineID()
				data, _ := json.Marshal(map[string]any{"id": id, "op": "screen"})
				if _, err := send.Write(append(data, '\n')); err != nil {
					return fmt.Errorf("box-use baseline: %w", err)
				}
				r, err := next()
				if err != nil {
					return fmt.Errorf("box-use baseline: %w", err)
				}
				if r.ID != id || r.Type != "screen" || r.Screen == nil || r.Screen.Revision == 0 {
					return errors.New("box-use did not return a valid attachment-local baseline")
				}
				revision = r.Screen.Revision
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(line, &fields)
			fields["since"] = json.RawMessage(strconv.FormatUint(revision, 10))
			line, _ = json.Marshal(fields)
			line = append(line, '\n')
		}
		if _, err := send.Write(line); err != nil {
			return fmt.Errorf("box-use request %q delivery may be uncertain; inspect before retrying: %w", request.ID, err)
		}
		r, err := next()
		if err != nil {
			return fmt.Errorf("box-use request %q has no confirmed result; inspect before retrying: %w", request.ID, err)
		}
		if r.Type == "exit" || r.Type == "disconnected" {
			return errors.New("box-use session ended before the batch completed; inspect results before retrying")
		}
		if r.ID != request.ID {
			return errors.New("box-use response ID mismatch; delivery may be uncertain; inspect before retrying")
		}
		if r.Type == "input_uncertain" {
			return fmt.Errorf("box-use input %q uncertain: %s", r.ID, r.Error)
		}
		if r.Type == "error" {
			failure = fmt.Errorf("box-use request %q rejected: %s; remaining actions were not sent", r.ID, r.Error)
			break
		}
		want := request.Op
		if want == "text" || want == "key" {
			want = "sent"
		}
		if want == "detach" {
			want = "detached"
		}
		if r.Type != want {
			return fmt.Errorf("unexpected box-use response %q; inspect results before retrying", r.Type)
		}
		if r.Screen != nil {
			revision = r.Screen.Revision
		}
		if want == "detached" {
			return nil
		}
	}
	_ = send.Close() // EOF detaches only after the last confirmed result.
	r, err := next()
	if err != nil {
		return errors.Join(failure, fmt.Errorf("box-use detach: %w", err))
	}
	if r.Type != "detached" {
		return errors.Join(failure, fmt.Errorf("box-use ended with %s instead of detach; inspect the session", r.Type))
	}
	return failure
}

func (b Batch) baselineID() string {
	for i := 0; ; i++ {
		id := fmt.Sprintf("yoloctl-baseline-%d", i)
		used := false
		for _, r := range b.requests {
			if r.ID == id {
				used = true
				break
			}
		}
		if !used {
			return id
		}
	}
}
