package logtheme

import (
	"bytes"
	"image/color"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestPaletteFollowsSpaceviewTheme(t *testing.T) {
	for _, dark := range []bool{true, false} {
		p := Palette(dark, nil)
		th := spaceview.NewTheme(dark, nil, nil)
		idx, _, _, _, _ := th.Bad.Spec()
		if p.Error != prettylog.Index(uint8(idx)) {
			t.Fatalf("dark=%v: error %v, theme index %d", dark, p.Error, idx)
		}
	}
	if Palette(true, nil) == Palette(false, nil) {
		t.Fatal("light and dark palettes are equal")
	}
}

func TestSetBackgroundAppliesLightTheme(t *testing.T) {
	var b bytes.Buffer
	l := prettylog.New(&b, prettylog.Options{Format: prettylog.FormatPretty, Color: true, Verbosity: prettylog.Normal})
	defer logging.SetLogger(l)()
	defer logging.SetPalette(prettylog.ANSIPalette)
	SetBackground(nil) // unknown: unchanged
	SetBackground(color.White)
	logging.L().Warn("x")
	idx, _, _, _, _ := spaceview.NewTheme(false, nil, nil).Warn.Spec()
	if want := "38;5;" + strconv.Itoa(idx); !strings.Contains(b.String(), want) {
		t.Fatalf("%q lacks %s", b.String(), want)
	}
}
