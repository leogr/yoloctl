//go:build linux

package vmanchor

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// These are producer mechanism fixtures using this test process, not actual
// PID1/VM provenance or consumer admission evidence. Production has no override.
func fixtureSource(t *testing.T) *FirecrackerSource {
	t.Helper()
	s, err := captureSource()
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		t.Skipf("kernel process pin unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func fixtureChild(t *testing.T) *exec.Cmd {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchProducerChild$")
	cmd.Env = append(os.Environ(), "YOLOCTL_ANCHOR_TEST_CHILD=1")
	cmd.Stdin = r
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		t.Fatal(err)
	}
	_ = r.Close()
	t.Cleanup(func() {
		_ = w.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	})
	return cmd
}

func TestLaunchProducerChild(t *testing.T) {
	if os.Getenv("YOLOCTL_ANCHOR_TEST_CHILD") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func fixtureTransfer(t *testing.T, s *FirecrackerSource) (*LaunchTransfer, *os.File) {
	t.Helper()
	tr, err := s.PrepareLaunch()
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := duplicate(tr.child)
	if err != nil {
		_ = tr.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = receiver.Close()
		if err := tr.Close(); err != nil {
			t.Error(err)
		}
	})
	return tr, receiver
}

func TestLaunchProducerSourceGuardAndLifetime(t *testing.T) {
	if os.Getpid() != 1 {
		if s, err := CaptureFirecrackerSource(); err == nil || s != nil {
			t.Fatal("ordinary caller minted boot source")
		}
	}
	s := fixtureSource(t)
	a, err := s.PrepareLaunch()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PrepareLaunch()
	if err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	if a.launch == b.launch || len(a.launch) != 32 || a.namespace.Fd() == b.namespace.Fd() {
		t.Fatal("launch attempt reused its transfer")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareLaunch(); err == nil {
		t.Fatal("closed source reused")
	}
}

func TestLaunchProducerRecordAndCredentials(t *testing.T) {
	s := fixtureSource(t)
	tr, receiver := fixtureTransfer(t, s)
	peer, err := unix.GetsockoptUcred(int(receiver.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int(peer.Pid) != os.Getpid() {
		t.Fatalf("creator: %v %v", peer, err)
	}
	child := fixtureChild(t)
	if err := tr.PublishStarted(child.Process); err != nil {
		t.Fatal(err)
	}
	buf, control := make([]byte, launchFrameLimit+1), make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	n, nc, flags, _, err := unix.Recvmsg(int(receiver.Fd()), buf, control, unix.MSG_DONTWAIT)
	if err != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n > launchFrameLimit {
		t.Fatalf("receive n=%d flags=%d err=%v", n, flags, err)
	}
	var record launchRecord
	if err := json.Unmarshal(buf[:n], &record); err != nil {
		t.Fatal(err)
	}
	if record.Version != 1 || record.Launch != tr.launch || record.Boot != s.boot || record.Launcher != s.launcher.identity || record.Child.PID != child.Process.Pid || record.Child.Parent != os.Getpid() || record.Child.Namespace != s.launcher.identity.Namespace {
		t.Fatalf("wrong launch record: %+v", record)
	}
	messages, err := unix.ParseSocketControlMessage(control[:nc])
	if err != nil || len(messages) != 1 {
		t.Fatalf("credentials: %v %v", messages, err)
	}
	cred, err := unix.ParseUnixCredentials(&messages[0])
	if err != nil || *cred != *peer {
		t.Fatalf("packet creator mismatch: %v %v", cred, err)
	}
	if tr.child != nil || tr.namespace != nil {
		t.Fatal("parent retained child-side handles")
	}
	if err := tr.PublishStarted(child.Process); err == nil {
		t.Fatal("publication replayed")
	}
}

func TestLaunchProducerFullQueueDoesNotWaitForChild(t *testing.T) {
	s := fixtureSource(t)
	tr, _ := fixtureTransfer(t, s)
	full := false
	for i := 0; i < 4096; i++ {
		_, err := unix.SendmsgN(int(tr.parent.Fd()), make([]byte, 1024), nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if errors.Is(err, unix.EAGAIN) {
			full = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("fixture did not fill bounded socket queue")
	}
	child := fixtureChild(t)
	before := time.Now()
	err := tr.PublishStarted(child.Process)
	if !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("full queue publication: %v", err)
	}
	if time.Since(before) > 2*time.Second {
		t.Fatal("publication waited for non-reading child")
	}
}

func TestLaunchProducerUnavailableAndForeignChild(t *testing.T) {
	t.Run("changed boot", func(t *testing.T) {
		s := fixtureSource(t)
		s.boot = "00000000-0000-0000-0000-000000000000"
		if _, err := s.PrepareLaunch(); err == nil {
			t.Fatal("changed boot accepted")
		}
	})
	t.Run("wrong parent", func(t *testing.T) {
		s := fixtureSource(t)
		tr, _ := fixtureTransfer(t, s)
		if err := tr.PublishStarted(&os.Process{Pid: os.Getpid()}); err == nil {
			t.Fatal("nonchild accepted")
		}
	})
	t.Run("closed receiver", func(t *testing.T) {
		s := fixtureSource(t)
		tr, receiver := fixtureTransfer(t, s)
		if err := receiver.Close(); err != nil {
			t.Fatal(err)
		}
		child := fixtureChild(t)
		if err := tr.PublishStarted(child.Process); !errors.Is(err, unix.EPIPE) && !errors.Is(err, unix.ECONNRESET) {
			t.Fatalf("closed receiver: %v", err)
		}
	})
	t.Run("nil child is terminal", func(t *testing.T) {
		s := fixtureSource(t)
		tr, _ := fixtureTransfer(t, s)
		if err := tr.PublishStarted(nil); err == nil {
			t.Fatal("missing Start accepted")
		}
		if err := tr.PublishStarted(nil); err == nil {
			t.Fatal("failed transfer replayed")
		}
	})
}

// Receiver helpers deliberately use a private fixture parent PID, never the
// production API's fixed original launcher. This tests continuity mechanics,
// not guest boot provenance or a hostile replacement-init scenario.
func TestLaunchReceiverChild(t *testing.T) {
	mode := os.Getenv("YOLOCTL_ANCHOR_RECEIVER_CHILD")
	if mode == "" {
		return
	}
	var a *LaunchAnchor
	var claimed bool
	var err error
	if mode == "foreign" {
		a, claimed, err = ConsumeInheritedFirecrackerLaunch(3, 4)
		if a != nil || claimed || err == nil {
			os.Exit(20)
		}
		for _, fd := range []int{3, 4} {
			flags, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if e != nil || flags&unix.FD_CLOEXEC != 0 {
				os.Exit(21)
			}
		}
		os.Exit(0)
	}
	budget := 5 * time.Second
	if mode == "timeout" {
		budget = 25 * time.Millisecond
	}
	a, claimed, err = consumeInheritedLaunch(3, 4, os.Getppid(), budget)
	if mode != "valid" {
		if a != nil || !claimed || err == nil {
			os.Exit(22)
		}
		for _, fd := range []int{3, 4} {
			if _, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(e, unix.EBADF) {
				os.Exit(23)
			}
		}
		os.Exit(0)
	}
	if err != nil || !claimed || a == nil {
		os.Exit(24)
	}
	if a.Check() != nil {
		os.Exit(25)
	}
	ns, e := a.DuplicatePIDNamespace()
	if e != nil {
		os.Exit(26)
	}
	flags, e := unix.FcntlInt(ns.Fd(), unix.F_GETFD, 0)
	if e != nil || flags&unix.FD_CLOEXEC == 0 {
		os.Exit(27)
	}
	if _, e := pidNamespace(ns); e != nil {
		os.Exit(28)
	}
	if e := ns.Close(); e != nil {
		os.Exit(29)
	}
	dup, e := a.Duplicate()
	if e != nil {
		os.Exit(30)
	}
	if e := a.Close(); e != nil {
		os.Exit(31)
	}
	if a.Check() == nil {
		os.Exit(32)
	}
	if _, e := a.DuplicatePIDNamespace(); e == nil {
		os.Exit(33)
	}
	if e := dup.Check(); e != nil {
		os.Exit(34)
	}
	if e := dup.Close(); e != nil {
		os.Exit(35)
	}
	os.Exit(0)
}

func TestLaunchReceiverContinuityAndOwnership(t *testing.T) {
	for _, mode := range []string{"valid", "foreign", "stale", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s := fixtureSource(t)
			tr, e := s.PrepareLaunch()
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tr.Close() }()
			cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchReceiverChild$")
			cmd.Env = append(os.Environ(), "YOLOCTL_ANCHOR_RECEIVER_CHILD="+mode)
			cmd.ExtraFiles = tr.ChildFiles()
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			if e := cmd.Start(); e != nil {
				t.Fatal(e)
			}
			if mode == "valid" {
				if e := tr.PublishStarted(cmd.Process); e != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal(e)
				}
			}
			if mode == "stale" {
				pin, e := pinProcess(s.root, cmd.Process.Pid)
				if e != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal(e)
				}
				record := launchRecord{Version: 1, Launch: tr.launch, Boot: s.boot, Launcher: s.launcher.identity, Child: pin.identity}
				record.Child.Start++
				_ = pin.close()
				raw, e := json.Marshal(record)
				if e != nil {
					t.Fatal(e)
				}
				if _, e := unix.SendmsgN(int(tr.parent.Fd()), raw, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); e != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal(e)
				}
			}
			if e := cmd.Wait(); e != nil {
				t.Fatalf("%s receiver: %v", mode, e)
			}
		})
	}
}

func TestLaunchReceiverUnknownPairUntouched(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if a, claimed, err := ConsumeInheritedFirecrackerLaunch(int(r.Fd()), int(w.Fd())); a != nil || claimed || err == nil {
		t.Fatal("unrelated pair claimed")
	}
	if _, e := w.Write([]byte("x")); e != nil {
		t.Fatal("unrelated write descriptor closed", e)
	}
	var b [1]byte
	if _, e := r.Read(b[:]); e != nil || b[0] != 'x' {
		t.Fatal("unrelated read descriptor closed", e)
	}
	if a, claimed, err := ConsumeInheritedFirecrackerLaunch(-1, -1); a != nil || claimed || err == nil {
		t.Fatal("absent pair claimed")
	}
}

func TestLaunchRecordStrictAndBounded(t *testing.T) {
	want := launchRecord{Version: 1, Launch: "00112233445566778899aabbccddeeff", Boot: "boot", Launcher: processIdentity{PID: 1}, Child: processIdentity{PID: 2}}
	valid, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var record launchRecord
	if e := strictLaunchRecord(valid, &record); e != nil {
		t.Fatal(e)
	}
	if record != want {
		t.Fatal("canonical producer record changed")
	}
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"version":1}`),
		[]byte(`{"launcher":{"pid":1,"pid":1}}`),
		[]byte(`{"unknown":1}`),
		[]byte(`{"child":{"unknown":1}}`),
		[]byte(`{} {}`),
		[]byte(`{"child":[1]}`),
		make([]byte, launchFrameLimit+1),
		[]byte(strings.Replace(string(valid), `"version":1`, `"Version":1`, 1)),
		[]byte(strings.Replace(string(valid), `"version":1`, `"version":0,"Version":1`, 1)),
		[]byte(strings.Replace(string(valid), `"version":1`, `"Version":0,"version":1`, 1)),
		[]byte(strings.Replace(string(valid), `"child":{"pid":2`, `"child":{"PID":2`, 1)),
		[]byte(strings.Replace(string(valid), `"child":{"pid":2`, `"child":{"pid":0,"PID":2`, 1)),
		[]byte(strings.Replace(string(valid), `"child":{"pid":2`, `"child":{"PID":0,"pid":2`, 1)),
		[]byte(strings.Replace(string(valid), `"device":0`, `"Device":0`, 1)),
		[]byte(strings.Replace(string(valid), `"device":0`, `"device":1,"Device":0`, 1)),
		[]byte(strings.Replace(string(valid), `"device":0`, `"Device":1,"device":0`, 1)),
		[]byte(strings.Replace(string(valid), want.Launch, strings.ToUpper(want.Launch), 1)),
	} {
		record = want
		if e := strictLaunchRecord(raw, &record); e == nil {
			t.Fatalf("invalid frame accepted: %.100s", raw)
		}
		if record != want {
			t.Fatalf("refused frame published partial result: %.100s", raw)
		}
	}
}

func TestLaunchPacketBoundsAndSecondFrame(t *testing.T) {
	for _, mode := range []string{"oversize", "second", "empty second", "empty second rights", "no second", "true EOF", "no credentials", "rights"} {
		t.Run(mode, func(t *testing.T) {
			pair, e := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
			if mode != "no credentials" {
				if e := unix.SetsockoptInt(pair[1], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); e != nil {
					t.Fatal(e)
				}
			}
			raw := []byte(`{}`)
			if mode == "oversize" {
				raw = make([]byte, launchFrameLimit+1)
			}
			var control []byte
			if mode == "rights" {
				control = unix.UnixRights(pair[0])
			}
			if _, e := unix.SendmsgN(pair[0], raw, control, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); e != nil {
				t.Fatal(e)
			}
			if mode == "second" || mode == "empty second" || mode == "empty second rights" {
				second, control := raw, []byte(nil)
				if mode != "second" {
					second = nil
				}
				if mode == "empty second rights" {
					control = unix.UnixRights(pair[0])
				}
				if _, e := unix.SendmsgN(pair[0], second, control, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "true EOF" {
				if e := unix.Shutdown(pair[0], unix.SHUT_WR); e != nil {
					t.Fatal(e)
				}
			}
			_, _, e = receiveLaunchFrame(pair[1], time.Now().Add(time.Second))
			if mode == "no second" || mode == "true EOF" {
				if e != nil {
					t.Fatalf("%s refused: %v", mode, e)
				}
			} else if e == nil {
				t.Fatalf("%s packet accepted", mode)
			}
		})
	}
}
