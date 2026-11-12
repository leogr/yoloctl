package spaceview

import "github.com/leogr/yoloctl/pkg/prettylog"

// LogTone exposes semantic log roles without copying theme colour values.
func (t Theme) LogTone(kind prettylog.Kind) Color {
	switch kind {
	case prettylog.KindError:
		return t.Bad
	case prettylog.KindWarn, prettylog.KindUnknown:
		return t.Warn
	case prettylog.KindNotice:
		return t.Label
	case prettylog.KindOK:
		return t.OK
	case prettylog.KindDebug, prettylog.KindSkipped:
		return t.Dim
	default:
		return t.Text
	}
}
