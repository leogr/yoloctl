package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestControllerActionRuntimePreparesOnlyServe(t *testing.T) {
	// These are all actions at the shared loader boundary, including verifier
	// and preflight. Deploy/restore and early worker intake retain their paths.
	for _, action := range []string{"serve", "image-preflight", "export-store", "store-state", "validate-grants", "stop-workloads", "initialize", "activate", "verify", "audit-collect", "collect-storage", "observe-codex", "unknown"} {
		t.Run(action, func(t *testing.T) {
			failure := errors.New("native account state mount is unavailable")
			var calls []string
			verify := func(context.Context) error { calls = append(calls, "verify"); return failure }
			prepare := func(context.Context) error { calls = append(calls, "prepare"); return failure }
			err := loadControllerActionRuntime(t.Context(), action, verify, prepare)
			want := "verify"
			if action == "serve" {
				want = "prepare"
			}
			if !errors.Is(err, failure) || !reflect.DeepEqual(calls, []string{want}) {
				t.Fatal(calls, err)
			}
		})
	}
}
