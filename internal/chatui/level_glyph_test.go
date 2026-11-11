package chatui

import (
	"testing"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Chat notices use the shared log glyphs and the theme's warning tone.
func TestNoticeLevelsUseSharedGlyphs(t *testing.T) {
	m, _, _ := channelModel(t)
	g := prettylog.DefaultGlyphs
	for _, c := range []struct {
		level noticeLevel
		icon  string
	}{
		{levelInfo, g.Info}, {levelOK, g.OK}, {levelWarn, g.Warn}, {levelError, g.Error},
	} {
		m.say(c.level, "text")
		runs := m.noticeRuns()
		if runs[0].Text != " "+c.icon+" " {
			t.Errorf("level %d: icon %q want %q", c.level, runs[0].Text, c.icon)
		}
		if c.level == levelWarn && runs[0].Style.FG != m.theme.Warn {
			t.Errorf("warning tone %v, want theme Warn %v", runs[0].Style.FG, m.theme.Warn)
		}
	}
}
