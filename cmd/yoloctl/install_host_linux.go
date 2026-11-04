package main

import "os"

// These positive markers are read only. Absence cannot prove host execution.
func installContainerSignal() string {
	for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Lstat(path); err == nil {
			return path
		}
	}
	return ""
}
