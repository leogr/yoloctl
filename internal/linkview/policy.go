// Package linkview adapts already-admitted file targets to pure Markdown
// intents. Transport and path authority do not depend on the view renderer.
package linkview

import (
	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/markdownview"
)

func Policy(admitted filelink.Admissions, raw ...string) markdownview.LinkPolicy {
	var denied map[string]bool
	if len(raw) > 0 {
		denied = markdownview.UnsafeDestinations(raw[0])
	}
	return func(destination string) markdownview.Action {
		if denied[destination] {
			return markdownview.Action{}
		}
		if _, ok := admitted[destination]; ok {
			return markdownview.Action{Kind: markdownview.IntentLocalFile, Value: destination}
		}
		if filelink.HTTP(destination) {
			return markdownview.Action{Kind: markdownview.IntentURL, Value: destination}
		}
		// An inert destination stays available for background admission/inspection.
		return markdownview.Action{Value: destination}
	}
}
