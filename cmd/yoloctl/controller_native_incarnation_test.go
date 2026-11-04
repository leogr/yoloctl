package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/operator"
)

func TestWorkerNativeIncarnationRequiresMutationEffectGateAndPrivatePhase(t *testing.T) {
	for _, mode := range []string{"read", "ungated", "nil-checkpoints", "original-worker"} {
		t.Run(mode, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = read.Close() }()
			owned := controller.NewWorkerCheckpoints(write)
			defer func() { _ = owned.Close() }()
			checkpoints := owned
			checkpoints.Gate()
			operation := controller.Operation{Mutates: true, EffectGated: true}
			switch mode {
			case "read":
				operation.Mutates = false
			case "ungated":
				operation.EffectGated = false
			case "nil-checkpoints":
				checkpoints = nil
			}
			ctx := withWorkerNativeIncarnation(t.Context(), operation, checkpoints)
			value, err := operator.AllocateNativeIncarnation(ctx, "box")
			if err == nil || value != "" {
				t.Fatal("worker bridge bypassed original private phase")
			}
			want := "requires its original controller incarnation allocator"
			if mode == "original-worker" {
				want = "original live effect-gated worker phase unavailable"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatal("worker installed the wrong allocator route", err)
			}
			// The missing/before-Begin routes must not write a private request.
			// Close the ONE physical owner before reading the pipe to EOF.
			if err := owned.Close(); err != nil {
				t.Fatal(err)
			}
			written, readErr := io.ReadAll(read)
			if readErr != nil || len(written) != 0 {
				t.Fatalf("refused phase wrote checkpoint bytes: %q, %v", written, readErr)
			}
		})
	}
}
