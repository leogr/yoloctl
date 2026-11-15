package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxcache"
)

func TestSpaceStorageCachesAreOutsideTheLayer(t *testing.T) {
	src := storageFixture()
	oldUsage := src.dirUsage
	var measured []string
	src.cacheBound = func(_ context.Context, box storageBox) (bool, error) { return box.Name != "idle", nil }
	src.dirUsage = func(ctx context.Context, path string) (uint64, error) {
		if strings.HasPrefix(path, boxcache.Root+"/") {
			measured = append(measured, path)
			return testGiB / 4, nil
		}
		return oldUsage(ctx, path)
	}
	r := collectSpaceStorage(t.Context(), src, time.Now())
	if len(r.Errors) != 0 || len(measured) != 2 || r.Other == nil {
		t.Fatalf("report: %+v, measured %v", r, measured)
	}
	var sum uint64
	for _, b := range r.Boxes {
		if b.Bytes != nil {
			sum += *b.Bytes
		}
		if !b.Declared {
			if b.Cache != nil {
				t.Fatal("foreign container has a box cache")
			}
			continue
		}
		if b.Cache == nil {
			t.Fatal("missing cache row")
		}
		if b.Name == "idle" {
			if !b.Cache.InLayer || b.Cache.Bytes != nil {
				t.Fatalf("legacy cache %+v", b.Cache)
			}
		} else {
			if b.Cache.Bytes == nil || *b.Cache.Bytes != testGiB/4 || b.Cache.InLayer {
				t.Fatalf("bound cache %+v", b.Cache)
			}
			sum += *b.Cache.Bytes
		}
	}
	sum += r.Images.Bytes + r.BuildKit.Bytes + *r.Other
	if sum != r.System.Used {
		t.Fatalf("cache counted more than once: %d != %d", sum, r.System.Used)
	}
	var out bytes.Buffer
	if err := writeSpaceStorage(&out, "work", r); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "    caches") != 3 || !strings.Contains(out.String(), "in the layer") {
		t.Fatal(out.String())
	}
}

func TestSpaceStorageUnknownCacheIsNotTheRemainder(t *testing.T) {
	for _, failure := range []string{"inspect", "walk"} {
		t.Run(failure, func(t *testing.T) {
			src := storageFixture()
			oldUsage := src.dirUsage
			src.cacheBound = func(_ context.Context, b storageBox) (bool, error) {
				if failure == "inspect" && b.Name == "opus" {
					return false, errors.New("inspect unavailable")
				}
				return true, nil
			}
			src.dirUsage = func(ctx context.Context, p string) (uint64, error) {
				if strings.HasPrefix(p, boxcache.Root+"/") {
					if failure == "walk" && strings.HasSuffix(p, "/opus") {
						return 0, errors.New("walk unavailable")
					}
					return testGiB / 4, nil
				}
				return oldUsage(ctx, p)
			}
			r := collectSpaceStorage(t.Context(), src, time.Now())
			if r.Other != nil {
				t.Fatal("unknown cache silently folded into remainder")
			}
			for _, b := range r.Boxes {
				if b.Name == "opus" && (b.Cache == nil || b.Cache.Bytes != nil || b.Cache.InLayer) {
					t.Fatalf("unknown cache %+v", b.Cache)
				}
			}
			var out bytes.Buffer
			if err := writeSpaceStorage(&out, "work", r); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "not measured") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestSpaceStorageSlowCacheRetainsMeasuredLayers(t *testing.T) {
	src := storageFixture()
	src.cacheBound = func(ctx context.Context, _ storageBox) (bool, error) { <-ctx.Done(); return false, ctx.Err() }
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	r := collectSpaceStorage(ctx, src, time.Now())
	for _, b := range r.Boxes {
		if b.Bytes == nil {
			t.Fatalf("completed layer lost: %+v", b)
		}
	}
	if r.Other != nil {
		t.Fatal("slow cache became remainder")
	}
}
