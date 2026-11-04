package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/spf13/cobra"
)

func TestBoxUseCLIRejectsInvalidLocalActions(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		input string
	}{
		{[]string{"coder", "text"}, ""},
		{[]string{"coder", "text", "value", "--stdin"}, "other"},
		{[]string{"coder", "text", "--stdin"}, strings.Repeat("x", (16<<10)+1)},
		{[]string{"coder", "text", "--stdin"}, "\xff"},
		{[]string{"coder", "text", "\x1b[31m"}, ""},
		{[]string{"coder", "text", "\xff"}, ""},
		{[]string{"coder", "key", "nonsense"}, ""},
		{[]string{"coder", "screen", "--modifier", "ctrl"}, ""},
		{[]string{"coder", "history", "--limit", "201"}, ""},
		{[]string{"coder", "wait", "--wait-timeout", "61s"}, ""},
		{[]string{"coder", "wait", "--settle", "1ns"}, ""},
		{[]string{"coder", "batch"}, ""},
		{[]string{"coder", "batch", "--stdin"}, "{\"id\":\"1\",\"op\":\"screen\"}\nnot JSON"},
	} {
		cmd := newAgentboxUseCmd()
		if err := cmd.ParseFlags(tc.args); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareBoxUseBatch(cmd, cmd.Flags().Args(), strings.NewReader(tc.input)); err == nil {
			t.Fatalf("accepted %q", tc.args)
		}
	}
}

func TestBoxUseCLICompletionAndAttachDelegation(t *testing.T) {
	stubOperatorClient(t, nil)
	root := newClientRootCmd()
	cmd := findByPath(root, "box", "use")
	if cmd == nil {
		t.Fatal("command not registered")
	}
	cmd.SetContext(t.Context())
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"coder"}, "batch"}, {[]string{"coder", "key"}, "arrow_up"},
	} {
		got, flags := cmd.ValidArgsFunction(cmd, tc.args, "")
		if !slices.Contains(got, tc.want) || flags != cobra.ShellCompDirectiveNoFileComp {
			t.Fatalf("completion: %v %v", got, flags)
		}
	}
	fn, ok := cmd.GetFlagCompletionFunc("modifier")
	if !ok {
		t.Fatal("modifier completion missing")
	}
	mods, _ := fn(cmd, nil, "")
	if !slices.Equal(mods, []string{"shift", "ctrl", "alt", "super"}) {
		t.Fatal(mods)
	}
	if err := cmd.Flags().Set("space", "explicit"); err != nil {
		t.Fatal(err)
	}
	attachRoot, argv := boxUseAttachCommand(cmd)
	attach := findByPath(attachRoot, "box", "attach")
	if err := attach.ParseFlags(argv[2:]); err != nil {
		t.Fatal(err)
	}
	if commandMethod(attach) != "box.attach" || attachRoot.Annotations["controller-client"] != "true" {
		t.Fatal("did not select ordinary controller attach")
	}
	if space, _ := attach.Flags().GetString("space"); space != "explicit" {
		t.Fatal("lost space selection")
	}
	for _, flag := range []string{"box-use", "no-autoupdate"} {
		if value, _ := attach.Flags().GetBool(flag); !value {
			t.Fatalf("lost %s", flag)
		}
	}
	if _, registered := controllerOperations["box.use"]; registered {
		t.Fatal("convenience acquired its own RPC authority")
	}
}

// The finite driver runs through the actual frontend/private protocol and
// controller receipt path. Ordinary stdout cannot impersonate session state.
func TestBoxUseCLIBatchUsesExistingManagedFrontend(t *testing.T) {
	vt, err := terminal.New(3, 40, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	_, _ = vt.Write([]byte("REAL_SCREEN\r\n> "))
	snapshot, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := newAgentboxUseCmd()
	if err := cmd.Flags().Set("stdin", "true"); err != nil {
		t.Fatal(err)
	}
	batch, err := prepareBoxUseBatch(cmd, []string{"coder", "batch"}, strings.NewReader("{\"id\":\"1\",\"op\":\"text\",\"text\":\"hello\"}\n{\"id\":\"2\",\"op\":\"key\",\"key\":\"enter\"}\n{\"id\":\"3\",\"op\":\"screen\"}"))
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	input := make(chan string, 1)
	err = batch.Run(t.Context(), &out, func(ctx context.Context, in io.Reader, out io.Writer) error {
		attach := newAgentboxAttachCmd()
		attach.SetContext(ctx)
		attach.SetIn(in)
		attach.SetOut(out)
		attach.SetErr(&diagnostics)
		target := controllerTarget{Operator: true, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
			if req.Method != "box.attach" || !req.View || req.Terminal || streams.Resize != nil {
				return errors.New("wrong attachment route")
			}
			_, _ = io.WriteString(streams.Out, "UNTRUSTED_STDOUT\n")
			if err := boxUseBootstrap(streams.View, snapshot); err != nil {
				return err
			}
			events, received := make(chan func(*boxUseEmitter) error, 4), make(chan string, 8)
			go boxUseAdmit(streams.In, events, received)
			emitter, data := &boxUseEmitter{view: streams.View}, ""
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case p := <-received:
					data += p
				case event := <-events:
					if err := event(emitter); err != nil {
						return err
					}
				case <-streams.Signals:
					// Each chunk is received before its acceptance releases sent.
					for len(received) > 0 {
						data += <-received
					}
					input <- data
					return nil
				}
			}
		}}
		return callControllerBoxUse(attach, target, controller.Request{ID: "test-batch", Method: "box.attach"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-input; got != "hello\r" {
		t.Fatalf("input: %q", got)
	}
	if !strings.Contains(out.String(), "REAL_SCREEN") || strings.Contains(out.String(), "UNTRUSTED_STDOUT") || !strings.Contains(diagnostics.String(), "UNTRUSTED_STDOUT") {
		t.Fatal("lost output provenance")
	}
	if !strings.Contains(out.String(), `"type":"detached"`) {
		t.Fatal("no confirmed detach")
	}
}

// Bash may implement a heredoc as a pipe or a regular file. Regular files
// cannot join epoll, so both box-use stdin paths read them directly; pipes and
// terminals keep cancellable reads.
func TestBoxUseStdinAcceptsRegularFilesAndPipes(t *testing.T) {
	inputs := map[string]func(*testing.T, string) *os.File{
		"regular": func(t *testing.T, content string) *os.File {
			path := filepath.Join(t.TempDir(), "heredoc")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			return f
		},
		"pipe": func(t *testing.T, content string) *os.File {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			go func() { _, _ = io.WriteString(w, content); _ = w.Close() }()
			return r
		},
	}
	vt, err := terminal.New(3, 40, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	_, _ = vt.Write([]byte("STDIN_SCREEN"))
	snapshot, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for name, open := range inputs {
		t.Run(name, func(t *testing.T) {
			// Finite: all input is validated before any attachment.
			for _, tc := range []struct {
				args         []string
				input, error string
			}{
				{[]string{"coder", "batch", "--stdin"}, "{\"id\":\"1\",\"op\":\"screen\"}\nnot JSON\n", "batch line 2"},
				{[]string{"coder", "batch", "--stdin"}, strings.Repeat("\n", (256<<10)+1), "exceeds 256 KiB"},
				{[]string{"coder", "text", "--stdin"}, strings.Repeat("x", (16<<10)+1), "exceeds 16 KiB"},
			} {
				cmd := newAgentboxUseCmd()
				cmd.SetIn(open(t, tc.input))
				cmd.SetArgs(tc.args)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("%v: %v", tc.args, err)
				}
			}
			cmd := newAgentboxUseCmd()
			if err := cmd.Flags().Set("stdin", "true"); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"coder", "batch"}, {"coder", "text"}} {
				reader, err := boxUseInput(open(t, "{\"id\":\"1\",\"op\":\"screen\"}\n"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = prepareBoxUseBatch(cmd, args, reader)
				_ = reader.Close()
				if err != nil {
					t.Fatalf("%v: %v", args, err)
				}
			}
			// Persistent: EOF after the requests is an ordinary detach.
			attach := newAgentboxAttachCmd()
			attach.SetContext(t.Context())
			attach.SetIn(open(t, "{\"id\":\"1\",\"op\":\"screen\"}\nnot JSON\n"))
			var out bytes.Buffer
			attach.SetOut(&out)
			attach.SetErr(io.Discard)
			target := controllerTarget{Operator: true, Call: func(ctx context.Context, _ controller.Request, streams controller.ClientIO) error {
				if err := boxUseBootstrap(streams.View, snapshot); err != nil {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-streams.Signals:
					return nil
				}
			}}
			if err := callControllerBoxUse(attach, target, controller.Request{ID: "stdin-" + name, Method: "box.attach"}); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"type":"screen"`, "STDIN_SCREEN", `"type":"error"`, `"type":"detached"`} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %s:\n%s", want, out.String())
				}
			}
		})
	}
	file := inputs["regular"](t, "kept")
	reader, err := boxUseInput(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("closing box-use input closed the caller's file:", err)
	}
	// A blocked pipe read still ends with the finite command's context.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := newAgentboxUseCmd()
	cmd.SetIn(r)
	cmd.SetArgs([]string{"coder", "batch", "--stdin"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	time.AfterFunc(50*time.Millisecond, cancel)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled pipe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked pipe read was not canceled")
	}
}

// The null device cannot join epoll either. Only the opened descriptor's
// identity qualifies it, so a differently named link reads EOF too, while
// other character devices keep the cancellable reader.
func TestBoxUseStdinReadsNullDeviceAsEOF(t *testing.T) {
	link := filepath.Join(t.TempDir(), "input")
	if err := os.Symlink(os.DevNull, link); err != nil {
		t.Fatal(err)
	}
	vt, err := terminal.New(3, 40, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	snapshot, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"devnull": os.DevNull, "link": link} {
		open := func(t *testing.T) *os.File {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			return f
		}
		t.Run(name, func(t *testing.T) {
			file := open(t)
			reader, err := boxUseInput(file)
			if err != nil {
				t.Fatal(err)
			}
			if data, err := io.ReadAll(reader); err != nil || len(data) != 0 {
				t.Fatalf("null input: %q, %v", data, err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := file.Stat(); err != nil {
				t.Fatal("closing box-use input closed the caller's file:", err)
			}
			// Finite: empty input keeps its local error before any attachment.
			for _, tc := range []struct {
				args  []string
				error string
			}{
				{[]string{"coder", "batch", "--stdin"}, "box-use batch is empty"},
				{[]string{"coder", "text", "--stdin"}, "text must contain 1..16384 UTF-8 bytes"},
			} {
				cmd := newAgentboxUseCmd()
				cmd.SetIn(open(t))
				cmd.SetArgs(tc.args)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("%v: %v", tc.args, err)
				}
			}
			// Persistent: ready, then an ordinary detach without application input.
			attach := newAgentboxAttachCmd()
			attach.SetContext(t.Context())
			attach.SetIn(open(t))
			var out bytes.Buffer
			attach.SetOut(&out)
			attach.SetErr(io.Discard)
			var sent bytes.Buffer
			target := controllerTarget{Operator: true, Call: func(ctx context.Context, _ controller.Request, streams controller.ClientIO) error {
				drained := make(chan struct{})
				go func() { _, _ = io.Copy(&sent, streams.In); close(drained) }()
				if err := boxUseBootstrap(streams.View, snapshot); err != nil {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-streams.Signals:
				}
				<-drained
				return nil
			}}
			if err := callControllerBoxUse(attach, target, controller.Request{ID: "null-" + name, Method: "box.attach"}); err != nil {
				t.Fatal(err)
			}
			ready, detached := strings.Index(out.String(), `"type":"ready"`), strings.Index(out.String(), `"type":"detached"`)
			if ready < 0 || detached < ready || strings.Contains(out.String(), `"type":"error"`) {
				t.Fatalf("frontend output:\n%s", out.String())
			}
			if sent.Len() != 0 {
				t.Fatalf("application input: %q", sent.String())
			}
		})
	}
	zero, err := os.Stat("/dev/zero")
	if err != nil {
		t.Skip(err)
	}
	if isNullDevice(zero) {
		t.Fatal("another character device was treated as the null device")
	}
}
