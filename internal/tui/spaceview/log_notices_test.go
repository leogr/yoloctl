package spaceview

import (
	"image/color"
	"testing"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestToastKindsUseThemeRoles(t *testing.T) {
	for _, dark := range []bool{false, true} {
		bg := color.White
		if dark {
			bg = color.Black
		}
		theme := NewTheme(dark, bg, nil)
		for kind, want := range map[prettylog.Kind]Color{
			prettylog.KindError: theme.Bad, prettylog.KindWarn: theme.Warn,
			prettylog.KindUnknown: theme.Warn, prettylog.KindNotice: theme.Label,
			prettylog.KindOK: theme.OK, prettylog.KindInfo: theme.Text,
			prettylog.KindDebug: theme.Dim, prettylog.KindSkipped: theme.Dim,
		} {
			if got := theme.LogTone(kind); got != want {
				t.Fatalf("dark=%t kind=%v got=%v want=%v", dark, kind, got, want)
			}
		}
	}
	m := New(t.Context(), Options{})
	m.say("opened", false)
	if m.toast.kind != prettylog.KindNotice {
		t.Fatalf("notice kind: %+v", m.toast)
	}
	m.say("refused", true)
	if m.toast.kind != prettylog.KindError {
		t.Fatalf("error kind: %+v", m.toast)
	}
}
