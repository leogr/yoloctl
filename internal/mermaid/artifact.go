// Package mermaid contains the pinned, host-side Mermaid text renderer.
package mermaid

import _ "embed"

// Normal builds need only Go; regeneration uses scripts/build-mermaid.py.
//
//go:embed wasm/mermaid.wasm
var artifact []byte

//go:embed wasm/manifest.json
var artifactManifest []byte
