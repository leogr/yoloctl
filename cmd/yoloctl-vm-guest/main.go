//go:build linux

package main

import (
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/vmguest"
)

func main() {
	if e := vmguest.Run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "microVM:", e)
		os.Exit(1)
	}
}
