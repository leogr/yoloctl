package main

import (
	"errors"
	"os"
	"testing"
)

type fakeEarlyLaunchObservation struct{ closes int }

func (*fakeEarlyLaunchObservation) Check() error { return nil }
func (*fakeEarlyLaunchObservation) DuplicatePIDNamespace() (*os.File, error) {
	return nil, errors.New("fixture only")
}

type failedCloseEarlyObservation struct {
	fakeEarlyLaunchObservation
	failure error
}

func (f *failedCloseEarlyObservation) Close() error {
	f.closes++
	return f.failure
}

func TestEarlyControllerLaunchCloseFailureIsRetainedWithoutRetry(t *testing.T) {
	failure := errors.New("original anchor Close failed")
	for _, phase := range []string{"owned", "capture-error"} {
		t.Run(phase, func(t *testing.T) {
			source := &failedCloseEarlyObservation{failure: failure}
			candidate := captureControllerLaunchCandidate(func() (controllerLaunchObservation, bool, error) {
				if phase == "capture-error" {
					return source, true, errors.New("original consume failed")
				}
				return source, true, nil
			})
			if phase == "capture-error" {
				if candidate.source != nil || !errors.Is(candidate.failure, failure) || source.closes != 1 {
					t.Fatal("owned capture error lost its Close result")
				}
			} else {
				for range 2 {
					if err := candidate.Close(); !errors.Is(err, failure) {
						t.Fatal("Close result changed", err)
					}
				}
			}
			_ = candidate.Close()
			if source.closes != 1 {
				t.Fatal("original Close retried", source.closes)
			}
		})
	}
}
func (f *fakeEarlyLaunchObservation) Close() error { f.closes++; return nil }

func TestEarlyControllerLaunchOwnershipIsIndependentFromOptionalStartup(t *testing.T) {
	for _, state := range []string{"unknown", "unclaimed-source", "claimed-error", "claimed-missing", "owned"} {
		t.Run(state, func(t *testing.T) {
			source := &fakeEarlyLaunchObservation{}
			candidate := captureControllerLaunchCandidate(func() (controllerLaunchObservation, bool, error) {
				switch state {
				case "unknown":
					return nil, false, errors.New("unrecognized old-image pair")
				case "unclaimed-source":
					return source, false, errors.New("invalid ownership result")
				case "claimed-error":
					return source, true, errors.New("owned receiver failed")
				case "claimed-missing":
					return nil, true, nil
				default:
					return source, true, nil
				}
			})
			if (candidate.source != nil) != (state == "owned") {
				t.Fatal("invalid transfer became a candidate")
			}
			if (candidate.failure == nil) != (state == "owned") {
				t.Fatal("unavailable transfer was not explicit")
			}
			if state == "unclaimed-source" && source.closes != 0 {
				t.Fatal("unclaimed original was closed")
			}
			if state == "claimed-error" && source.closes != 1 {
				t.Fatal("owned failure was not released once")
			}
			for range 2 {
				if err := candidate.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if state == "owned" || state == "claimed-error" {
				if source.closes != 1 {
					t.Fatal("original candidate Close repeated", source.closes)
				}
			} else if source.closes != 0 {
				t.Fatal("foreign source was closed")
			}
			// This component never constructs a Server or sets Server.PIDAnchor.
			// Captured source continuity remains separate from native admission.
		})
	}
}
