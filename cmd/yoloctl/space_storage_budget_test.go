package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
)

func TestSpaceStorageKeepsBoxesWhenGlobalSnapshotScanTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), spaceStorageTimeout)
		defer cancel()
		src := storageFixture()
		containers := src.containers
		src.containers = func(ctx context.Context) ([]container.Container, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return containers(ctx)
		}
		src.snapshots = func(ctx context.Context) (map[string]uint64, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		r := collectSpaceStorage(ctx, src, time.Now())
		if len(r.Boxes) != 4 || r.Boxes[0].Name != "yolo-dev" {
			t.Fatalf("global scan starved the box measurements: %+v", r)
		}
		if r.Images != nil || r.BuildKit != nil || r.Other != nil {
			t.Fatalf("unmeasured global totals fabricated: %+v", r)
		}
	})
}

func TestSpaceStorageSlowLayerDoesNotStarveOtherBoxes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := storageFixture()
		measure := src.snapshotUsage
		src.snapshotUsage = func(ctx context.Context, key string) (uint64, error) {
			if key == "k-opus" {
				<-ctx.Done()
				return 0, ctx.Err()
			}
			return measure(ctx, key)
		}
		r := collectSpaceStorage(t.Context(), src, time.Now())
		if len(r.Boxes) != 4 || r.Boxes[0].Name != "yolo-dev" || r.Other != nil {
			t.Fatalf("lost completed measurements: %+v", r)
		}
		var slow storageBox
		for _, b := range r.Boxes {
			if b.Name == "opus" {
				slow = b
			}
		}
		if slow.Name == "" || slow.Bytes != nil || len(r.Errors) != 1 || r.Errors[0].Item != "box opus" {
			t.Fatalf("slow box did not remain unknown: %+v %+v", slow, r.Errors)
		}
		raw, err := json.Marshal(slow)
		if err != nil || !bytes.Contains(raw, []byte(`"bytes":null`)) {
			t.Fatal(string(raw), err)
		}
		var out bytes.Buffer
		if err := writeSpaceStorage(&out, "dev", r); err != nil || !strings.Contains(out.String(), "unavailable: box opus") {
			t.Fatal(out.String(), err)
		}
	})
}

func TestSpaceStorageViewDeliversPartialBeforeCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		old := storageViewSource
		defer func() { storageViewSource = old }()
		storageViewSource = func(string) storageSource {
			src := storageFixture()
			src.snapshots = func(ctx context.Context) (map[string]uint64, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return src
		}
		var out bytes.Buffer
		start := time.Now()
		handled, err := runControllerView(parent, controller.Request{Method: spaceStorageMethod, Space: "dev"}, &out)
		var r storageReport
		if !handled || err != nil || parent.Err() != nil || time.Since(start) != spaceStorageTimeout || json.Unmarshal(out.Bytes(), &r) != nil || len(r.Boxes) != 4 {
			t.Fatalf("partial result not delivered under enclosing deadline: elapsed=%v handled=%v err=%v parent=%v out=%s", time.Since(start), handled, err, parent.Err(), out.String())
		}
	})
}

func TestSpaceStorageSharedLayerIsMeasuredAndCountedOnce(t *testing.T) {
	src := storageFixture()
	keys := src.snapshotKeys
	src.snapshotKeys = func(ctx context.Context, names []string) (map[string]string, error) {
		got, err := keys(ctx, names)
		got[fullID("2")] = "k-opus"
		return got, err
	}
	measure := src.snapshotUsage
	calls := map[string]int{}
	src.snapshotUsage = func(ctx context.Context, key string) (uint64, error) {
		calls[key]++
		return measure(ctx, key)
	}
	r := collectSpaceStorage(t.Context(), src, time.Now())
	if len(r.Errors) != 0 || r.Other == nil || calls["k-opus"] != 1 {
		t.Fatalf("shared layer measurement: calls=%v report=%+v", calls, r)
	}
	shared := false
	var sum uint64
	for _, b := range r.Boxes {
		if b.Name == "yolo-dev" {
			shared = b.Bytes == nil && b.SharedWith == "opus"
		}
		if b.Bytes != nil {
			sum += *b.Bytes
		}
	}
	if !shared || sum != 2*testGiB+testGiB/2 {
		t.Fatalf("shared layer was double counted: shared=%v bytes=%d", shared, sum)
	}
}

func TestSpaceStorageViewPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	old := storageViewSource
	defer func() { storageViewSource = old }()
	storageViewSource = func(string) storageSource { return storageFixture() }
	var out bytes.Buffer
	err := runSpaceStorageView(ctx, "dev", &out)
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatal(out.String(), err)
	}
}
