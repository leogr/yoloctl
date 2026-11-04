package resources

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

var memorySize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([kmgtpe]?)(?:i?b)?$`)

// BoxMemory returns a byte limit; zero means sharing the VM workload pool
// without an additional per-box limit. Binary suffixes match nerdctl's units.
func BoxMemory(s string) (int64, error) {
	if s == "" || s == "shared" {
		return 0, nil
	}
	m := memorySize.FindStringSubmatch(strings.ToLower(s))
	if m != nil {
		n, ok := new(big.Rat).SetString(m[1])
		if ok {
			shift := uint(0)
			if m[2] != "" {
				shift = uint(strings.Index("kmgtpe", m[2])+1) * 10
			}
			n.Mul(n, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), shift)))
			v := new(big.Int).Quo(n.Num(), n.Denom())
			if v.IsInt64() && (v.Sign() > 0 || n.Sign() == 0) {
				return v.Int64(), nil
			}
		}
	}
	return 0, fmt.Errorf("invalid box memory %q; use shared or a size such as 512m or 2g", s)
}

// CPUQuota matches nerdctl's 100000us CFS period and truncation.
func CPUQuota(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n*100000 >= math.MaxInt64 || (n > 0 && n*100000 < 1) {
		return 0, fmt.Errorf("invalid CPU limit %q", s)
	}
	return int64(n * 100000), nil
}
