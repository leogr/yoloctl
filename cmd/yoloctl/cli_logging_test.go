package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/spf13/cobra"
)

func captureCLIRecords(t *testing.T) (*bytes.Buffer, *prettylog.Logger) {
	t.Helper()
	var wire bytes.Buffer
	l := prettylog.New(&wire, prettylog.Options{Format: prettylog.FormatWire, Verbosity: prettylog.Trace})
	restore := logging.SetLogger(l)
	t.Cleanup(func() { _ = l.Close(); restore() })
	return &wire, l
}

func readCLIRecords(t *testing.T, wire *bytes.Buffer) []slog.Record {
	t.Helper()
	reader := prettylog.NewWireReader(wire)
	var records []slog.Record
	for {
		r, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
}

func TestCLIOutcomeBelongsToFinalizedOperation(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			wire, _ := captureCLIRecords(t)
			cmd := &cobra.Command{}
			ctx := context.Background()
			cmd.SetContext(ctx)
			failure := errors.New("cleanup failed after VM work")
			if uncertain {
				failure = &controller.OutcomeUnknown{ID: "request-final-123456", Cause: io.EOF}
			}
			err := cliOperation(cmd, "Creating space test", "Created space test", "test", func() error {
				if err := cliOperation(cmd, "Preparing replacement", "Prepared replacement", "test", func() error {
					return cliRunStep(cmd.Context(), "Booting VM", func(ctx context.Context) error { return nil })
				}); err != nil {
					return err
				}
				return failure // final cleanup is allowed to fail after successful steps
			})
			if !errors.Is(err, failure) {
				t.Fatalf("lost error identity: %v", err)
			}
			if cmd.Context() != ctx {
				t.Fatal("command context was not restored")
			}
			// main must not repeat this exact operation's successfully rendered outcome.
			renderLoggedCLIError(err)
			var outcomes []prettylog.Structure
			for _, r := range readCLIRecords(t, wire) {
				s := prettylog.StructureOf(r)
				if s.Outcome != "" {
					outcomes = append(outcomes, s)
				}
			}
			want := string(prettylog.OutcomeFailed)
			if uncertain {
				want = string(prettylog.OutcomeUnknown)
			}
			if len(outcomes) != 1 || outcomes[0].Outcome != want {
				t.Fatalf("outcomes=%+v", outcomes)
			}
		})
	}
}

func TestCLIErrorDoesNotUseEarlierSuccessAsEvidence(t *testing.T) {
	wire, l := captureCLIRecords(t)
	l.Outcome(prettylog.OutcomeOK, "An earlier controller call finished")
	later := &controller.OutcomeUnknown{ID: "request-later-123456", Cause: io.EOF}
	renderLoggedCLIError(later)
	records := readCLIRecords(t, wire)
	if len(records) != 2 || prettylog.StructureOf(records[1]).Outcome != string(prettylog.OutcomeUnknown) || prettylog.StructureOf(records[1]).Err != later.Error() {
		t.Fatalf("later uncertainty lost: %+v", records)
	}
}

func TestCLIErrorUsesOnlyItsRemoteRenderedReceipt(t *testing.T) {
	wire, _ := captureCLIRecords(t)
	remote := &controller.ExitError{Status: 7, Message: "already shown failure", Rendered: true}
	renderLoggedCLIError(fmt.Errorf("wrapped: %w", remote))
	if wire.Len() != 0 {
		t.Fatal("duplicated remote error")
	}
	if classifyError(fmt.Errorf("wrapped: %w", remote)) != 7 {
		t.Fatal("remote status changed")
	}
	unrendered := &controller.ExitError{Status: 7, Message: "sink rejected the first presentation"}
	renderLoggedCLIError(unrendered)
	records := readCLIRecords(t, wire)
	if len(records) != 1 || prettylog.StructureOf(records[0]).Err != unrendered.Error() {
		t.Fatalf("missing retry of presentation: %+v", records)
	}
}

type rejectedCLILog struct{}

func (rejectedCLILog) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCLISinkFailureDoesNotChangeOperationResult(t *testing.T) {
	l := prettylog.New(rejectedCLILog{}, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal})
	restore := logging.SetLogger(l)
	defer restore()
	if err := cliFinishCommand(nil, "Completed"); err != nil {
		t.Fatal(err)
	}
	failure := asUserError(errors.New("invalid input"))
	err := cliFinishCommand(failure, "Completed")
	var rendered *cliRenderedError
	if !errors.Is(err, failure) || errors.As(err, &rendered) || classifyError(err) != exitUser {
		t.Fatalf("sink claimed display or changed status: %v", err)
	}
}

func TestCLIRoutedDataAndRawStreamsBypassOutcome(t *testing.T) {
	for _, method := range []string{"box.exec", "box.attach", "chat.stream", "image.list", "box.show", "box.diff", "account.show"} {
		if cliRoutedOutcome(&cobra.Command{}, method) {
			t.Errorf("added outcome to data/stream %s", method)
		}
	}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", true, "")
	if cliRoutedOutcome(cmd, "image.build") {
		t.Fatal("dry-run receives outcome")
	}
	if !cliRoutedOutcome(&cobra.Command{}, "account.create") {
		t.Fatal("mutation misses final outcome")
	}
}

func TestCLIWarningDoesNotWriteDryRunData(t *testing.T) {
	var data, diagnostics bytes.Buffer
	l := prettylog.New(&diagnostics, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal})
	restore := logging.SetLogger(l)
	defer func() { _ = l.Close(); restore() }()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &agentbox.Config{Name: "worker", Agent: "none", Account: "none", Image: "ubuntu:24.04", Capabilities: []string{"test-missing-capability"}}
	if err := printDryRun(&data, cfg, agentbox.ActualState{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(data.String(), "PLAN: create container") || strings.Contains(data.String(), "cannot compute") || strings.Contains(data.String(), "showing container-level") {
		t.Fatal(data.String())
	}
	if !strings.Contains(diagnostics.String(), "warning: box: cannot compute desired policy") || !strings.Contains(diagnostics.String(), "showing container-level diff only; policy diff skipped") {
		t.Fatal(diagnostics.String())
	}
}

func TestCLIControllerRoutingKeepsDataAndRawBytes(t *testing.T) {
	for _, tc := range []struct {
		name           string
		argv           []string
		stdout, stderr string
		mutation       bool
	}{
		{"json", []string{"image", "list", "--json"}, "[{\"image\":\"example\"}]\n", "", false},
		{"raw exec", []string{"box", "exec", "worker", "--", "printf", "unused"}, "\x1b[31mraw\x00\r\n", "raw diagnostic\r\n", false},
		{"mutation", []string{"account", "create", "personal"}, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			wire, _ := captureCLIRecords(t)
			oldTarget, oldOperator := controllerTargetForCommand, loadOperatorClient
			t.Cleanup(func() { controllerTargetForCommand, loadOperatorClient = oldTarget, oldOperator })
			loadOperatorClient = func() (*controller.OperatorClient, error) { return nil, nil }
			finalized := false
			controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
				return controllerTarget{Space: "test", Call: func(_ context.Context, _ controller.Request, streams controller.ClientIO) error {
					if _, err := io.WriteString(streams.Out, tc.stdout); err != nil {
						return err
					}
					if _, err := io.WriteString(streams.Err, tc.stderr); err != nil {
						return err
					}
					if wire.Len() != 0 {
						t.Fatal("command outcome precedes the final controller response")
					}
					finalized = true
					return nil
				}}, nil
			}
			root := newClientRootCmd()
			var stdout, stderr bytes.Buffer
			root.SetIn(strings.NewReader(""))
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(tc.argv)
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !finalized || stdout.String() != tc.stdout || stderr.String() != tc.stderr {
				t.Fatalf("finalized=%v stdout=%q stderr=%q", finalized, stdout.String(), stderr.String())
			}
			records := readCLIRecords(t, wire)
			if tc.mutation {
				if len(records) != 1 || prettylog.StructureOf(records[0]).Outcome != string(prettylog.OutcomeOK) {
					t.Fatalf("mutation final outcome=%+v", records)
				}
			} else if len(records) != 0 {
				t.Fatalf("data/raw path gained records: %+v", records)
			}
		})
	}
}

// bindCLITestLogger keeps process logging explicit in fixtures that already
// supply a diagnostic writer. It disables folding so separate fixture calls
// cannot share a pending burst; folding itself is tested by prettylog.
func bindCLITestLogger(t *testing.T, w io.Writer) func() {
	t.Helper()
	l := prettylog.New(w, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal})
	restore := logging.SetLogger(l)
	return func() { _ = l.Close(); restore() }
}

func TestCLIRemoteUncertaintyUsesTypedStateWithoutNewGuidance(t *testing.T) {
	for _, state := range []string{"uncertain", "command-uncertain"} {
		t.Run(state, func(t *testing.T) {
			wire, _ := captureCLIRecords(t)
			remote := &controller.ExitError{Status: 2, Message: "query the existing request result; never replay", State: state}
			if err := cliFinishCommand(remote, "Completed"); !errors.Is(err, remote) || classifyError(err) != 2 {
				t.Fatalf("status/error changed: %v", err)
			}
			records := readCLIRecords(t, wire)
			if len(records) != 1 || prettylog.StructureOf(records[0]).Outcome != string(prettylog.OutcomeUnknown) || prettylog.StructureOf(records[0]).Err != remote.Error() {
				t.Fatalf("uncertainty changed: %+v", records)
			}
			if strings.Contains(records[0].Message, "acknowledge") {
				t.Fatal("invented hold guidance")
			}
		})
	}
}

func TestCLIPrivilegedAuthoritySurvivesMessageCap(t *testing.T) {
	var shown bytes.Buffer
	l := prettylog.New(&shown, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal, Limits: prettylog.Limits{Message: 32}})
	restore := logging.SetLogger(l)
	defer func() { _ = l.Close(); restore() }()
	cfg := privilegedTestConfig("dev")
	if err := guardPrivilegedLaunch(context.Background(), cfg, launchCreate, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown.String(), agentbox.PrivilegedAuthority) {
		t.Fatalf("authority warning was truncated: %s", shown.String())
	}
}

func TestCLIDetachedClientIsNotAnUncertainMutation(t *testing.T) {
	wire, _ := captureCLIRecords(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	req := controller.Request{ID: "attach-request-123456", Method: "box.attach", Space: "work", Args: []string{"worker"}}
	detached := &controller.ClientDetached{ID: req.ID, Signal: 2, Owned: true}
	err := reportClientDetached(cmd, req, detached)
	err = cliFinishCommand(err, "Completed")
	if !errors.Is(err, detached) || classifyError(err) != exitDetached || !cmd.SilenceErrors {
		t.Fatalf("detach status changed: %v", err)
	}
	records := readCLIRecords(t, wire)
	if len(records) != 1 || prettylog.StructureOf(records[0]).Outcome != "" || !strings.Contains(records[0].Message, "nothing is retried") || !strings.Contains(records[0].Message, "yoloctl box session worker --space work") {
		t.Fatalf("detach guidance changed: %+v", records)
	}
}

func TestCLIErrorReasonSurvivesMessageCap(t *testing.T) {
	var shown bytes.Buffer
	l := prettylog.New(&shown, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal, Limits: prettylog.Limits{Message: 32}})
	restore := logging.SetLogger(l)
	defer restore()
	reason := strings.Repeat("diagnostic ", 200) + "request result must be queried; do not replay"
	renderLoggedCLIError(&controller.OutcomeUnknown{ID: "bounded-message", Cause: errors.New(reason)})
	_ = l.Close()
	if !strings.Contains(shown.String(), reason) || strings.Count(shown.String(), reason) != 1 {
		t.Fatalf("lost or duplicated final guidance: %q", shown.String())
	}
}

func TestCLIOutcomesRespectSelectedLogLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			v, err := logging.ParseLogLevel(level)
			if err != nil {
				t.Fatal(err)
			}
			var shown bytes.Buffer
			l := prettylog.New(&shown, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: v})
			restore := logging.SetLogger(l)
			defer restore()
			_ = cliFinishOutcome(nil, "Completed", logging.Host)
			failure := errors.New("specific failure")
			result := cliFinishOutcome(failure, "unused", logging.Host)
			if !errors.Is(result, failure) {
				t.Fatal("failure identity changed")
			}
			unknown := &controller.OutcomeUnknown{ID: "level-test", Cause: io.EOF}
			result = cliFinishOutcome(unknown, "unused", logging.Controller)
			if !errors.Is(result, unknown) {
				t.Fatal("uncertainty identity changed")
			}
			_ = l.Close()
			if strings.Contains(shown.String(), "Completed") != (level != "error") {
				t.Fatalf("success filtering: %q", shown.String())
			}
			if !strings.Contains(shown.String(), failure.Error()) || !strings.Contains(shown.String(), unknown.Error()) {
				t.Fatalf("final diagnostic hidden: %q", shown.String())
			}
		})
	}
}
