package agentbox

import (
	"strings"

	"github.com/leogr/yoloctl/internal/ocifeature"
)

const (
	ociGoRef   = "ghcr.io/devcontainers/features/go:1"
	ociRustRef = "ghcr.io/devcontainers/features/rust:1"
	// Upstream containerEnv and runtime requests of go:1 and rust:1.
	ociGoMetadata   = `{"id":"go","version":"1.3.2","containerEnv":{"GOROOT":"/usr/local/go","GOPATH":"/go","PATH":"/usr/local/go/bin:/go/bin:${PATH}"},"init":true,"capAdd":["SYS_PTRACE"],"securityOpt":["seccomp=unconfined"]}`
	ociRustMetadata = `{"id":"rust","version":"1.3.1","containerEnv":{"CARGO_HOME":"/usr/local/cargo","RUSTUP_HOME":"/usr/local/rustup","PATH":"/usr/local/cargo/bin:${PATH}"},"capAdd":["SYS_PTRACE"],"securityOpt":["seccomp=unconfined"]}`
)

func ociLock(ref, digestChar, metadata string) ocifeature.Lock {
	return ocifeature.Lock{Ref: ref, Digest: "sha256:" + strings.Repeat(digestChar, 64), Layer: ocifeature.Descriptor{Digest: "sha256:" + strings.Repeat("f", 64), MediaType: "application/vnd.devcontainers.layer.v1+tar", Size: 123}, Metadata: metadata}
}

func ociGoRustLocks() []ocifeature.Lock {
	return []ocifeature.Lock{ociLock(ociGoRef, "a", ociGoMetadata), ociLock(ociRustRef, "b", ociRustMetadata)}
}
