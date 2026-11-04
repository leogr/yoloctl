//go:build ignore

// Maintenance inspector only. It compiles the same dependency closure to read
// its embedded production source identity; it neither launches a helper nor
// installs/publishes an artifact. Ordinary builds never invoke this command.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	helperinputs "github.com/leogr/yoloctl"
	"github.com/leogr/yoloctl/internal/openshell"
)

func main() {
	binary := flag.String("binary", "", "inspect a release binary without executing it")
	arch := flag.String("arch", "", "expected Linux binary architecture")
	flag.Parse()
	if flag.NArg() != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "unexpected inspector argument")
		os.Exit(1)
	}
	identity, err := helperinputs.Current()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *binary != "" {
		file, err := os.Open(*binary)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer func() { _ = file.Close() }()
		goVersion := openshell.HelperGoVersion(runtime.Version())
		if err := openshell.CheckHelperProducer(file, openshell.HelperPins{Inputs: identity, GoVersion: goVersion}, *arch); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		result := map[string]any{"inputs": identity, "go_version": goVersion, "architecture": *arch, "loader": "static", "binary_sha256": hex.EncodeToString(hash.Sum(nil))}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := json.NewEncoder(os.Stdout).Encode(identity); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
