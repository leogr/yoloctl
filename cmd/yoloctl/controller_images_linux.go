package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

func imageVMIncarnation(disk string) (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	value := sha256.Sum256(append([]byte(disk), raw...))
	return hex.EncodeToString(value[:]), nil
}
