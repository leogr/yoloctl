package controller

import (
	"context"
	"errors"
	"io"
	"testing"
)

// Closed typed outcome controls only, no source/provider/backend constructor.
func TestReceivedImageUncertaintySurvivesFinalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw := []byte(`{"uncertain":true,"error":"original publication outcome unknown"}`)
	reply, err := receivedImageOutcome(ctx, raw, context.Canceled)
	var uncertain *EffectsUncertain
	if reply.Error != "original publication outcome unknown" || !reply.Uncertain || !errors.As(err, &uncertain) || !errors.Is(err, context.Canceled) {
		t.Fatal("known original typed outcome was discarded", reply, err)
	}
}
func TestReceivedImageKnownErrorAndCancellationBothRetained(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reply, err := receivedImageOutcome(ctx, []byte(`{"error":"original request denied"}`), context.Canceled)
	var uncertain *EffectsUncertain
	if reply.Error != "original request denied" || !errors.Is(err, context.Canceled) || errors.As(err, &uncertain) {
		t.Fatal("known denial/cancellation semantics changed", reply, err)
	}
}
func TestReceivedImageSuccessBytesDoNotEraseTransportFailure(t *testing.T) {
	reply, err := receivedImageOutcome(t.Context(), []byte(`{"platform":"linux/amd64"}`), io.ErrUnexpectedEOF)
	if reply.Platform != "linux/amd64" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("complete result or transport failure discarded", reply, err)
	}
}
func TestPartialOrMalformedImageCannotMintKnownUncertainty(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`{"uncertain":true,"error":`)} {
		reply, err := receivedImageOutcome(t.Context(), raw, io.ErrUnexpectedEOF)
		var uncertain *EffectsUncertain
		if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) || reply.Uncertain || reply.Error != "" || errors.As(err, &uncertain) {
			t.Fatal("partial/malformed data became typed parent outcome", reply, err)
		}
	}
}
