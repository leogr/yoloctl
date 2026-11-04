//go:build linux

package vmanchor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Bound subprocess diagnostics without making its stderr writer fail or
// retaining private input. Helpers print only fixture stages/kernel errors.
type workerFixtureDiagnostics struct{ raw []byte }

func (d *workerFixtureDiagnostics) Write(raw []byte) (int, error) {
	n := len(raw)
	remaining := 4096 - len(d.raw)
	if remaining > 0 {
		if len(raw) > remaining {
			raw = raw[:remaining]
		}
		d.raw = append(d.raw, raw...)
	}
	return n, nil
}

func workerFixtureDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func workerFixtureRequest(view, phase bool) WorkerHandoffRequest {
	r := WorkerHandoffRequest{ExecutionID: "original-execution", Method: "box.destroy", AssociationDigest: workerFixtureDigest([]byte("original-owner/receipt/storage/scope")), InputDigest: workerFixtureDigest([]byte(`{}`))}
	next := 5
	if view {
		r.ViewFD = next
		next++
	}
	if phase {
		r.PhaseFD = next
		next++
	}
	r.NamespaceFD, r.BootstrapFD = next, next+1
	return r
}

func TestWorkerHandoffRequestSlotsAndClosedFrame(t *testing.T) {
	for _, view := range []bool{false, true} {
		for _, phase := range []bool{false, true} {
			r := workerFixtureRequest(view, phase)
			if err := r.validate(); err != nil {
				t.Fatal(err)
			}
			bad := r
			bad.BootstrapFD = bad.NamespaceFD
			if err := bad.validate(); err == nil {
				t.Fatal("duplicate slots accepted")
			}
			if phase {
				bad = r
				bad.PhaseFD = bad.NamespaceFD
				if err := bad.validate(); err == nil {
					t.Fatal("namespace before phase accepted")
				}
			}
		}
	}
	frame := workerHandoffFrame{Version: 1, Transfer: strings.Repeat("a", 32), ControllerLaunch: strings.Repeat("b", 32), Boot: "original", Request: workerFixtureRequest(false, false)}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeWorkerHandoff(raw); err != nil || got != frame {
		t.Fatalf("canonical frame: %+v %v", got, err)
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"version":1`, `"version":0,"Version":1`, 1)),
		[]byte(strings.Replace(string(raw), `"version":1`, `"Version":0,"version":1`, 1)),
		[]byte(strings.Replace(string(raw), `"pid":0`, `"PID":0`, 1)),
		[]byte(strings.Replace(string(raw), `"input_digest":`, `"Input_Digest":`, 1)),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
		make([]byte, launchFrameLimit+1),
	} {
		if got, err := decodeWorkerHandoff(bad); err == nil || got != (workerHandoffFrame{}) {
			t.Fatal("noncanonical frame accepted or partial result exposed")
		}
	}
}

func TestWorkerHandoffUnavailableInputUntouched(t *testing.T) {
	r := workerFixtureRequest(false, false)
	r.InputDigest = workerFixtureDigest([]byte(`{"original":true}`))
	if w, claimed, err := ConsumeWorkerHandoff(r, []byte(`{}`)); w != nil || claimed || err == nil {
		t.Fatal("wrong input digest claimed handles")
	}
	r = workerFixtureRequest(false, false)
	if w, claimed, err := ConsumeWorkerHandoff(r, []byte(`{}`)); w != nil || claimed || err == nil {
		t.Fatal("unrelated absent pair claimed")
	}
	var a *LaunchAnchor
	if tr, err := a.PrepareWorkerHandoff(r, WorkerHandoffFiles{}); tr != nil || err == nil {
		t.Fatal("nil original source created handoff")
	}
	var w *WorkerHandoff
	if w.Check() == nil {
		t.Fatal("nil handoff accepted")
	}
	if ns, err := w.DuplicatePIDNamespace(); ns != nil || err == nil {
		t.Fatal("nil handoff duplicated namespace")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// These subprocess fixtures bootstrap the controller through the existing
// private test-parent launch receiver. They are project continuity mechanics,
// NOT original PID1/VM provenance, receipt admission or actual RunWorker tests.
func TestWorkerHandoffOriginalChild(t *testing.T) {
	for _, mode := range []string{"plain", "view", "phase", "view-phase", "association", "input", "indices", "payload", "checkpoint", "creator", "sender", "stale-child", "closed-source"} {
		t.Run(mode, func(t *testing.T) {
			s := fixtureSource(t)
			tr, err := s.PrepareLaunch()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Close() }()
			cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerHandoffControllerChild$")
			cmd.Env = append(os.Environ(), "YOLOCTL_WORKER_HANDOFF_CONTROLLER="+mode)
			cmd.ExtraFiles = tr.ChildFiles()
			if mode == "creator" {
				foreign, e := duplicate(tr.child)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = foreign.Close() }()
				cmd.ExtraFiles = append(cmd.ExtraFiles, foreign)
			}
			var diagnostic workerFixtureDiagnostics
			cmd.Stdout, cmd.Stderr = io.Discard, &diagnostic
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := tr.PublishStarted(cmd.Process); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("original controller/worker %s: %v; diagnostics: %s", mode, err, diagnostic.raw)
			}
			if mode == "creator" {
				t.Logf("creator publication/child continuity: %s", diagnostic.raw)
			}
		})
	}
}

type workerFixtureInput struct {
	Request WorkerHandoffRequest `json:"request"`
	Mode    string               `json:"mode"`
}

func workerFixtureSocket() (*os.File, *os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(pair[0]), "fixture-parent-checkpoint"), os.NewFile(uintptr(pair[1]), "fixture-child-checkpoint"), nil
}

func TestWorkerHandoffControllerChild(t *testing.T) {
	mode := os.Getenv("YOLOCTL_WORKER_HANDOFF_CONTROLLER")
	if mode == "" {
		return
	}
	a, claimed, err := consumeInheritedLaunch(3, 4, os.Getppid(), 5*time.Second)
	if err != nil || !claimed || a == nil {
		os.Exit(40)
	}
	defer func() { _ = a.Close() }()
	payload, payloadWrite, err := os.Pipe()
	if err != nil {
		os.Exit(41)
	}
	originalPayloadWrite := payloadWrite
	defer func() { _ = payload.Close(); _ = payloadWrite.Close(); _ = originalPayloadWrite.Close() }()
	checkpointParent, checkpoint, err := workerFixtureSocket()
	if err != nil {
		os.Exit(42)
	}
	defer func() { _ = checkpointParent.Close(); _ = checkpoint.Close() }()
	view := mode == "view" || mode == "view-phase" || workerCheckpointFixtureMode(mode)
	phase := mode == "phase" || mode == "view-phase"
	request := workerFixtureRequest(view, phase)
	files := WorkerHandoffFiles{Payload: payload, Checkpoint: checkpoint}
	extra := []*os.File{payload, checkpoint}
	var checkpointControl, checkpointControlChild *os.File
	if view {
		left, right, err := workerCheckpointFixtureViewPair(mode)
		if err != nil {
			os.Exit(43)
		}
		defer func() { _ = left.Close(); _ = right.Close() }()
		files.View = right
		extra = append(extra, right)
		if workerCheckpointFixtureMode(mode) {
			checkpointControl, checkpointControlChild = left, right
		}
	}
	if phase {
		left, right, err := os.Pipe()
		if err != nil {
			os.Exit(44)
		}
		defer func() { _ = left.Close(); _ = right.Close() }()
		files.Phase = right
		extra = append(extra, right)
	}
	// InputDigest is separately derived over canonical input, never self-hashed.
	wireRequest := request
	wireRequest.InputDigest = ""
	if mode == "indices" {
		wireRequest.NamespaceFD = wireRequest.BootstrapFD
	}
	input, err := json.Marshal(workerFixtureInput{wireRequest, mode})
	if err != nil {
		os.Exit(45)
	}
	request.InputDigest = workerFixtureDigest(input)
	tr, err := a.PrepareWorkerHandoff(request, files)
	if err != nil {
		os.Exit(46)
	}
	defer func() { _ = tr.Close() }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerHandoffWorkerChild$")
	cmd.Env = append(os.Environ(), "YOLOCTL_WORKER_HANDOFF_WORKER=1")
	if mode == "payload" {
		other, otherWrite, err := os.Pipe()
		if err != nil {
			os.Exit(47)
		}
		defer func() { _ = other.Close(); _ = otherWrite.Close() }()
		extra[0] = other
		payloadWrite = otherWrite
	}
	if mode == "checkpoint" {
		otherParent, otherChild, e := workerFixtureSocket()
		if e != nil {
			os.Exit(76)
		}
		defer func() { _ = otherParent.Close(); _ = otherChild.Close() }()
		extra[1] = otherChild
		// The original mandatory framing owner follows the actual child end;
		// producer's retained descriptor binding still names the original one.
		checkpointParent = otherParent
	}
	cmd.ExtraFiles = append(extra, tr.ChildFiles()...)
	if mode == "creator" {
		foreign := os.NewFile(5, "fixture-foreign-creator-bootstrap")
		defer func() { _ = foreign.Close() }()
		cmd.ExtraFiles[len(cmd.ExtraFiles)-1] = foreign
	}
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	if mode == "closed-source" {
		if err := a.Close(); err != nil {
			os.Exit(48)
		}
	}
	if err := cmd.Start(); err != nil {
		os.Exit(49)
	}
	var publishErr error
	if mode == "sender" || mode == "stale-child" {
		pin, e := pinProcess(a.root, cmd.Process.Pid)
		if e != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			os.Exit(77)
		}
		record := workerHandoffFrame{1, tr.transfer, a.boot, a.launch, a.child.identity, pin.identity, tr.request, tr.descriptors}
		_ = pin.close()
		if mode == "stale-child" {
			record.Worker.Start++
		}
		frame, e := json.Marshal(record)
		if e != nil {
			os.Exit(78)
		}
		if mode == "sender" {
			sender := exec.Command(os.Args[0], "-test.run=^TestWorkerHandoffSenderChild$")
			sender.Env = append(os.Environ(), "YOLOCTL_WORKER_HANDOFF_SENDER=1")
			sender.ExtraFiles = []*os.File{tr.parent}
			sender.Stdin = strings.NewReader(string(frame))
			publishErr = sender.Run()
		} else {
			_, publishErr = unix.SendmsgN(int(tr.parent.Fd()), frame, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		}
	} else {
		publishErr = tr.PublishStarted(cmd.Process)
	}
	// The foreign-creator child deliberately inherits another bootstrap
	// endpoint. The original transfer's child endpoint has NO receiver after
	// PublishStarted closes its parent-held child copy, so its one send must
	// fail EPIPE/ECONNRESET. This is optional proof loss; continue the SAME
	// actual worker/checkpoint/soleWait and require its raw foreign refusal.
	expectedPublicationFailure := mode == "closed-source" || mode == "creator"
	if expectedPublicationFailure != (publishErr != nil) || (mode == "creator" && !errors.Is(publishErr, unix.EPIPE) && !errors.Is(publishErr, unix.ECONNRESET)) {
		fmt.Fprintf(os.Stderr, "worker fixture publication mode=%s err=%v expectedFailure=%t\n", mode, publishErr, expectedPublicationFailure)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(50)
	}
	if mode == "creator" {
		fmt.Fprintf(os.Stderr, "worker fixture creator original publication: %v; retaining actual child for original checkpoint/Wait\n", publishErr)
	}
	if _, err := payloadWrite.Write(input); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(51)
	}
	_ = payloadWrite.Close()
	if workerCheckpointFixtureMode(mode) {
		// These are only the parent's retained child-side copies. The actual
		// worker owns its original fd4/view; close copies before asserting EOF.
		var closeErr error
		if checkpointControlChild == nil {
			closeErr = errors.New("checkpoint cancellation fixture lacks original child view")
		} else {
			closeErr = errors.Join(checkpoint.Close(), checkpointControlChild.Close())
		}
		if closeErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			fmt.Fprintln(os.Stderr, "checkpoint fixture copies:", closeErr)
			os.Exit(81)
		}
		if err := serveWorkerCheckpointCancellationFixture(checkpointParent, checkpointControl, mode); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			fmt.Fprintln(os.Stderr, "checkpoint fixture parent:", err)
			os.Exit(82)
		}
		if err := cmd.Wait(); err != nil {
			fmt.Fprintln(os.Stderr, "checkpoint fixture original Wait:", err)
			os.Exit(83)
		}
		if tr.Check() == nil {
			os.Exit(84)
		}
		return
	}
	var ready [1]byte
	if _, err := io.ReadFull(checkpointParent, ready[:]); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(52)
	}
	if mode == "plain" || mode == "view" || mode == "phase" || mode == "view-phase" {
		if ready[0] != 1 || tr.Check() != nil {
			os.Exit(53)
		}
	} else if ready[0] != 2 {
		os.Exit(54)
	}
	if _, err := checkpointParent.Write([]byte{1}); err != nil {
		os.Exit(55)
	}
	if err := cmd.Wait(); err != nil {
		os.Exit(56)
	}
	if tr.Check() == nil {
		os.Exit(57)
	}
}

func TestWorkerHandoffSenderChild(t *testing.T) {
	if os.Getenv("YOLOCTL_WORKER_HANDOFF_SENDER") != "1" {
		return
	}
	frame, err := io.ReadAll(io.LimitReader(os.Stdin, launchFrameLimit+1))
	if err != nil || len(frame) > launchFrameLimit {
		os.Exit(79)
	}
	if _, err := unix.SendmsgN(3, frame, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); err != nil {
		os.Exit(80)
	}
	os.Exit(0)
}

func TestWorkerHandoffWorkerChild(t *testing.T) {
	if os.Getenv("YOLOCTL_WORKER_HANDOFF_WORKER") != "1" {
		return
	}
	// Actual early mandatory ownership precedes the optional raw pair. Fixture
	// code uses raw fcntl before os.File wrappers just as the production seam must.
	for _, fd := range []int{3, 4} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			os.Exit(60)
		}
	}
	payload := os.NewFile(3, "fixture-original-payload")
	checkpoint := os.NewFile(4, "fixture-original-checkpoint")
	raw, err := io.ReadAll(io.LimitReader(payload, (4<<20)+1))
	if err != nil {
		os.Exit(61)
	}
	var input workerFixtureInput
	if err := json.Unmarshal(raw, &input); err != nil {
		os.Exit(62)
	}
	request := input.Request
	request.InputDigest = workerFixtureDigest(raw)
	if input.Mode == "association" {
		request.AssociationDigest = strings.Repeat("0", 64)
	}
	if input.Mode == "input" {
		request.InputDigest = strings.Repeat("0", 64)
	}
	budget := 5 * time.Second
	if input.Mode == "closed-source" {
		budget = 50 * time.Millisecond
	}
	w, claimed, err := consumeWorkerHandoff(request, raw, budget)
	if workerCheckpointFixtureMode(input.Mode) {
		if err != nil || !claimed || w == nil || w.Check() != nil {
			if claimed && w != nil {
				_ = w.Close()
			}
			fmt.Fprintln(os.Stderr, "checkpoint fixture genuine intake:", err)
			os.Exit(85)
		}
		if err := runWorkerCheckpointCancellationFixture(w, checkpoint, request, input.Mode); err != nil {
			fmt.Fprintln(os.Stderr, "checkpoint fixture genuine worker:", err)
			os.Exit(86)
		}
		os.Exit(0)
	}
	ready := byte(1)
	good := input.Mode == "plain" || input.Mode == "view" || input.Mode == "phase" || input.Mode == "view-phase"
	if good {
		if err != nil || !claimed || w == nil || w.Check() != nil {
			os.Exit(63)
		}
		ns, e := w.DuplicatePIDNamespace()
		if e != nil {
			os.Exit(64)
		}
		if _, e := pidNamespace(ns); e != nil {
			os.Exit(65)
		}
		flags, e := unix.FcntlInt(ns.Fd(), unix.F_GETFD, 0)
		if e != nil || flags&unix.FD_CLOEXEC == 0 {
			os.Exit(66)
		}
		_ = ns.Close()
		if e := w.Close(); e != nil {
			os.Exit(67)
		}
		if w.Check() == nil {
			os.Exit(68)
		}
	} else {
		ready = 2
		if err == nil || w != nil {
			os.Exit(69)
		}
		if input.Mode == "input" || input.Mode == "indices" || input.Mode == "creator" {
			if claimed {
				os.Exit(70)
			}
			for _, fd := range []int{request.NamespaceFD, request.BootstrapFD} {
				flags, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
				if e != nil || flags&unix.FD_CLOEXEC != 0 {
					os.Exit(71)
				}
			}
		} else if !claimed {
			os.Exit(72)
		}
	}
	if _, e := checkpoint.Write([]byte{ready}); e != nil {
		os.Exit(73)
	}
	var reply [1]byte
	if _, e := io.ReadFull(checkpoint, reply[:]); e != nil {
		os.Exit(74)
	}
	// Existing mandatory ownership survives optional refusal without byte loss.
	if _, e := unix.FcntlInt(3, unix.F_GETFD, 0); e != nil {
		os.Exit(75)
	}
	os.Exit(0)
}

func TestWorkerHandoffClosedSourceAndTransfer(t *testing.T) {
	var a *LaunchAnchor
	r := workerFixtureRequest(false, false)
	if tr, e := a.PrepareWorkerHandoff(r, WorkerHandoffFiles{}); tr != nil || e == nil {
		t.Fatal("unavailable anchor minted transfer")
	}
	tr := &WorkerHandoffTransfer{}
	if !errors.Is(tr.Close(), nil) {
		t.Fatal("empty transfer close failed")
	}
	if tr.Check() == nil {
		t.Fatal("closed transfer repaired")
	}
	if e := tr.PublishStarted(nil); e == nil {
		t.Fatal("closed transfer published")
	}
}
