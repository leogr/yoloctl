package controller

import (
	"context"
	"errors"
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
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("native verification is unbounded")
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
