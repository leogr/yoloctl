package main

import (
	"errors"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/vmanchor"
)

type earlyWorkerConsumer func(vmanchor.WorkerHandoffRequest, []byte) (workerLaunchObservation, bool, error)

func consumeEarlyWorkerLaunch(work controller.WorkerInput, inputDigest string) *workerLaunchCandidate {
	return consumeEarlyWorkerLaunchWith(work, inputDigest, func(request vmanchor.WorkerHandoffRequest, canonical []byte) (workerLaunchObservation, bool, error) {
		source, claimed, err := vmanchor.ConsumeWorkerHandoff(request, canonical)
		if source == nil {
			return nil, claimed, err
		}
		return source, claimed, err
	})
}
func consumeEarlyWorkerLaunchWith(work controller.WorkerInput, inputDigest string, consume earlyWorkerConsumer) *workerLaunchCandidate {
	canonical, digest, err := controller.CanonicalWorkerInput(work)
	if err != nil || digest != inputDigest || consume == nil {
		return captureWorkerLaunchCandidate(func() (workerLaunchObservation, bool, error) {
			return nil, false, errors.Join(err, errors.New("actual canonical private worker input changed"))
		})
	}
	viewFD := 0
	if work.Request.View {
		viewFD = 5
	}
	request := vmanchor.WorkerHandoffRequest{
		ExecutionID: work.Request.ID, Method: work.Request.Method,
		AssociationDigest: work.WorkerAssociationDigest, InputDigest: inputDigest,
		ViewFD: viewFD, PhaseFD: work.NativePhaseFD,
		NamespaceFD: work.PIDNamespaceFD, BootstrapFD: work.PIDNamespaceBootstrapFD,
	}
	// Actual frozen vmanchor API owns all raw pair eligibility, retained
	// creator/parent/self/process joins, immediate CLOEXEC and finite receive.
	// Never wrap, alter or standalone-claim either optional original here.
	return captureWorkerLaunchCandidate(func() (workerLaunchObservation, bool, error) { return consume(request, canonical) })
}
