//go:build !linux || !amd64

package main

import "errors"

func installDefaultImageReadOnly() (string, error) {
	return "", errors.New("the Firecracker backend is unavailable in this host build")
}
