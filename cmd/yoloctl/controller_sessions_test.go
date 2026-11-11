package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

func runningSession(name string, build boxstate.SessionBuild, holder string) boxstate.Box {
	return boxstate.Box{Name: name, Session: boxstate.SessionInfo{State: boxstate.SessionRunning, Build: build, Holder: holder}}
}

// inSync is a box.drift fake: every named box needs no recreation.
func inSync(_ context.Context, names []string) ([]boxDriftView, error) {
	view := make([]boxDriftView, 0, len(names))
	for _, name := range names {
		view = append(view, boxDriftView{Name: name, Drift: boxstate.DriftInSync})
	}
	return view, nil
}

// sessionOfferFixture has two sessions of the previous build, one of this
// build, one that could not be asked, one of another build whose holder
// could not be identified, and boxes without a running session. Restarts
// record "name holder".
func sessionOfferFixture(t *testing.T, interactive bool, answer string) (*sessionRestartOffer, *[]string, *bytes.Buffer) {
	var restarted []string
	var errw bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &errw))
	o := &sessionRestartOffer{
		inventory: func(context.Context) ([]boxstate.Box, error) {
			return []boxstate.Box{
				runningSession("alpha", boxstate.SessionBuildOther, "11@a"),
				runningSession("beta", boxstate.SessionBuildCurrent, "12@b"),
				runningSession("gamma", boxstate.SessionBuildOther, "13@g"),
				runningSession("delta", boxstate.SessionBuildUnknown, "14@d"),
				runningSession("epsilon", boxstate.SessionBuildOther, ""),
				{Name: "idle", Session: boxstate.SessionInfo{State: boxstate.SessionNone}},
				{Name: "gone", Session: boxstate.SessionInfo{State: boxstate.SessionLost}},
			}, nil
		},
		chatServer: func(context.Context) (string, error) { return "", nil },
		drift:      inSync,
		restart: func(_ context.Context, name, holder string, startWorkload bool) error {
			if startWorkload {
				holder += " +workload"
			}
			restarted = append(restarted, name+" "+holder)
			return nil
		},
		ensureChat:  func(context.Context) error { return errors.New("no room to ensure") },
		interactive: interactive,
		in:          strings.NewReader(answer),
		errw:        &errw,
	}
	return o, &restarted, &errw
}

// Scripts never restart: the sessions of the previous build are named with
// the exact restart command, unknown ones are named as left running, and
// nobody is asked.
func TestSessionRestartOfferWithoutTerminalOnlyPrints(t *testing.T) {
	o, restarted, errw := sessionOfferFixture(t, false, "y\n")
	if err := offerSessionRestarts(context.Background(), "work", *o, false); err != nil {
		t.Fatal(err)
	}
	out := errw.String()
	if len(*restarted) != 0 || strings.Contains(out, "[y/N]") {
		t.Fatalf("restarted %v without a terminal:\n%s", *restarted, out)
	}
	for _, want := range []string{
		"previous build, which this build cannot attach to: alpha, gamma.",
		"ends its agent's current process",
		"alpha: its conversation is not known here; resume it yourself after the restart\n",
		"Nothing was restarted",
		"  yoloctl box restart alpha --space work --no-attach\n",
		"  yoloctl box restart gamma --space work --no-attach\n",
		"Could not check or identify the agent sessions of delta, epsilon; they are left running.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, name := range []string{"beta", "idle", "gone"} {
		if strings.Contains(out, name) {
			t.Errorf("%s is not a session of another build:\n%s", name, out)
		}
	}
}

// At a terminal the one prompt defaults to No: only an explicit yes restarts,
// in listed order; anything else leaves the sessions and prints the commands.
func TestSessionRestartPromptDefaultsToNo(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n", "maybe\n"} {
		o, restarted, errw := sessionOfferFixture(t, true, answer)
		if err := offerSessionRestarts(context.Background(), "work", *o, false); err != nil {
			t.Fatal(err)
		}
		if len(*restarted) != 0 || !strings.Contains(errw.String(), "Restart 2 boxes now? [y/N]: ") || !strings.Contains(errw.String(), "Left running") {
			t.Fatalf("answer %q restarted %v:\n%s", answer, *restarted, errw)
		}
	}
	o, restarted, errw := sessionOfferFixture(t, true, "yes\n")
	if err := offerSessionRestarts(context.Background(), "work", *o, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*restarted, []string{"alpha 11@a", "gamma 13@g"}) || !strings.Contains(errw.String(), "Restarted 2 boxes on this build. A tracked conversation is requested on the box's next attach and stays unverified until its agent reports it") {
		t.Fatalf("yes restarted %v:\n%s", *restarted, errw)
	}
}

// --restart-sessions is the explicit opt-in: no prompt, terminal or not.
func TestSessionRestartFlagRestartsWithoutAsking(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		o, restarted, errw := sessionOfferFixture(t, interactive, "")
		if err := offerSessionRestarts(context.Background(), "work", *o, true); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(*restarted, []string{"alpha 11@a", "gamma 13@g"}) || strings.Contains(errw.String(), "[y/N]") {
			t.Fatalf("interactive=%v restarted %v:\n%s", interactive, *restarted, errw)
		}
	}
}

// A failed restart stops the sequence and is never retried: the error names
// the boxes not attempted and keeps an unknown outcome inspectable.
func TestSessionRestartStopsAtTheFirstFailure(t *testing.T) {
	o, _, _ := sessionOfferFixture(t, false, "")
	var calls []string
	lost := &controller.OutcomeUnknown{ID: "req1", Cause: errors.New("reply lost")}
	o.restart = func(_ context.Context, name, _ string, _ bool) error { calls = append(calls, name); return lost }
	err := offerSessionRestarts(context.Background(), "work", *o, true)
	var unknown *controller.OutcomeUnknown
	if !errors.As(err, &unknown) || !strings.Contains(err.Error(), "restart box alpha (not attempted: gamma)") || !slices.Equal(calls, []string{"alpha"}) {
		t.Fatalf("calls %v, error %v", calls, err)
	}
}

// Nothing to offer prints nothing. A failed listing restarts nothing: it is
// a warning after the successful update, and an error only when restarts
// were requested.
func TestSessionRestartOfferQuietWhenCurrentAndFailsWithoutInventory(t *testing.T) {
	o, restarted, errw := sessionOfferFixture(t, true, "y\n")
	o.inventory = func(context.Context) ([]boxstate.Box, error) {
		return []boxstate.Box{{Name: "beta", Session: boxstate.SessionInfo{State: boxstate.SessionRunning, Build: boxstate.SessionBuildCurrent}}}, nil
	}
	if err := offerSessionRestarts(context.Background(), "work", *o, true); err != nil || errw.Len() != 0 || len(*restarted) != 0 {
		t.Fatalf("current sessions: %v %q %v", err, errw, *restarted)
	}
	o.inventory = func(context.Context) ([]boxstate.Box, error) { return nil, errors.New("controller unavailable") }
	if err := offerSessionRestarts(context.Background(), "work", *o, true); err == nil || len(*restarted) != 0 {
		t.Fatalf("requested restarts without inventory: %v %v", err, *restarted)
	}
	if err := offerSessionRestarts(context.Background(), "work", *o, false); err != nil || len(*restarted) != 0 ||
		!strings.Contains(errw.String(), "warning: host: controller updated, but its agent sessions could not be listed (controller unavailable); check them with yoloctl box list --space work") {
		t.Fatalf("offer without inventory: %v %v %q", err, *restarted, errw)
	}
}

// Each restart is the ordinary shared `box restart NAME --no-attach` with
// its own request ID, so it keeps the command's lifecycle and receipt, bound
// to the listed holder through the hidden precondition flag.
func TestSessionRestartUsesTheSharedBoxRestart(t *testing.T) {
	a, b := sessionRestartRequest("work", "alpha", "11@a", false), sessionRestartRequest("work", "alpha", "11@a", false)
	if a.ID == "" || a.ID == b.ID || a.Method != "box.restart" || a.Space != "work" || a.View || a.Dash != -1 {
		t.Fatalf("request %+v / %+v", a, b)
	}
	argv, err := controllerCommandArgs(newRootCmd(), a)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv, []string{"box", "restart", "--if-stale-session=11@a", "--no-attach=true", "alpha"}) {
		t.Fatalf("argv %q", argv)
	}
	argv, err = controllerCommandArgs(newRootCmd(), sessionRestartRequest("work", "svc", "15@s", true))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv, []string{"box", "restart", "--if-stale-session=15@s", "--no-attach=true", "--start-workload=true", "svc"}) {
		t.Fatalf("workload argv %q", argv)
	}
	restart, _, _ := newRootCmd().Find([]string{"box", "restart"})
	for _, name := range []string{staleSessionFlag, startWorkloadFlag} {
		if f := restart.Flags().Lookup(name); f == nil || !f.Hidden {
			t.Fatalf("the %s flag is missing or visible", name)
		}
	}
	ensure, err := chatEnsureRequest("work")
	if err != nil || ensure.Method != "chat.ensure" || ensure.Space != "work" || ensure.ID == "" || len(ensure.Flags) != 0 || len(ensure.Args) != 0 ||
		string(ensure.Payload) != `{"default_human":"`+defaultChatHuman()+`"}` && defaultChatHuman() != "" {
		t.Fatalf("chat ensure request %+v %s %v", ensure, ensure.Payload, err)
	}
}

// qa B2: the answer can take a while. A box whose session another restart
// replaced during the prompt, or before its turn in the batch, is skipped
// and left running; the others still restart. Inventory read 0 is the
// listing; read n+1 precedes the n-th restart.
func TestSessionRestartSkipsSessionsReplacedAfterListing(t *testing.T) {
	for _, c := range []struct {
		name       string
		gammaFirst bool
		replaceAt  int // alpha's session is replaced from this read on
		want       []string
	}{
		{"during the prompt", false, 1, []string{"gamma 13@g"}},
		{"before its turn", true, 2, []string{"gamma 13@g"}},
		{"after it restarted", false, 2, []string{"alpha 11@a", "gamma 13@g"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			o, restarted, errw := sessionOfferFixture(t, true, "yes\n")
			reads := 0
			base := o.inventory
			o.inventory = func(ctx context.Context) ([]boxstate.Box, error) {
				boxes, _ := base(ctx)
				if reads >= c.replaceAt {
					boxes[0] = runningSession("alpha", boxstate.SessionBuildCurrent, "21@a")
				}
				if c.gammaFirst {
					boxes[0], boxes[2] = boxes[2], boxes[0]
				}
				reads++
				return boxes, nil
			}
			if err := offerSessionRestarts(context.Background(), "work", *o, false); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*restarted, c.want) {
				t.Fatalf("restarted %v, want %v:\n%s", *restarted, c.want, errw)
			}
			skipped := strings.Contains(errw.String(), "Skipped box alpha: its session changed since it was listed; left running.")
			if skipped != (len(c.want) == 1) {
				t.Fatalf("skip report %v:\n%s", skipped, errw)
			}
		})
	}
}

// qa B2, reproduced as reported: the session becomes current while the
// person answers, so nothing is restarted.
func TestSessionRestartPromptDoesNotRestartAReplacement(t *testing.T) {
	build := boxstate.SessionBuildOther
	restarts := 0
	answer := strings.NewReader("yes\n")
	var out bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &out))
	o := sessionRestartOffer{
		inventory: func(context.Context) ([]boxstate.Box, error) {
			return []boxstate.Box{runningSession("alpha", build, "11@a")}, nil
		},
		interactive: true,
		in: readerFunc(func(p []byte) (int, error) {
			build = boxstate.SessionBuildCurrent // another managed restart finished
			return answer.Read(p)
		}),
		errw:       &out,
		restart:    func(context.Context, string, string, bool) error { restarts++; return nil },
		chatServer: func(context.Context) (string, error) { return "", nil },
		drift:      inSync,
	}
	if err := offerSessionRestarts(context.Background(), "work", o, false); err != nil {
		t.Fatal(err)
	}
	if restarts != 0 {
		t.Fatalf("restarted a now-current replacement:\n%s", out.String())
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// The controller's precondition stops only the listed holder while it still
// answers as another build; a stopped, replaced, current or unknown session
// is refused with nothing restarted.
func TestStaleSessionPreconditionRefusesAnyChange(t *testing.T) {
	if err := checkStaleSession("alpha", "11@a", true, boxstate.SessionBuildOther, "11@a"); err != nil {
		t.Fatalf("listed session refused: %v", err)
	}
	for _, c := range []struct {
		running bool
		build   boxstate.SessionBuild
		holder  string
	}{
		{false, boxstate.SessionBuildUnknown, ""},
		{true, boxstate.SessionBuildOther, "21@a"},
		{true, boxstate.SessionBuildOther, ""},
		{true, boxstate.SessionBuildCurrent, "11@a"},
		{true, boxstate.SessionBuildUnknown, "11@a"},
	} {
		err := checkStaleSession("alpha", "11@a", c.running, c.build, c.holder)
		if !errors.Is(err, errStaleSessionChanged) || !strings.Contains(err.Error(), "box alpha:") {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// The precondition runs before anything else in box restart: refused
// outside the controller, it stops no holder, port or container.
func TestBoxRestartChecksThePreconditionBeforeTeardown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	calls := installStopAgentboxRuntimeHooks(t, nil)
	cmd := newAgentboxRestartCmd()
	cmd.SetArgs([]string{"alpha", "--no-attach", "--" + staleSessionFlag + "=11@a"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "checked only by the space controller") || len(*calls) != 0 {
		t.Fatalf("error %v, teardown calls %v", err, *calls)
	}
}

// qa B3: /dev/null is a character device, not a terminal; it gets the
// print-only branch, never the prompt.
func TestDevNullIsNotAnInteractivePrompt(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if isInteractive(f) || isTerminalWriter(f) {
		t.Fatal("/dev/null counted as a terminal")
	}
	o, restarted, out := sessionOfferFixture(t, false, "")
	o.in, o.interactive = f, isInteractive(f)
	if err := offerSessionRestarts(context.Background(), "work", *o, false); err != nil {
		t.Fatal(err)
	}
	if len(*restarted) > 0 || strings.Contains(out.String(), "[y/N]") || !strings.Contains(out.String(), "Nothing was restarted") {
		t.Fatalf("/dev/null prompted or restarted:\n%s", out.String())
	}
}

// The opt-in flag exists only beside --update-controller and is refused
// before any lifecycle lock or bootstrap read. The test runs as the host
// (no operator endpoint), also inside an operator box.
func TestRestartSessionsRequiresUpdateController(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := newClientRootCmd()
	root.SetArgs([]string{"space", "start", "nowhere", "--restart-sessions"})
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&stderr)
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--restart-sessions requires --update-controller") {
		t.Fatalf("got %v", err)
	}
	start, _, _ := newClientRootCmd().Find([]string{"space", "start"})
	if start.Flags().Lookup("restart-sessions") == nil || start.Flags().Lookup("update-controller") == nil {
		t.Fatal("space start lacks the update flags")
	}
}

// workloadOfferFixture lists the managed chat server, a box running another
// session_command workload and an agent box, all on the previous build. Its
// log records each restart and chat ensure in order.
func workloadOfferFixture(t *testing.T, interactive bool) (*sessionRestartOffer, *[]string, *bytes.Buffer) {
	var log []string
	var errw bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &errw))
	declared := func(name, holder string, command []string) boxstate.Box {
		b := runningSession(name, boxstate.SessionBuildOther, holder)
		b.Config = &agentbox.Config{Name: name, Agent: "none", Account: "none", SessionCommand: command}
		return b
	}
	o := &sessionRestartOffer{
		inventory: func(context.Context) ([]boxstate.Box, error) {
			return []boxstate.Box{
				runningSession("alpha", boxstate.SessionBuildOther, "11@a"),
				declared("chat", "16@c", agentbox.ChatServerCommand()),
				declared("svc", "17@s", []string{"/usr/bin/svc", "--serve"}),
			}, nil
		},
		chatServer: func(context.Context) (string, error) { return "chat", nil },
		drift:      inSync,
		restart: func(_ context.Context, name, _ string, startWorkload bool) error {
			log = append(log, fmt.Sprintf("restart %s workload=%v", name, startWorkload))
			return nil
		},
		ensureChat:  func(context.Context) error { log = append(log, "chat ensure"); return nil },
		interactive: interactive,
		in:          strings.NewReader(""),
		errw:        &errw,
	}
	return o, &log, &errw
}

// r4: a restarted managed workload is running again when the offer returns.
// The chat server goes through chat ensure right after its restart, which
// confirms readiness and membership; another session_command workload is
// started again by its own restart; an agent box keeps no session.
func TestSessionRestartBringsManagedWorkloadsBack(t *testing.T) {
	o, log, errw := workloadOfferFixture(t, false)
	if err := offerSessionRestarts(context.Background(), "work", *o, true); err != nil {
		t.Fatalf("%v\n%s", err, errw)
	}
	want := []string{"restart alpha workload=false", "restart chat workload=false", "chat ensure", "restart svc workload=true"}
	if !slices.Equal(*log, want) {
		t.Fatalf("log %v, want %v", *log, want)
	}
	for _, line := range []string{
		"chat is the managed chat server: restarting it restarts the room on this build, and every connected chat client must reconnect.",
		"svc runs a managed workload, which is started again after its restart.",
		"The chat room was restarted on this build; every connected chat client must reconnect.",
		"Restarted 3 boxes on this build.",
	} {
		if !strings.Contains(errw.String(), line) {
			t.Errorf("missing %q in:\n%s", line, errw)
		}
	}
}

// Someone restarting by hand learns the recovery step for each workload:
// both the print-only and the declined listings name it.
func TestSessionRestartListingsNameWorkloadRecovery(t *testing.T) {
	for _, c := range []struct {
		interactive bool
		answer      string
		lead        string
	}{{false, "", "Nothing was restarted. To restart them:"}, {true, "\n", "Left running. To restart them later:"}} {
		o, log, errw := workloadOfferFixture(t, c.interactive)
		o.in = strings.NewReader(c.answer)
		if err := offerSessionRestarts(context.Background(), "work", *o, false); err != nil || len(*log) != 0 {
			t.Fatalf("%v %v", err, *log)
		}
		want := c.lead + `
  yoloctl box restart alpha --space work --no-attach
  yoloctl box restart chat --space work --no-attach
  yoloctl chat ensure --space work   # then start the chat room again on this build
  yoloctl box restart svc --space work --no-attach
  yoloctl box attach svc --space work   # then start its managed workload again, and detach
`
		if !strings.Contains(errw.String(), want) {
			t.Fatalf("listing:\n%s\nwant:\n%s", errw, want)
		}
	}
}

// A chat ensure that fails after the server restarted is reported with the
// exact recovery command and an error (a nonzero exit); the boxes after it
// are not attempted.
func TestSessionRestartReportsAFailedChatEnsure(t *testing.T) {
	o, log, errw := workloadOfferFixture(t, false)
	o.ensureChat = func(context.Context) error {
		*log = append(*log, "chat ensure")
		return errors.New("readiness unconfirmed")
	}
	err := offerSessionRestarts(context.Background(), "work", *o, true)
	if err == nil || !strings.Contains(err.Error(), "restart box chat (not attempted: svc): start the chat room again: readiness unconfirmed") {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(errw.String(), "The chat room may be down. Recover it with:\n  yoloctl chat ensure --space work\n") ||
		strings.Contains(errw.String(), "The chat room was restarted") {
		t.Fatalf("report:\n%s", errw)
	}
	if want := []string{"restart alpha workload=false", "restart chat workload=false", "chat ensure"}; !slices.Equal(*log, want) {
		t.Fatalf("log %v", *log)
	}
}

// A failed workload restart names its recovery too.
func TestSessionRestartReportsAFailedWorkloadRestart(t *testing.T) {
	o, _, errw := workloadOfferFixture(t, false)
	o.restart = func(_ context.Context, name, _ string, _ bool) error {
		if name == "svc" {
			return errors.New("holder did not open its socket")
		}
		return nil
	}
	if err := offerSessionRestarts(context.Background(), "work", *o, true); err == nil ||
		!strings.Contains(errw.String(), "The managed workload of svc may be down. Recover it with:\n  yoloctl box attach svc --space work\n") {
		t.Fatalf("error %v:\n%s", err, errw)
	}
}

// Without the room, a declared session_command cannot be classified: such
// boxes are left running, while agent boxes are still offered.
func TestSessionRestartLeavesWorkloadsRunningWithoutTheRoom(t *testing.T) {
	o, log, errw := workloadOfferFixture(t, false)
	o.chatServer = func(context.Context) (string, error) { return "", errors.New("chat status unavailable") }
	if err := offerSessionRestarts(context.Background(), "work", *o, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*log, []string{"restart alpha workload=false"}) ||
		!strings.Contains(errw.String(), "Could not check or identify the agent sessions of chat, svc; they are left running.") ||
		!strings.Contains(errw.String(), "warning: host: the managed chat room could not be read (chat status unavailable)") {
		t.Fatalf("log %v:\n%s", *log, errw)
	}
}

// --start-workload is refused before any teardown unless the box declares a
// session_command other than the managed chat server's.
func TestStartWorkloadNeedsANonChatSessionCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	save := func(name string, command []string) {
		t.Helper()
		cfg := &agentbox.Config{Image: "img:1", SchemaVersion: agentbox.SchemaVersion, Name: name, Space: "default", Agent: "none", Account: "none",
			Workdir: agentbox.NewEphemeralWorkdir(name), SessionCommand: command}
		if err := cfg.Save(); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}
	save("shell", nil)
	save("svc", []string{"/usr/bin/svc"})
	if err := requireRestartableWorkload("default", "svc"); err != nil {
		t.Fatalf("workload refused: %v", err)
	}
	if err := requireRestartableWorkload("default", "shell"); err == nil || !strings.Contains(err.Error(), "declares no session_command") {
		t.Fatalf("shell: %v", err)
	}
	if err := requireRestartableWorkload("default", "missing"); err == nil {
		t.Fatal("an undeclared box was accepted")
	}
	chat := &agentbox.Config{Name: "chat", SessionCommand: agentbox.ChatServerCommand()}
	if err := restartableWorkload(chat, "default"); err == nil || !strings.Contains(err.Error(), "start it again with yoloctl chat ensure --space default") {
		t.Fatalf("chat server: %v", err)
	}
	calls := installStopAgentboxRuntimeHooks(t, nil)
	cmd := newAgentboxRestartCmd()
	cmd.SetArgs([]string{"shell", "--no-attach", "--" + startWorkloadFlag})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || len(*calls) != 0 {
		t.Fatalf("refused restart tore down: %v %v", err, *calls)
	}
}

// The restarted workload starts through the shared session start (the one
// attach uses), and a failure names the manual recovery.
func TestRestartedWorkloadStartsThroughTheSharedSessionStart(t *testing.T) {
	old := startWorkloadSession
	t.Cleanup(func() { startWorkloadSession = old })
	var started []string
	startWorkloadSession = func(_ context.Context, _ *container.Client, space, name string, _ io.Writer, noAutoupdate bool, _ *bool, _ sessionStartOptions) (string, error) {
		started = append(started, space+"/"+name+fmt.Sprint(noAutoupdate))
		return "", nil
	}
	var errw bytes.Buffer
	t.Cleanup(bindCLITestLogger(t, &errw))
	if err := startRestartedWorkload(context.Background(), nil, "work", "svc", &errw); err != nil || !slices.Equal(started, []string{"work/svctrue"}) ||
		!strings.Contains(errw.String(), "Started the managed workload of box svc again on this build.") {
		t.Fatalf("%v %v %q", err, started, errw.String())
	}
	startWorkloadSession = func(context.Context, *container.Client, string, string, io.Writer, bool, *bool, sessionStartOptions) (string, error) {
		return "", errors.New("no socket")
	}
	if err := startRestartedWorkload(context.Background(), nil, "work", "svc", &errw); err == nil || !strings.Contains(err.Error(), "recover with yoloctl box attach svc --space work") {
		t.Fatalf("failure: %v", err)
	}
}

// r5 (qa R4-B1): an ensure that exits successfully is accepted only when its
// final observation shows the server ready. Otherwise its pending reasons
// become the error; pending participant changes behind a ready server are
// shown.
func TestChatEnsureOutcomeRequiresAReadyServer(t *testing.T) {
	room := &managedchat.Room{Server: managedchat.Box{Name: "chat"}}
	for _, c := range []struct {
		status managedchat.Status
		err    string
		shown  string
	}{
		{managedchat.Status{Room: room, ServerReady: true, RuntimeApplied: true}, "", ""},
		{managedchat.Status{Room: room, ServerReady: true, Pending: []string{"member coder binds"}}, "", "The chat server is ready; still pending: member coder binds"},
		{managedchat.Status{Room: room, Pending: []string{"managed server holder is not active"}}, "chat ensure reported the server not ready: managed server holder is not active", ""},
		{managedchat.Status{Room: room, RuntimeApplied: true}, "chat ensure reported the server not ready", ""},
		{managedchat.Status{ServerReady: true}, "chat ensure reported no managed room", ""},
	} {
		var errw bytes.Buffer
		t.Cleanup(bindCLITestLogger(t, &errw))
		err := chatEnsureOutcome(c.status, &errw)
		if (err == nil) != (c.err == "") || err != nil && err.Error() != c.err || !strings.Contains(errw.String(), c.shown) || c.shown == "" && errw.Len() != 0 {
			t.Errorf("%+v: error %v, shown %q", c.status, err, errw.String())
		}
	}
}

// qa R4-B1, through the production ensure closure and real controller
// framing: a fake limactl replays a hello, the typed ensure status and a
// successful exit. A ready status is success; a pending one stops the
// sequence with the recovery command, before svc, and is never announced.
func TestSessionRestartTreatsAPendingChatEnsureAsFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	frames := filepath.Join(dir, "frames")
	script := "#!/bin/sh\ncat \"$YOLOCTL_TEST_FRAMES\"\nexec cat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("YOLOCTL_TEST_FRAMES", frames)
	boot := &spaceconfig.Bootstrap{Name: "work", Bundle: "fixture", Generation: "fixture"}
	room := &managedchat.Room{Server: managedchat.Box{Name: "chat"}}
	for _, ready := range []bool{true, false} {
		t.Run(fmt.Sprint("ready=", ready), func(t *testing.T) {
			status := managedchat.Status{Room: room, ServerReady: ready, RuntimeApplied: ready}
			if !ready {
				status.Pending = []string{"managed server holder is not active"}
			}
			raw, _ := json.Marshal(status)
			var buf bytes.Buffer
			wire := controller.NewWire(nil, &buf)
			for _, f := range []controller.Frame{
				{Type: "hello", Hello: &controller.Hello{Protocol: controller.ProtocolVersion, Bundle: boot.Bundle, Space: boot.Name, Generation: boot.Generation}},
				{Type: "stdout", Data: raw},
				{Type: "exit"},
			} {
				if err := wire.Write(f); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(frames, buf.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := &cobra.Command{}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			o, log, out := workloadOfferFixture(t, false)
			o.ensureChat = hostSessionRestartOffer(cmd, boot).ensureChat
			err := offerSessionRestarts(context.Background(), "work", *o, true)
			if ready {
				if err != nil || !slices.Contains(*log, "restart svc workload=true") || !strings.Contains(out.String(), "The chat room was restarted") {
					t.Fatalf("ready ensure: %v %v\n%s", err, *log, out)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "managed server holder is not active") ||
				strings.Contains(out.String(), "The chat room was restarted") ||
				!strings.Contains(out.String(), "The chat room may be down. Recover it with:\n  yoloctl chat ensure --space work\n") ||
				slices.Contains(*log, "restart svc workload=true") {
				t.Fatalf("pending ensure: err=%v log=%v\n%s", err, *log, out)
			}
		})
	}
}

// qa: a lost reply to the chat server's restart or to its ensure keeps the
// unknown outcome, repeats nothing, attempts no later box and still names
// the recovery command; after an uncertain restart no ensure is sent.
func TestSessionRestartNeverReplaysALostChatOutcome(t *testing.T) {
	for _, phase := range []string{"restart", "ensure"} {
		t.Run(phase, func(t *testing.T) {
			o, log, out := workloadOfferFixture(t, false)
			lost := &controller.OutcomeUnknown{ID: "lost-chat-" + phase, Cause: errors.New("reply lost")}
			orig := o.restart
			o.restart = func(ctx context.Context, name, holder string, workload bool) error {
				err := orig(ctx, name, holder, workload)
				if name == "chat" && phase == "restart" {
					return lost
				}
				return err
			}
			ensured := 0
			o.ensureChat = func(context.Context) error { ensured++; return lost }
			err := offerSessionRestarts(context.Background(), "work", *o, true)
			var unknown *controller.OutcomeUnknown
			want := 0
			if phase == "ensure" {
				want = 1
			}
			if !errors.As(err, &unknown) || unknown != lost || ensured != want ||
				!slices.Equal(*log, []string{"restart alpha workload=false", "restart chat workload=false"}) ||
				!strings.Contains(out.String(), "yoloctl chat ensure --space work") {
				t.Fatalf("lost %s: err=%v ensured=%d log=%v\n%s", phase, err, ensured, *log, out)
			}
		})
	}
}

// qa: the real restart command refuses --start-workload for the chat
// server's own command before any teardown, naming chat ensure.
func TestStartWorkloadRefusesTheChatServerBeforeTeardown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &agentbox.Config{Image: "img:1", SchemaVersion: agentbox.SchemaVersion, Name: "chat", Space: "default", Agent: "none", Account: "none",
		Workdir: agentbox.NewEphemeralWorkdir("chat"), SessionCommand: agentbox.ChatServerCommand()}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	calls := installStopAgentboxRuntimeHooks(t, nil)
	cmd := newAgentboxRestartCmd()
	cmd.SetArgs([]string{"chat", "--no-attach", "--" + startWorkloadFlag})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "yoloctl chat ensure --space default") || len(*calls) != 0 {
		t.Fatalf("err=%v teardown=%v", err, *calls)
	}
}
