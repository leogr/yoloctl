//go:build !linux

package main

import "errors"

func imageVMIncarnation(string) (string, error) {
	return "", errors.New("image jobs require the Linux VM controller")
}
