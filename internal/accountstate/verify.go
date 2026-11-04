package accountstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const accountVerificationBudget = 5 * time.Second
const accountVerificationStdout = 4096
const accountVerificationStderr = 64 << 10

var errAccountVerificationOverflow = errors.New("native account verification output exceeds bound")

type accountVerificationCapture struct {
	contents bytes.Buffer
	limit    int
	overflow bool
}

func (w *accountVerificationCapture) Write(raw []byte) (int, error) {
	if len(raw) > w.limit-w.Len() {
		w.overflow = true
		return 0, errAccountVerificationOverflow
	}
	return w.contents.Write(raw)
}

// Named buffer deliberately exposes no ReaderFrom method: exec's io.Copy
// fast paths must pass through the bounded Write above.
func (w *accountVerificationCapture) Len() int       { return w.contents.Len() }
func (w *accountVerificationCapture) Bytes() []byte  { return w.contents.Bytes() }
func (w *accountVerificationCapture) String() string { return w.contents.String() }

// Only the read-only verify action uses this transport. Bound capture BEFORE
// growth and settle inherited stdout/stderr even if the original helper exits.
// A command/cancel/pipe/overflow error never yields a partial storage identity.
func runAccountVerification(ctx context.Context, cmd *exec.Cmd) (Result, error) {
	var zero Result
	if ctx == nil || cmd == nil {
		return zero, errors.New("native account verification command unavailable")
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(err, context.Cause(ctx))
	}
	stdout := accountVerificationCapture{limit: accountVerificationStdout}
	stderr := accountVerificationCapture{limit: accountVerificationStderr}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		err = errors.Join(err, errAccountVerificationOverflow)
	}
	err = errors.Join(err, ctx.Err(), context.Cause(ctx))
	if err != nil {
		// Preserve the existing trusted verifier diagnostic when capture completed.
		// Overflowed output is not rendered as a usable partial diagnostic.
		if !stdout.overflow && !stderr.overflow && stderr.Len() != 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return zero, err
	}
	result, err := decodeAccountVerification(stdout.Bytes())
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(err, context.Cause(ctx))
	}
	return result, nil
}

// guest.py's verify branch returns ONLY uuid. Keep ordinary Python whitespace,
// while refusing aliases, duplicate/missing fields and additional documents.
func decodeAccountVerification(raw []byte) (Result, error) {
	invalid := errors.New("invalid native account verification response")
	d := json.NewDecoder(bytes.NewReader(raw))
	open, err := d.Token()
	if err != nil || open != json.Delim('{') {
		return Result{}, invalid
	}
	if !d.More() {
		return Result{}, invalid
	}
	key, err := d.Token()
	if err != nil || key != "uuid" {
		return Result{}, invalid
	}
	var uuid string
	if d.Decode(&uuid) != nil || uuid == "" || len(uuid) > 128 || d.More() {
		return Result{}, invalid
	}
	close, err := d.Token()
	if err != nil || close != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return Result{}, invalid
	}
	return Result{UUID: uuid}, nil
}
