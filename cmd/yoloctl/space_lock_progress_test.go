package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestSpaceLockProgressEndsAfterContention(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "acquired", true: "cancelled"}[cancelled], func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			wire, _ := captureCLIRecords(t)
			release, err := spacelock.Acquire(t.Context(), "progress", true)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			ctx = context.WithValue(ctx, controllerAdvisoryKey{}, func(context.Context, controller.AdvisoryNotice) (controller.AdvisoryReply, error) {
				calls++
				if cancelled {
					cancel()
					return controller.AdvisoryReply{Holder: &controller.BlockerSummary{Method: "box.create", Box: "peer"}}, nil
				} else {
					release()
				}
				return controller.AdvisoryReply{}, nil
			})
			unlock, err := acquireSpaceOperationLock(ctx, "progress")
			if cancelled {
				if !errors.Is(err, context.Canceled) || unlock != nil {
					t.Fatal("cancellation changed", err)
				}
			} else {
				if err != nil || unlock == nil {
					t.Fatal("acquisition changed", err)
				}
				unlock()
			}
			if calls != 1 {
				t.Fatal("contention callback", calls)
			}
			records := readCLIRecords(t, wire)
			if len(records) != 2 {
				t.Fatal("expected running/end only", len(records))
			}
			start, end := prettylog.StructureOf(records[0]), prettylog.StructureOf(records[1])
			want := prettylog.StepOK
			if cancelled {
				want = prettylog.StepFailed
			}
			message := "Waiting for another operation"
			if cancelled {
				message = "Waiting for space lock held by box create peer"
			}
			if start.State != string(prettylog.StepRunning) || end.State != string(want) || start.Step != end.Step || !strings.Contains(records[0].Message, message) {
				t.Fatal("unfinished wait", start, end)
			}
		})
	}
}

func TestSpaceLockUncontendedIsSilent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wire, _ := captureCLIRecords(t)
	unlock, err := acquireSpaceOperationLock(t.Context(), "progress")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if len(readCLIRecords(t, wire)) != 0 {
		t.Fatal("uncontended lock emitted progress")
	}
}
