package main

import "github.com/leogr/yoloctl/internal/controller"

// CLI-private lifetimes. Neither metadata nor this interface constructs source.
type controllerActivationObservation interface{ Close() error }
type controllerServiceSource interface {
	controller.WorkerPIDAnchor
	Close() error
}
