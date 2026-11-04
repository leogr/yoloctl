//go:build !linux

package openshell

import (
	"fmt"
	"os"
)

func NetworkWorkerMain([]string) int {
	_, _ = fmt.Fprintln(os.Stderr, "native egress infrastructure runs only in the Linux space VM")
	return 1
}
