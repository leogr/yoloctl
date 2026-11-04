package main

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestInstallResultPipeDoesNotLeakIntoSubprocesses(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	if err := checkInstallResultPipe(int(write.Fd())); err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(write.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("private result descriptor is inheritable", flags, err)
	}
	regular, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regular.Close() }()
	if err := checkInstallResultPipe(int(regular.Fd())); err == nil {
		t.Fatal("ordinary file accepted as private pipe")
	}
}
