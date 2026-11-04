package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/boxuse"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newAgentboxUseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use BOX ACTION [VALUE]",
		Short: "Observe or steer a persistent session, then detach (JSON Lines)",
		Long: `Run a finite request or batch through box attach --box-use. This has the
same authorization and ensure semantics: it may start a missing session or
restart a stopped box. By default this convenience skips agent autoupdate.
It leaves the persistent session and other attachments running. A signal or
timeout before the attachment starts exits 75 without cancelling a session start
in progress; check box session before retrying.

Actions: screen, history, text, key, wait, batch. Stdout preserves the versioned
ready/session identity, responses, advisory peer activity and detach record as
JSON Lines; diagnostics go to stderr. A sent result means the session holder
queued all of the input, not that the application read or executed it. Inspect
the screen before typing and after uncertain delivery.
There is no automatic retry, atomic batch, shell parsing or inferred completion.

  yoloctl box use coder screen
  yoloctl box use coder text "Please review this change"
  yoloctl box use coder text --stdin < prompt.txt
  yoloctl box use coder key enter
  yoloctl box use coder key key_c --modifier ctrl
  yoloctl box use coder wait --wait-timeout 5s
  yoloctl box use coder history --offset 0 --limit 50
  yoloctl box use coder batch --stdin < requests.jsonl

Text inserts without Enter, is limited to 16 KiB and requires bracketed paste
mode for newlines/tabs. Wait takes a fresh baseline on this attachment, then
observes changes; its timeout is not a sleep or proof of completion. A timed_out
result still includes the current screen. Batch reads all input before
attaching: 1..32 existing JSON requests, at most 256 KiB in total and below
64 KiB per line. Blank/whitespace-only lines are rejected. Each request requires
a distinct id. An optional detach must be last.
Each action awaits its response; the first rejection stops the remaining actions.
Batch waits without since use the last screen/wait revision from this attachment,
or request a baseline first. Baseline screen records appear in stdout too.
Explicit since is the expert wire value; revisions do not carry across attachments.
For example, requests.jsonl can contain:
  {"id":"prompt","op":"text","text":"Reply with OK"}
  {"id":"enter","op":"key","key":"enter"}
  {"id":"screen","op":"screen"}

Use box attach --box-use for an ongoing response-driven interaction.`,
		Args:              cobra.RangeArgs(2, 3),
		RunE:              runAgentboxUse,
		ValidArgsFunction: completeAgentboxUse,
	}
	cmd.Flags().String("space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().Bool("no-autoupdate", true, "Skip agent autoupdate if attachment restarts a stopped box")
	cmd.Flags().Bool("stdin", false, "Read complete text or JSONL batch from stdin")
	cmd.Flags().Duration("timeout", 2*time.Minute, "Overall deadline including session startup (1s..10m), excluding bounded receipt cleanup")
	cmd.Flags().Duration("wait-timeout", 30*time.Second, "Wait action deadline (0..60s)")
	cmd.Flags().Duration("settle", 100*time.Millisecond, "Wait for quiet after a change (0..1s)")
	cmd.Flags().Int("offset", 0, "History offset backward from newest retained line")
	cmd.Flags().Int("limit", 100, "History line limit (1..200)")
	cmd.Flags().StringSlice("modifier", nil, "Key modifiers: shift, ctrl, alt, super")
	_ = cmd.RegisterFlagCompletionFunc("modifier", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"shift", "ctrl", "alt", "super"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func runAgentboxUse(cmd *cobra.Command, args []string) error {
	if err := validateAgentboxName(args[0]); err != nil {
		return err
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if timeout < time.Second || timeout > 10*time.Minute {
		return asUserError(errors.New("timeout must be 1s..10m"))
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	input := cmd.InOrStdin()
	stdin, _ := cmd.Flags().GetBool("stdin")
	if file, ok := input.(*os.File); ok && stdin {
		reader, err := boxUseInput(file)
		if err != nil {
			return err
		}
		defer func() { reader.Cancel(); _ = reader.Close() }()
		stop := context.AfterFunc(ctx, func() { reader.Cancel() })
		defer stop()
		input = reader
	}
	batch, err := prepareBoxUseBatch(cmd, args, input)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return asUserError(err)
	}
	return batch.Run(ctx, cmd.OutOrStdout(), func(ctx context.Context, input io.Reader, output io.Writer) error {
		root, argv := boxUseAttachCommand(cmd)
		root.SetArgs(append(argv, "--", args[0]))
		root.SetIn(input)
		root.SetOut(output)
		root.SetErr(cmd.ErrOrStderr())
		root.SilenceErrors = true
		return root.ExecuteContext(ctx)
	})
}

// Borrow the public attach command rather than manufacturing a second RPC or
// operation. It retains target selection, server authorization and receipts.
func boxUseAttachCommand(cmd *cobra.Command) (*cobra.Command, []string) {
	root := newClientRootCmd()
	argv := []string{"box", "attach", "--box-use"}
	noUpdate, _ := cmd.Flags().GetBool("no-autoupdate")
	argv = append(argv, fmt.Sprintf("--no-autoupdate=%t", noUpdate))
	if cmd.Flags().Changed("space") {
		space, _ := cmd.Flags().GetString("space")
		argv = append(argv, "--space", space)
	}
	return root, argv
}

func prepareBoxUseBatch(cmd *cobra.Command, args []string, input io.Reader) (boxuse.Batch, error) {
	var empty boxuse.Batch
	op := args[1]
	allowed := map[string]string{"screen": " ", "history": " offset limit ", "text": " stdin ", "key": " modifier ", "wait": " wait-timeout settle ", "batch": " stdin "}
	fields, ok := allowed[op]
	if !ok {
		return empty, errors.New("unknown action; use screen, history, text, key, wait or batch")
	}
	var flagErr error
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if !strings.Contains(" space no-autoupdate timeout "+fields, " "+flag.Name+" ") {
			flagErr = fmt.Errorf("--%s is not valid for %s", flag.Name, op)
		}
	})
	if flagErr != nil {
		return empty, flagErr
	}
	stdin, _ := cmd.Flags().GetBool("stdin")
	if op == "batch" {
		if !stdin || len(args) != 2 {
			return empty, errors.New("use batch --stdin with JSON Lines input")
		}
		return boxuse.ReadBatch(input)
	}
	r := map[string]any{"id": "1", "op": op}
	switch op {
	case "text":
		if stdin {
			if len(args) != 2 {
				return empty, errors.New("text accepts either VALUE or --stdin, not both")
			}
			data, err := io.ReadAll(io.LimitReader(input, (16<<10)+1))
			if err != nil {
				return empty, err
			}
			if len(data) > 16<<10 {
				return empty, errors.New("text exceeds 16 KiB")
			}
			if !utf8.Valid(data) {
				return empty, errors.New("text is not valid UTF-8")
			}
			r["text"] = string(data)
		} else {
			if len(args) != 3 {
				return empty, errors.New("text requires VALUE or --stdin")
			}
			if !utf8.ValidString(args[2]) {
				return empty, errors.New("text is not valid UTF-8")
			}
			r["text"] = args[2]
		}
	case "key":
		if len(args) != 3 {
			return empty, errors.New("key requires a native key name")
		}
		r["key"] = args[2]
		if mods, _ := cmd.Flags().GetStringSlice("modifier"); len(mods) > 0 {
			r["modifiers"] = mods
		}
	default:
		if len(args) != 2 {
			return empty, fmt.Errorf("%s does not accept VALUE", op)
		}
		if op == "history" {
			r["offset"], _ = cmd.Flags().GetInt("offset")
			r["limit"], _ = cmd.Flags().GetInt("limit")
		}
		if op == "wait" {
			for flag, field := range map[string]string{"wait-timeout": "timeout_ms", "settle": "settle_ms"} {
				value, _ := cmd.Flags().GetDuration(flag)
				if value < 0 || value%time.Millisecond != 0 {
					return empty, fmt.Errorf("--%s requires nonnegative whole milliseconds", flag)
				}
				r[field] = value.Milliseconds()
			}
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return empty, err
	}
	return boxuse.ReadBatch(bytes.NewReader(data))
}

func completeAgentboxUse(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		root, argv := boxUseAttachCommand(cmd)
		root.SetContext(cmd.Context())
		attach := findByPath(root, "box", "attach")
		if err := attach.ParseFlags(argv[2:]); err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return attach.ValidArgsFunction(attach, nil, prefix)
	}
	if len(args) == 1 {
		return []string{"screen", "history", "text", "key", "wait", "batch"}, cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 2 && args[1] == "key" {
		return boxuse.KeyNames(), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
