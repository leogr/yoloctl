package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/storagegc"
)

func reserveReadFor(t *testing.T, value *string) buildReserveRead {
	t.Helper()
	r, err := storagegc.ParseReserve(value)
	if err != nil {
		t.Fatal(err)
	}
	return buildReserveRead{Reserve: r, Setting: value}
}

func TestBuildReservePreflightKeepsCleanupAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		value                                *string
		free                                 uint64
		cleanupErr, timeout, cancel, refused bool
	}{
		{name: "default-admits-after-cleanup", free: 15},
		{name: "uncapped-refuses", value: reserveValue("none"), free: 15, refused: true},
		{name: "zero-admits-empty", value: reserveValue("0GiB"), free: 0},
		{name: "zero-keeps-cleanup-error", value: reserveValue("0GiB"), free: 0, cleanupErr: true, refused: true},
		{name: "zero-keeps-budget", value: reserveValue("0GiB"), free: 0, timeout: true, refused: true},
		{name: "zero-keeps-cancellation", value: reserveValue("0GiB"), free: 0, cancel: true, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				defer bindCLITestLogger(t, io.Discard)()
				reads, cleanups := 0, 0
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				collector := storageCollector{
					disk: func() (storagegc.Disk, error) {
						return storagegc.Disk{Total: 145 * testGiB, Available: tc.free * testGiB}, nil
					},
					reserve: func() buildReserveRead { reads++; return reserveReadFor(t, tc.value) },
					images: func(ctx context.Context, _ []string) (int, error) {
						cleanups++
						if tc.cleanupErr {
							return 0, errors.New("inventory unavailable")
						}
						if tc.timeout {
							<-ctx.Done()
							return 0, ctx.Err()
						}
						if tc.cancel {
							cancel()
							return 0, ctx.Err()
						}
						return 0, nil
					},
					cache: func(context.Context, uint64) error { return nil },
				}
				err := collector.beforeBuild(parent, nil, io.Discard)
				if (err != nil) != tc.refused || reads != 1 || cleanups != 1 {
					t.Fatalf("err=%v reads=%d cleanup=%d", err, reads, cleanups)
				}
				if tc.name == "uncapped-refuses" {
					for _, want := range []string{"box cache clear BOX", "ordinary recreate keeps it", "box recreate BOX discards non-bound files"} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("missing cache remedy %q: %v", want, err)
						}
					}
				}
				if tc.timeout && (!errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "30s preflight budget")) {
					t.Fatal(err)
				}
				if tc.cancel && (!errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "budget")) {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestBuildReserveReadFailureDisclosesUncappedFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := buildReserveConfig
		defer func() { buildReserveConfig = old }()
		loads := 0
		buildReserveConfig = func(string) (*spaceconfig.Config, error) { loads++; return nil, errors.New("settings unreadable") }
		var out bytes.Buffer
		defer bindCLITestLogger(t, &out)()
		c := storageCollector{
			disk: func() (storagegc.Disk, error) {
				return storagegc.Disk{Total: 145 * testGiB, Available: 15 * testGiB}, nil
			},
			reserve: func() buildReserveRead { return readBuildReserve("test") },
			images:  func(context.Context, []string) (int, error) { return 0, nil },
			cache:   func(context.Context, uint64) error { return nil },
		}
		err := c.beforeBuild(t.Context(), nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "29.0 GiB") || !strings.Contains(err.Error(), "cap could not be read") || loads != 1 || !strings.Contains(out.String(), "settings unreadable") {
			t.Fatalf("err=%v loads=%d log=%s", err, loads, out.String())
		}
	})
}

func TestBuildReserveWarningAndReportUseTheAdmissionFigure(t *testing.T) {
	oldDisk, oldReserve := storageWarningDisk, storageWarningReserve
	defer func() { storageWarningDisk, storageWarningReserve = oldDisk, oldReserve }()
	storageWarningDisk = func() (storagegc.Disk, error) {
		return storagegc.Disk{Total: 145 * testGiB, Available: 15 * testGiB}, nil
	}
	storageWarningReserve = func() buildReserveRead { return reserveReadFor(t, nil) }
	if note := diskReserveWarning(); note != "" {
		t.Fatal("warned at cleanup target instead of capped admission", note)
	}
	storageWarningDisk = func() (storagegc.Disk, error) {
		return storagegc.Disk{Total: 145 * testGiB, Available: 9 * testGiB}, nil
	}
	if note := diskReserveWarning(); !strings.Contains(note, "needs 10.0 GiB") || !strings.Contains(note, "storage.build_reserve_max") {
		t.Fatal(note)
	}
	src := storageFixture()
	src.reserve = func() buildReserveRead { return reserveReadFor(t, reserveValue("0GiB")) }
	r := collectSpaceStorage(t.Context(), src, time.Now())
	if r.BuildReserve == nil || r.BuildReserve.Required != 0 || r.BuildReserve.Cap == nil || *r.BuildReserve.Cap != 0 || r.Other == nil {
		t.Fatalf("%+v", r)
	}
	var out bytes.Buffer
	if err := writeSpaceStorage(&out, "test", r); err != nil || !strings.Contains(out.String(), "storage.build_reserve_max 0.0 GiB") {
		t.Fatal(out.String(), err)
	}
}

func reserveValue(s string) *string { return &s }
