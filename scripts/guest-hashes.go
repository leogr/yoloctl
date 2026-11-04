//go:build ignore

// Prints linker flags that bind a host CLI to the freshly built companions.
// Run natively even when the host CLI itself is being cross-compiled.
package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: guest-hashes BIN_DIR")
		os.Exit(1)
	}
	for _, arch := range []struct{ file, symbol string }{{"yoloctl-linux-amd64", "AMD64"}, {"yoloctl-linux-arm64", "ARM64"}, {"yoloctl-vm-init-linux-amd64", "InitAMD64"}} {
		f, err := os.Open(filepath.Join(os.Args[1], arch.file))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("-X github.com/leogr/yoloctl/internal/guestbinary.%sSHA256=%x ", arch.symbol, h.Sum(nil))
	}
}
