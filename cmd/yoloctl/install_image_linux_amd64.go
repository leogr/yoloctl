package main

import "github.com/leogr/yoloctl/internal/firecracker"

func installDefaultImageReadOnly() (string, error) { return firecracker.ReadDefaultImage() }
