//go:build !darwin

package vmapi

import "runtime"

func ReadNestedHostFacts() NestedHostFacts { return NestedHostFacts{OS: runtime.GOOS} }
