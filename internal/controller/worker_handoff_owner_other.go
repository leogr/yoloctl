//go:build !linux

package controller

// No platform DTO or invented launch proof on unsupported producer paths.
func newNativeWorkerHandoffOwner(*nativeNonceOwner) workerHandoffOwner { return nil }
