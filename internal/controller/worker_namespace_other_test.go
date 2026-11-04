//go:build !linux

package controller

import "os"

func workerPIDNamespaceTestWorker(_ *os.File, _ WorkerInput, _ *WorkerPIDNamespace) { os.Exit(93) }
