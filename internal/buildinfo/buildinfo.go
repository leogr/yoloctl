// Package buildinfo defines the release identity shared by hosts and guests.
package buildinfo

import (
	"regexp"
	"strings"
)

// Version is stamped by the coordinated build. Arbitrary stamps remain dev.
var Version = "dev"

var releasePattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// IsRelease accepts full SemVer tags without build metadata or numeric leading zeros.
func IsRelease(v string) bool {
	if !releasePattern.MatchString(v) {
		return false
	}
	if _, pre, ok := strings.Cut(v, "-"); ok {
		for _, part := range strings.Split(pre, ".") {
			numeric := true
			for _, c := range part {
				if c < '0' || c > '9' {
					numeric = false
				}
			}
			if numeric && len(part) > 1 && part[0] == '0' {
				return false
			}
		}
	}
	return true
}

func Channel() string {
	if IsRelease(Version) {
		return "release"
	}
	return "dev"
}

// HomeLeaf is the config/cache leaf for every channel. Development and
// release builds share the default home (leogr's option B); a separate home
// is opt-in through an explicit YOLOCTL_HOME.
func HomeLeaf() string { return "yoloctl" }

// CompareRelease compares validated release tags using SemVer ordering.
func CompareRelease(a, b string) int {
	aCore, aPre, aHas := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	bCore, bPre, bHas := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	for i, part := range strings.Split(aCore, ".") {
		other := strings.Split(bCore, ".")[i]
		if len(part) != len(other) {
			if len(part) < len(other) {
				return -1
			}
			return 1
		}
		if c := strings.Compare(part, other); c != 0 {
			return c
		}
	}
	if !aHas && !bHas {
		return 0
	}
	if !aHas {
		return 1
	}
	if !bHas {
		return -1
	}
	left, right := strings.Split(aPre, "."), strings.Split(bPre, ".")
	for i, part := range left {
		if i >= len(right) {
			return 1
		}
		other := right[i]
		numeric := func(value string) bool { return strings.Trim(value, "0123456789") == "" }
		an, bn := numeric(part), numeric(other)
		if an != bn {
			if an {
				return -1
			}
			return 1
		}
		if an && len(part) != len(other) {
			if len(part) < len(other) {
				return -1
			}
			return 1
		}
		if c := strings.Compare(part, other); c != 0 {
			return c
		}
	}
	if len(left) < len(right) {
		return -1
	}
	return 0
}
