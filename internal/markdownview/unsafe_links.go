package markdownview

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// UnsafeDestinations retains the admission consequence of raw controls that
// Plain removes from display. Reuse the existing block/inline grammar so code
// and escaped text do not poison otherwise valid destinations. This analysis
// never paints its unsanitized intermediate document or performs any I/O.
func UnsafeDestinations(text string) map[string]bool {
	unsafe := func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }
	if !strings.ContainsFunc(text, unsafe) {
		return nil
	}
	denied := map[string]bool{}
	parseLines(ansi.WcWidth, text, false, spaceview.Theme{}, nil, nil, nil, func(destination string) Action {
		if strings.ContainsFunc(destination, unicode.IsControl) {
			denied[Plain(destination)] = true
		}
		return Action{}
	}, true)
	return denied
}
