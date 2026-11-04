package main

import (
	"github.com/leogr/yoloctl/internal/nativeartifacts"
	"github.com/leogr/yoloctl/internal/openshell"
)

// Native installation reads its qualified release archives from this binary.
func init() { openshell.UseEmbeddedArchives(nativeartifacts.ReleaseArchive) }
