package vmapi

import "golang.org/x/sys/unix"

// ReadNestedHostFacts reads the two facts used by auto's conservative heuristic.
func ReadNestedHostFacts() NestedHostFacts {
	chip, _ := unix.Sysctl("machdep.cpu.brand_string")
	version, _ := unix.Sysctl("kern.osproductversion")
	return NestedHostFacts{OS: "darwin", Chip: chip, Version: version}
}
