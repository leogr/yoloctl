//go:build !linux

package boxendpoint

import (
	"fmt"
	"os"
)

// Main refuses to run: the endpoint exists only inside a Linux box.
func Main(args []string) int {
	_, _ = fmt.Fprintln(os.Stderr, "yoloctl box endpoint: only supported as a Linux box's PID 1")
	return 1
}
