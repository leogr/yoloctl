package spacetelemetry

// DiskLevel is how close a disk is to running out of space.
type DiskLevel int

const (
	DiskOK DiskLevel = iota
	DiskWarn
	DiskCritical
)

// Disk pressure thresholds. Free space comes first because writers fail at
// zero regardless of size; 4 GiB is also BuildKit's cleanup cap, so the
// warning fires while automatic cleanup can still help. A small disk's free
// floor shrinks to a share of its size, so it is not flagged half empty.
const (
	DiskWarnFree      = 4 << 30
	DiskCriticalFree  = 2 << 30
	DiskWarnShare     = 0.15
	DiskCriticalShare = 0.10
	DiskWarnUsed      = 0.85
	DiskCriticalUsed  = 0.95
)

// Used is the space no unprivileged writer can use, filesystem reserve
// included.
func (u Usage) Used() uint64 { return u.Total - min(u.Free, u.Total) }

// WarnFree is the free space below which the disk warns: 4 GiB, or 15% of
// a disk smaller than about 27 GiB.
func (u Usage) WarnFree() uint64 {
	return min(DiskWarnFree, uint64(float64(u.Total)*DiskWarnShare))
}

// CriticalFree is the free space below which the disk is almost full: 2 GiB,
// or 10% of a disk smaller than 20 GiB.
func (u Usage) CriticalFree() uint64 {
	return min(DiskCriticalFree, uint64(float64(u.Total)*DiskCriticalShare))
}

// Pressure classifies a disk: warn below WarnFree or from 85% used, critical
// below CriticalFree or from 95% used. An empty disk size is unknown, never
// fine; callers keep nil or zero usage out of this check.
func (u Usage) Pressure() DiskLevel {
	if u.Total == 0 {
		return DiskOK
	}
	used := float64(u.Used()) / float64(u.Total)
	switch {
	case u.Free < u.CriticalFree() || used >= DiskCriticalUsed:
		return DiskCritical
	case u.Free < u.WarnFree() || used >= DiskWarnUsed:
		return DiskWarn
	}
	return DiskOK
}
