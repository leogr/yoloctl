package main

import (
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// noticeIgnoredRuntime logs, per Feature, every runtime request yoloctl
// ignores (init, capAdd, securityOpt, mounts, lifecycle commands…), one per
// line. It is a notice record of the process logger, which escapes
// registry-supplied text and honours the log level; a worker's warm
// preparation pass may repeat it.
func noticeIgnoredRuntime(ignored []ocifeature.IgnoredFeature) {
	for _, f := range ignored {
		lines := make([]string, 0, len(f.Requests))
		for _, r := range f.Requests {
			if r.Value == "true" {
				lines = append(lines, r.Key)
			} else {
				lines = append(lines, r.Key+" "+r.Value)
			}
		}
		cliLogLines(prettylog.LevelNotice, logging.Box, "feature "+f.Ref+": ignored runtime requests (yoloctl controls the runtime; debuggers may not work):", lines)
	}
}
