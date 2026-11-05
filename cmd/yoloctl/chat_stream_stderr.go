package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/controller"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// chatStreamStderrLimit bounds the controller stderr a chat view keeps to
// explain how its stream ended.
const chatStreamStderrLimit = 2 << 10

// callChatStream runs one human chat stream; takeOver is the user's explicit
// choice to replace this account's other session holding the nickname. A
// failed worker reports only its exit status in the controller's exit frame;
// its reason is on stderr, so the last bytes of that are kept and attached to
// the error the view shows.
func callChatStream(ctx context.Context, target controllerTarget, takeOver bool, in io.Reader, out io.Writer) error {
	tail := &stderrTail{}
	req := controller.Request{ID: rand.Text(), Method: "chat.stream", Space: target.Space, Dash: -1}
	if takeOver {
		req.Flags = map[string][]string{chatstream.TakeOverFlag: {"true"}}
	}
	err := target.Call(ctx, req, controller.ClientIO{In: in, Out: out, Err: tail})
	return tail.finish("chat stream", err)
}

// stderrTail keeps the last chatStreamStderrLimit bytes written to it, in a
// buffer that never grows past that limit whatever the size of one write.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if t.buf == nil {
		t.buf = make([]byte, 0, chatStreamStderrLimit)
	}
	if len(p) >= chatStreamStderrLimit {
		// Only the suffix can survive; copy nothing else.
		t.buf = append(t.buf[:0], p[len(p)-chatStreamStderrLimit:]...)
		return n, nil
	}
	if drop := len(t.buf) + len(p) - chatStreamStderrLimit; drop > 0 {
		t.buf = t.buf[:copy(t.buf, t.buf[drop:])] // drop the oldest bytes first
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

// annotate adds the kept stderr to err as one line of printable text:
// controls are dropped and whitespace runs collapse, so remote output cannot
// style or move the view's footer.
func (t *stderrTail) annotate(err error) error {
	if err == nil {
		return nil
	}
	t.mu.Lock()
	text := strings.ToValidUTF8(string(t.buf), "")
	t.mu.Unlock()
	text = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)), " ")
	if text == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, text)
}

// Interactive calls never forward launcher/worker stderr to the terminal.
// Finite CLI helpers retain their explicit output writer.
func callChatViewControllerInto(ctx context.Context, cmd *cobra.Command, method string, input, result any) error {
	target, err := selectControllerTarget(ctx, cmd)
	if err != nil {
		return err
	}
	return callChatViewTargetInto(ctx, target, method, input, result)
}

func callChatViewTargetInto(ctx context.Context, target controllerTarget, method string, input, result any) error {
	tail := &stderrTail{}
	err := callChatTargetInto(ctx, target, method, input, result, tail)
	return tail.finish("chat "+method, err)
}

func (t *stderrTail) finish(operation string, err error) error {
	t.mu.Lock()
	raw := append([]byte(nil), t.buf...)
	t.mu.Unlock()
	// A log failure never fails a call that succeeded.
	return t.annotate(vmhost.RecordDiagnostics(operation, raw, err))
}
