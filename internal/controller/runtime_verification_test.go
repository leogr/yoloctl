package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestRuntimeStorageEntryUsesOnlyBoundedReadOnlyVerification(t *testing.T) {
	old := executeRuntimeDisk
	t.Cleanup(func() { executeRuntimeDisk = old })
	c := &spaceenv.Context{Space: "work", DiskID: testDisk, DiskUUID: "native-uuid", UID: 2000, GID: 2000}
	for _, failure := range []string{"", "UUID unavailable", "ownership marker mismatch", "native bind missing"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			executeRuntimeDisk = func(ctx context.Context, space, disk, uuid string, uid, gid int, req accountstate.Request) (accountstate.Result, error) {
				calls++
				if req.Action != "verify" || space != c.Space || disk != c.DiskID || uuid != c.DiskUUID || uid != c.UID || gid != c.GID {
					t.Fatal("runtime entry attempted preparation or changed identity", req)
				}
				// The controller's own check has no client: it is bounded end to
				// end by its fail-safe, which is at least a minute.
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < time.Minute || time.Until(deadline) > runtimeStorageFailSafe {
					t.Fatal("native verification is not bounded by its fail-safe")
				}
				if failure != "" {
					return accountstate.Result{}, errors.New(failure)
				}
				return accountstate.Result{UUID: uuid}, nil
			}
			err := verifyRuntimeStorage(t.Context(), c)
			if (err != nil) != (failure != "") || calls != 1 {
				t.Fatal("identity was repaired or ignored", calls, err)
			}
		})
	}
}

func TestLifecyclePreparationVerifiesAfterPrepareAndPreservesFailures(t *testing.T) {
	old := executeRuntimeDisk
	t.Cleanup(func() { executeRuntimeDisk = old })
	c := &spaceenv.Context{Space: "work", DiskID: testDisk, DiskUUID: "native-uuid", UID: 2000, GID: 2000}
	for _, failure := range []string{"absent", "present", "wrong UUID", "missing marker", "another space marker", "target mounts other storage", "verification failed"} {
		t.Run(failure, func(t *testing.T) {
			var calls []string
			sentinel := errors.New(failure)
			executeRuntimeDisk = func(_ context.Context, space, disk, uuid string, uid, gid int, req accountstate.Request) (accountstate.Result, error) {
				if space != c.Space || disk != c.DiskID || uuid != c.DiskUUID || uid != c.UID || gid != c.GID {
					t.Fatal("changed disk identity")
				}
				calls = append(calls, req.Action)
				if req.Action == "prepare" && failure != "absent" && failure != "present" && failure != "verification failed" {
					return accountstate.Result{}, sentinel
				}
				if req.Action == "verify" && failure == "verification failed" {
					return accountstate.Result{}, sentinel
				}
				return accountstate.Result{UUID: uuid}, nil
			}
			err := prepareRuntimeStorage(t.Context(), c)
			want := []string{"prepare"}
			if failure == "absent" || failure == "present" || failure == "verification failed" {
				want = append(want, "verify")
			}
			success := failure == "absent" || failure == "present"
			if !reflect.DeepEqual(calls, want) || (success && err != nil) || (!success && !errors.Is(err, sentinel)) {
				t.Fatal(calls, err)
			}
		})
	}
}
