package controller

import (
	"context"
	"os"
	"os/exec"
	"strconv"
)

// This fixed test child is entered only through TestMain's actual RunWorker
// route. It exercises private inheritance without a VM or workload launch.
func nativePhaseTestWorker(released *os.File, work WorkerInput) {
	writer := OpenNativePhaseWriter(work)
	if writer != nil {
		defer writer.Close()
		for _, badFD := range []int{3, 4} {
			bad := work
			bad.NativePhaseFD = badFD
			if OpenNativePhaseWriter(bad) != nil {
				os.Exit(100)
			}
		}
		bad := work
		identity := *bad.NativePhasePipe
		identity.Inode++
		bad.NativePhasePipe = &identity
		if OpenNativePhaseWriter(bad) != nil {
			os.Exit(101)
		}
		// CLOEXEC must be established before the first descendant command.
		if err := exec.Command("/bin/sh", "-c", "test ! -e /dev/fd/"+strconv.Itoa(work.NativePhaseFD)).Run(); err != nil {
			os.Exit(102)
		}
	}
	checkpoints := NewWorkerCheckpoints(released)
	checkpoints.Gate()
	if err := checkpoints.BeginEffects(context.Background(), EffectIntent{Scope: &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "coder"}}}}); err != nil {
		os.Exit(103)
	}
	_, _ = os.Stdout.Write([]byte("original-effect"))
	if len(work.Request.Args) != 0 && work.Request.Args[0] == "authority-fault" {
		_, _ = released.Write([]byte{255})
		writer.Flush(nativePhaseTestBody("coder", false))
		os.Exit(7)
	}
	if err := checkpoints.Finish(); err != nil {
		os.Exit(104)
	}
	writer.Flush(nativePhaseTestBody("coder", false))
	os.Exit(7)
}
