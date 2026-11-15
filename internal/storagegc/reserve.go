package storagegc

import (
	"fmt"
	"strconv"
	"strings"
)

const DefaultBuildReserve = uint64(10) << 30

// Reserve limits only build admission. A nil Cap retains the uncapped 20%
// requirement; cleanup's TargetFree/Low and BuildKit policy never use this cap.
type Reserve struct{ Cap *uint64 }

// ParseReserve keeps omission (the default), none and an explicit zero distinct.
// Sizes are whole GiB; accepting zero does not disable cleanup or its errors.
func ParseReserve(value *string) (Reserve, error) {
	cap := DefaultBuildReserve
	if value == nil {
		return Reserve{Cap: &cap}, nil
	}
	if *value == "none" {
		return Reserve{}, nil
	}
	amount, ok := strings.CutSuffix(*value, "GiB")
	if !ok || amount == "" || strings.Trim(amount, "0123456789") != "" {
		return Reserve{}, fmt.Errorf("build reserve must be a whole GiB size (including 0GiB) or none")
	}
	n, err := strconv.ParseUint(amount, 10, 64)
	if err != nil || n > ^uint64(0)>>30 {
		return Reserve{}, fmt.Errorf("build reserve size overflows bytes")
	}
	cap = n << 30
	return Reserve{Cap: &cap}, nil
}

func (r Reserve) AdmissionFree(d Disk) uint64 {
	if r.Cap == nil {
		return d.TargetFree()
	}
	return min(d.TargetFree(), *r.Cap)
}

func (r Reserve) Admits(d Disk) bool { return d.Available >= r.AdmissionFree(d) }
