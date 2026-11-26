//go:build !linux

package controller

import "os"

// available is Linux-only (FIONREAD); the controller runs only in the Linux VM.
// Elsewhere nothing is drained after the output join expired.
func available(*os.File) int { return 0 }
