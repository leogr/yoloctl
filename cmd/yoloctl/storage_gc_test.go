package main

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/storagegc"
)

func TestStorageBeforeBuild(t *testing.T) {
	for _, test := range []struct {
		name               string
		initial, recovered uint64
		inventoryErr       bool
		wantErr            bool
		wantCleanup        bool
	}{
		{"healthy", 40, 40, false, false, false},
		{"reclaimed", 5, 30, false, false, true},
		{"shared cache did not free disk", 5, 5, false, true, true},
		{"uncertain inventory", 5, 40, true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				defer bindCLITestLogger(t, io.Discard)()
				available := test.initial
				cleaned, pruned := false, false
				refs := []string{"local/from:latest", "local/output:latest"}
				c := storageCollector{
					disk: func() (storagegc.Disk, error) { return storagegc.Disk{Total: 100, Available: available}, nil },
					images: func(ctx context.Context, extra []string) (int, error) {
						cleaned = true
						if !reflect.DeepEqual(extra, refs) {
							t.Fatal("lost build inputs", extra)
						}
						if _, ok := ctx.Deadline(); !ok {
							t.Fatal("unbounded cleanup")
						}
						if test.inventoryErr {
							return 0, errors.New("uncertain")
						}
						return 2, nil
					},
					cache: func(_ context.Context, keep uint64) error {
						if keep != 0 {
							t.Fatal("pressure retained unused cache")
						}
						pruned = true
						available = test.recovered
						return nil
					},
				}
				err := c.beforeBuild(t.Context(), refs, io.Discard)
				if (err != nil) != test.wantErr || cleaned != test.wantCleanup {
					t.Fatalf("cleanup=%v, error=%v", cleaned, err)
				}
				if test.inventoryErr && pruned {
					t.Fatal("pruned despite uncertain inventory")
				}
				if test.name == "shared cache did not free disk" && !strings.Contains(err.Error(), "20%") {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestStorageBeforeBuildDelayedReclamation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		defer bindCLITestLogger(t, io.Discard)()
		start := time.Now()
		measures := 0
		c := storageCollector{
			disk: func() (storagegc.Disk, error) {
				measures++
				free := uint64(5)
				if time.Since(start) >= 250*time.Millisecond {
					free = 30
				}
				return storagegc.Disk{Total: 100, Available: free}, nil
			},
			images: func(context.Context, []string) (int, error) { return 1, nil },
			cache:  func(context.Context, uint64) error { return nil },
		}
		if err := c.beforeBuild(t.Context(), nil, io.Discard); err != nil {
			t.Fatal(err)
		}
		if measures < 5 || time.Since(start) != 300*time.Millisecond {
			t.Fatalf("did not remeasure delayed recovery: %d reads in %s", measures, time.Since(start))
		}
	})
}

func TestStorageBeforeBuildTimeoutDiagnostic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		defer bindCLITestLogger(t, io.Discard)()
		c := storageCollector{
			disk: func() (storagegc.Disk, error) { return storagegc.Disk{Total: 100, Available: 5}, nil },
			images: func(ctx context.Context, _ []string) (int, error) {
				<-ctx.Done()
				return 0, errors.New("signal: killed")
			},
			cache: func(context.Context, uint64) error { t.Fatal("pruned incomplete inventory"); return nil },
		}
		err := c.beforeBuild(t.Context(), nil, io.Discard)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "30s preflight budget") || !strings.Contains(err.Error(), "20%") {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := c.beforeBuild(ctx, nil, io.Discard); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "budget") {
			t.Fatalf("user cancellation reported as shortage: %v", err)
		}
	})
}

func TestStorageIdleCleanupKeepsBudget(t *testing.T) {
	for _, removed := range []int{0, 1} {
		pruned := false
		c := storageCollector{
			disk:   func() (storagegc.Disk, error) { return storagegc.Disk{Total: 100, Available: 40}, nil },
			images: func(context.Context, []string) (int, error) { return removed, nil },
			cache: func(_ context.Context, keep uint64) error {
				pruned = true
				if keep != 10 {
					t.Fatal(keep)
				}
				return nil
			},
		}
		if err := c.collect(t.Context(), nil, io.Discard); err != nil || pruned != (removed > 0) {
			t.Fatalf("pruned=%v, %v", pruned, err)
		}
	}
}
