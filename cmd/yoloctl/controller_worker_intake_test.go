package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
)

type workerIntakeOrderFixture struct {
	order     *[]string
	work      controller.WorkerInput
	decodeErr error
	closes    int
}

func (f *workerIntakeOrderFixture) Decode(context.Context) (controller.WorkerInput, string, error) {
	*f.order = append(*f.order, "decode")
	return f.work, "fixture digest", f.decodeErr
}
func (f *workerIntakeOrderFixture) TakeCheckpoint() (*os.File, error) {
	return nil, errors.New("fixture has no checkpoint permission")
}
func (f *workerIntakeOrderFixture) Close() error {
	f.closes++
	*f.order = append(*f.order, "mandatory-close")
	return nil
}

type workerPhaseOrderFixture struct {
	order  *[]string
	closes int
}

func (f *workerPhaseOrderFixture) Flush([]byte) {}
func (f *workerPhaseOrderFixture) Close()       { f.closes++; *f.order = append(*f.order, "phase-close") }

type workerLaunchOrderFixture struct {
	order  *[]string
	closes int
}

func (f *workerLaunchOrderFixture) Check() error {
	return errors.New("fixture does not establish launch authority")
}
func (f *workerLaunchOrderFixture) DuplicatePIDNamespace() (*os.File, error) {
	return nil, errors.New("fixture has no namespace grant")
}
func (f *workerLaunchOrderFixture) Close() error {
	f.closes++
	*f.order = append(*f.order, "launch-close")
	return nil
}

// These fixture callbacks verify the actual entry's ordering and sole owned
// Close paths. They supply no fake native context or positive VM admission.
func TestEarlyWorkerIntakePhaseAndWholePairPrecedeRuntimeLoader(t *testing.T) {
	for _, optional := range []string{"absent", "foreign", "owned-error", "retained"} {
		t.Run(optional, func(t *testing.T) {
			var order []string
			mandatory := &workerIntakeOrderFixture{order: &order}
			phase := &workerPhaseOrderFixture{order: &order}
			launch := &workerLaunchOrderFixture{order: &order}
			state, err := acquireEarlyControllerWorkerWith(t.Context(), earlyControllerWorkerOps{
				claim: func(context.Context) (earlyWorkerMandatory, bool, error) {
					order = append(order, "claim")
					return mandatory, true, nil
				},
				phase: func(controller.WorkerInput) earlyWorkerPhase { order = append(order, "phase"); return phase },
				launch: func(controller.WorkerInput, string) *workerLaunchCandidate {
					order = append(order, "whole-pair")
					return captureWorkerLaunchCandidate(func() (workerLaunchObservation, bool, error) {
						switch optional {
						case "absent":
							return nil, false, nil
						case "foreign":
							return launch, false, errors.New("foreign optional pair remains untouched")
						case "owned-error":
							return launch, true, errors.New("owned pair refused")
						default:
							return launch, true, nil
						}
					})
				},
				load: func(context.Context) error { order = append(order, "loader"); return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			prefix := []string{"claim", "decode", "phase", "whole-pair"}
			if !reflect.DeepEqual(order[:4], prefix) || order[len(order)-1] != "loader" {
				t.Fatal(order)
			}
			if optional == "owned-error" && launch.closes != 1 {
				t.Fatal("owned error did not close original observation", order)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			want := 0
			if optional == "owned-error" || optional == "retained" {
				want = 1
			}
			if mandatory.closes != 1 || phase.closes != 1 || launch.closes != want {
				t.Fatal("owned lifetime changed", mandatory.closes, phase.closes, launch.closes)
			}
			if _, err := state.TakeCheckpoint(); err == nil {
				t.Fatal("closed intake supplied checkpoint")
			}
		})
	}
}
func TestEarlyWorkerIntakeMandatoryAndRuntimeFailuresKeepOriginalError(t *testing.T) {
	for _, stage := range []string{"unclaimed", "claim", "decode", "loader"} {
		t.Run(stage, func(t *testing.T) {
			var order []string
			boom := errors.New("original " + stage + " error")
			mandatory := &workerIntakeOrderFixture{order: &order}
			phase := &workerPhaseOrderFixture{order: &order}
			launch := &workerLaunchOrderFixture{order: &order}
			if stage == "decode" {
				mandatory.decodeErr = boom
			}
			state, err := acquireEarlyControllerWorkerWith(t.Context(), earlyControllerWorkerOps{
				claim: func(context.Context) (earlyWorkerMandatory, bool, error) {
					order = append(order, "claim")
					if stage == "unclaimed" {
						return mandatory, false, boom
					}
					if stage == "claim" {
						return mandatory, true, boom
					}
					return mandatory, true, nil
				},
				phase: func(controller.WorkerInput) earlyWorkerPhase { order = append(order, "phase"); return phase },
				launch: func(controller.WorkerInput, string) *workerLaunchCandidate {
					order = append(order, "whole-pair")
					return captureWorkerLaunchCandidate(func() (workerLaunchObservation, bool, error) { return launch, true, nil })
				},
				load: func(context.Context) error { order = append(order, "loader"); return boom },
			})
			if state != nil || !errors.Is(err, boom) {
				t.Fatal(state, err)
			}
			if stage == "unclaimed" {
				if mandatory.closes != 0 {
					t.Fatal("unclaimed mandatory handle closed")
				}
			} else if mandatory.closes != 1 {
				t.Fatal(order)
			}
			if stage == "loader" {
				if phase.closes != 1 || launch.closes != 1 {
					t.Fatal("loader failure lost owned observations", order)
				}
			} else {
				if phase.closes != 0 || launch.closes != 0 {
					t.Fatal("early failure touched later handles", order)
				}
			}
		})
	}
}
