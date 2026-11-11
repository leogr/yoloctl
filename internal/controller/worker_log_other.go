//go:build !linux

package controller

import "os"

// OpenWorkerLog is Linux-only: controller workers run in the space VM.
func OpenWorkerLog(WorkerInput) *os.File { return nil }
