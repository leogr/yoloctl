package session

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/mainlaunch"
	"github.com/leogr/yoloctl/internal/terminal"
	"golang.org/x/crypto/ssh"
)

const (
	// mainSubsystem attaches to the box's canonical main process.
	mainSubsystem = "openshell-main"
	// mainRawInputEnv makes every input byte reach the main process
	// (OpenShell patch 0005); refused unless this attachment owns input.
	mainRawInputEnv = "OPENSHELL_MAIN_RAW_INPUT"
)

// markMainSession sends a privileged endpoint's main-session request on the
// session channel before its exec; a refusal (an endpoint from before) is a
// startup failure.
func markMainSession(s *ssh.Session, mark bool) error {
	if !mark {
		return nil
	}
	ok, err := s.SendRequest(boxendpoint.MainSessionRequest, true, nil)
	if err != nil || !ok {
		return fmt.Errorf("session: the box's endpoint refused the main-session request (ok=%v, %v); restart the box", ok, err)
	}
	return nil
}

// ErrNoMkfifo is directive 61's refusal: the image cannot take a managed
// session through the launcher.
var ErrNoMkfifo = errors.New("the box's image has no mkfifo (install coreutils in the image); a session cannot be started in it")

// handOver gives the box's launcher this session's launch with one SSH exec
// of the fixed hand-over command, without the server's login shell, the
// record on its standard input. It never sends the launch a second time.
// nil: delivered. ErrNoMkfifo, or another error that is not
// ErrHolderConnLost: proven not delivered. ErrHolderConnLost: not known.
func handOver(client *ssh.Client, opts HolderOptions, command []byte) error {
	token, err := mainlaunch.NewToken(command)
	if err != nil {
		return fmt.Errorf("session: launch token: %w", err)
	}
	record, err := mainlaunch.Record(opts.Term, token, command)
	if err != nil {
		return fmt.Errorf("session: launch record: %w", err)
	}
	s, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("%w: the hand-over channel failed: %v", ErrHolderConnLost, err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Setenv(mainlaunch.NoLoginShellEnv, "1"); err != nil {
		return fmt.Errorf("%w: the hand-over request failed: %v", ErrHolderConnLost, err)
	}
	s.Stdin = bytes.NewReader(record)
	runErr := s.Run(mainlaunch.HandoverScript)
	if runErr == nil {
		return nil
	}
	var exit *ssh.ExitError
	if errors.As(runErr, &exit) {
		switch exit.ExitStatus() {
		case mainlaunch.HandoverNoMkfifo:
			return ErrNoMkfifo
		case mainlaunch.HandoverNoFIFO:
			return errors.New("session: the box's main process is not waiting for a launch")
		case mainlaunch.HandoverNameHasToken:
			return errors.New("session: the launch could not be framed for this box's environment; nothing was started")
		}
	}
	return fmt.Errorf("%w: the hand-over did not answer (%v)", ErrHolderConnLost, runErr)
}

// sessionWait maps ssh.Session.Wait's result: a status, or an error when
// the channel ended without one (statusless or transport loss).
func sessionWait(werr error) (int, error) {
	if werr == nil {
		return 0, nil
	}
	var ee *ssh.ExitError
	if errors.As(werr, &ee) {
		return ee.ExitStatus(), nil
	}
	return -1, werr
}

// mainChannel is the session channel attached to the box's main process.
type mainChannel struct {
	ch   ssh.Channel
	reqs <-chan *ssh.Request
}

// openMainChannel opens a session channel, requests the terminal, the raw
// input switch and the openshell-main subsystem, each answered by the server.
func openMainChannel(client *ssh.Client, term string, g terminal.Geometry) (*mainChannel, error) {
	ch, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		return nil, fmt.Errorf("session: open channel: %w", err)
	}
	m := &mainChannel{ch: ch, reqs: reqs}
	fail := func(err error) (*mainChannel, error) {
		_ = ch.Close()
		return nil, err
	}
	if err := requestPTY(ch, term, g); err != nil {
		return fail(fmt.Errorf("session: request pty: %w", err))
	}
	ok, err := ch.SendRequest("env", true, ssh.Marshal(struct{ Name, Value string }{mainRawInputEnv, "1"}))
	if err != nil || !ok {
		return fail(fmt.Errorf("session: the main process's raw input was refused (ok=%v, %v)", ok, err))
	}
	ok, err = ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{mainSubsystem}))
	if err != nil || !ok {
		return fail(fmt.Errorf("session: attach to the box's main process refused (ok=%v, %v)", ok, err))
	}
	return m, nil
}

// signalNumbers are x/crypto/ssh's exit-signal numbers (Session.wait).
var signalNumbers = map[string]int{"ABRT": 6, "ALRM": 14, "FPE": 8, "HUP": 1, "ILL": 4, "INT": 2, "KILL": 9, "PIPE": 13, "QUIT": 3, "SEGV": 11, "TERM": 15}

// wait reads the channel's requests to their end exactly as ssh.Session's
// wait does: the exit status, else 128 plus a known exit signal's number;
// neither is a statusless end.
func (m *mainChannel) wait() (int, error) {
	status, signal := -1, ""
	for msg := range m.reqs {
		switch msg.Type {
		case "exit-status":
			if len(msg.Payload) < 4 {
				return -1, errors.New("ssh: malformed exit-status request")
			}
			status = int(binary.BigEndian.Uint32(msg.Payload))
		case "exit-signal":
			var sig struct {
				Signal     string
				CoreDumped bool
				Error      string
				Lang       string
			}
			if err := ssh.Unmarshal(msg.Payload, &sig); err != nil {
				return -1, err
			}
			signal = sig.Signal
		default:
			if msg.WantReply {
				_ = msg.Reply(false, nil)
			}
		}
	}
	if status == -1 {
		if signal == "" {
			return -1, &ssh.ExitMissingError{}
		}
		status = 128 + signalNumbers[signal]
	}
	return status, nil
}
