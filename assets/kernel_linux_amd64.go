package assets

import "embed"

// VMKernel contains the release-built kernel and its checked source/configuration
// provenance. Kernel compilation is a maintainer operation, never first use.
//
//go:embed vm/firecracker/kernel-linux-amd64.gz vm/firecracker/kernel.config vm/firecracker/kernel.json
var VMKernel embed.FS
