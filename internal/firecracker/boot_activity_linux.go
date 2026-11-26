//go:build linux

package firecracker

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/rotatelog"
)

const (
	bootIdleTimeout       = 2 * time.Minute
	bootReadyProbeTimeout = 10 * time.Second
)

// bootActivity observes validated current log generations without replaying
// retained history. Rotation is coarse activity, never proof of readiness.
type bootActivity struct {
	previous os.FileInfo
	last     time.Time
}

func newBootActivity(path string, now time.Time) (*bootActivity, error) {
	st, err := bootLogStat(path)
	if err != nil {
		return nil, err
	}
	return &bootActivity{previous: st, last: now}, nil
}
func bootLogStat(path string) (os.FileInfo, error) {
	f, err := rotatelog.OpenRead(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Stat()
}
func (a *bootActivity) observe(path string, now time.Time) error {
	st, err := bootLogStat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("observe boot output %s: %w", path, err)
	}
	if err == nil {
		if (os.SameFile(a.previous, st) && st.Size() > a.previous.Size()) || (!os.SameFile(a.previous, st) && st.Size() > 0) {
			a.last = now
		}
		a.previous = st
	}
	if now.Sub(a.last) >= bootIdleTimeout {
		return fmt.Errorf("VM boot produced no new output for %s; inspect %s", bootIdleTimeout, path)
	}
	return nil
}
