//go:build linux

package vmguest

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

type fixtureControllerTransfer struct {
	files                []*os.File
	publishErr, closeErr error
	published            *os.Process
	closed               int
	onPublish            func()
}

func (f *fixtureControllerTransfer) ChildFiles() []*os.File { return f.files }
func (f *fixtureControllerTransfer) PublishStarted(p *os.Process) error {
	f.published = p
	if f.onPublish != nil {
		f.onPublish()
	}
	return f.publishErr
}
func (f *fixtureControllerTransfer) Close() error { f.closed++; return f.closeErr }

func TestControllerAnchorPreservesActualStartAndOneWait(t *testing.T) {
	for _, failure := range []string{"none", "publication", "close"} {
		t.Run(failure, func(t *testing.T) {
			transfer := &fixtureControllerTransfer{}
			if failure == "publication" {
				transfer.publishErr = errors.New("metadata unavailable")
			}
			if failure == "close" {
				transfer.closeErr = errors.New("close unavailable")
			}
			transfer.onPublish = func() {
				if !childrenMu.TryLock() {
					t.Fatal("publication holds reaper lock")
				}
				childrenMu.Unlock()
			}
			cmd := exec.Command("/bin/sh", "-c", "exit 0")
			child, err := startControllerAttempt(cmd, transfer, startOwned)
			if err != nil || child == nil {
				t.Fatalf("actual child lost: %v", err)
			}
			if transfer.published != cmd.Process || transfer.closed != 1 {
				t.Fatal("original Start or transfer ownership changed")
			}
			if _, ok := ownedChildren.Load(cmd.Process.Pid); !ok {
				t.Fatal("original reaper registration lost")
			}
			if err := child.wait(); err != nil {
				t.Fatal(err)
			}
			if _, ok := ownedChildren.Load(cmd.Process.Pid); ok {
				t.Fatal("sole Wait did not clear original registration")
			}
			if err := child.wait(); err == nil {
				t.Fatal("child permitted a second successful Wait")
			}
		})
	}
}

func TestControllerAnchorPreservesFailedStart(t *testing.T) {
	want := errors.New("original Start failure")
	transfer := &fixtureControllerTransfer{}
	child, err := startControllerAttempt(exec.Command("unused"), transfer, func(*exec.Cmd) error { return want })
	if child != nil || err != want || transfer.published != nil || transfer.closed != 1 {
		t.Fatalf("Start failure rewritten: %v %v %+v", child, err, transfer)
	}
}

func TestControllerAnchorLegacyAndUnrelatedFiles(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		var transfer *fixtureControllerTransfer
		var optional controllerLaunchTransfer
		if foreign {
			file, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			cmd.ExtraFiles = []*os.File{file}
			transfer = &fixtureControllerTransfer{}
			optional = transfer
		}
		child, err := startControllerAttempt(cmd, optional, startOwned)
		if err != nil {
			t.Fatal(err)
		}
		if err := child.wait(); err != nil {
			t.Fatal(err)
		}
		if foreign && (transfer.published != nil || transfer.closed != 1 || len(cmd.ExtraFiles) != 1) {
			t.Fatal("unrelated inherited files overwritten")
		}
	}
}
