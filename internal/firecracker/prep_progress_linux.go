//go:build linux && amd64

package firecracker

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// VM preparation in the helper (hashing the guest artifact, formatting a disk,
// building the boot disk or a system-disk seed) can take minutes on a slow
// host. Its stage records go to the helper's stderr, which is the instance's
// vm.log, where Driver.Start's boot activity watch counts them as progress.
// Records are emitted only for completed work: a stage's begin and end, and
// a byte count at most every prepProgressGap while reads or writes complete.
// There is no independent heartbeat, so a blocked read, write or fsync still
// reaches the boot idle limit.
var (
	prepReport = func(format string, args ...any) {
		_, _ = fmt.Fprintf(os.Stderr, "yoloctl: VM preparation: "+format+"\n", args...)
	}
	prepProgressGap = 30 * time.Second
)

type prepProgress struct {
	stage string
	done  int64
	last  time.Time
}

func newPrepProgress(stage string) *prepProgress {
	prepReport("%s begin", stage)
	return &prepProgress{stage: stage, last: time.Now()}
}

// add records n bytes of completed work.
func (p *prepProgress) add(n int) {
	if p == nil || n <= 0 {
		return
	}
	p.done += int64(n)
	if time.Since(p.last) >= prepProgressGap {
		p.last = time.Now()
		prepReport("%s progress %d bytes", p.stage, p.done)
	}
}

func (p *prepProgress) end() {
	if p != nil {
		prepReport("%s end (%d bytes)", p.stage, p.done)
	}
}

type prepProgressKey struct{}

// withPrepProgress makes contextDisk writes under ctx count as p's work.
func withPrepProgress(ctx context.Context, p *prepProgress) context.Context {
	return context.WithValue(ctx, prepProgressKey{}, p)
}

func prepProgressFrom(ctx context.Context) *prepProgress {
	p, _ := ctx.Value(prepProgressKey{}).(*prepProgress)
	return p
}

// prepReader counts completed reads as p's work.
type prepReader struct {
	r io.Reader
	p *prepProgress
}

func (r prepReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	r.p.add(n)
	return n, err
}
