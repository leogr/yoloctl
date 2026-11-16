//go:build linux

package firecracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/rotatelog"
)

func TestBootActivityRotatesAndThenExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm.log")
	original, err := rotatelog.OpenStartupFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = original.Close() }()
	if _, err := original.WriteString("old history"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	activity, err := newBootActivity(path, now)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := rotatelog.Open(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	for i := 0; i < 6; i++ {
		now = now.Add(90 * time.Second)
		if _, err := writer.Write([]byte(strings.Repeat("x", 16))); err != nil {
			t.Fatal(err)
		}
		if err := activity.observe(path, now); err != nil {
			t.Fatal(err)
		}
	}
	current, err := bootLogStat(path)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(stale, current) {
		t.Fatal("fixture did not rotate original descriptor")
	}
	if err := activity.observe(path, now.Add(bootIdleTimeout)); err == nil {
		t.Fatal("silence did not expire")
	}
}

func TestBootActivityHistoryTruncationAndGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm.log")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	activity, err := newBootActivity(path, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := activity.observe(path, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if activity.last != start {
		t.Fatal("history renewed idle")
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if err := activity.observe(path, start.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if activity.last != start {
		t.Fatal("truncation renewed idle")
	}
	// Rename simulates the writer's publication gap; absence is not activity.
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := activity.observe(path, start.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := activity.observe(path, start.Add(110*time.Second)); err != nil {
		t.Fatal(err)
	}
	if activity.last != start {
		t.Fatal("empty replacement renewed idle")
	}
	if err := activity.observe(path, start.Add(bootIdleTimeout)); err == nil {
		t.Fatal("old bytes kept boot alive")
	}
	// New bytes in the new valid generation, unlike its empty publication, count.
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := activity.observe(path, start.Add(119*time.Second)); err != nil {
		t.Fatal(err)
	}
	if activity.last != start.Add(119*time.Second) {
		t.Fatal("fresh bytes not observed")
	}
}

func TestBootActivityRefusesUnsafeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm.log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	activity, err := newBootActivity(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".old", path); err != nil {
		t.Fatal(err)
	}
	if err := activity.observe(path, time.Now()); err == nil {
		t.Fatal("symlink accepted")
	}
}
