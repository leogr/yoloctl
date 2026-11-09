package chatui

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Compatibility names keep chat interaction code on the single shared renderer.
type piece = markdownview.Piece
type displayLine = markdownview.DisplayLine
type tableInfo = markdownview.TableInfo
type bodyRow = markdownview.BodyRow
type srcMap = markdownview.SourceMap
type lineSrc = markdownview.LineSource
type run = markdownview.Run
type action = markdownview.Action
type actionKind = markdownview.ActionKind
type quoteLine = markdownview.QuoteLine

const copyLabel = markdownview.CopyLabel
const fenceNone = markdownview.FenceNone
const fenceOpen = markdownview.FenceOpen
const maxBodyRows = markdownview.MaxBodyRows
const strikeOn = markdownview.StrikeOn
const maxTableRows = markdownview.MaxTableRows
const maxTableColumns = markdownview.MaxTableColumns
const tableFloor = markdownview.TableFloor
const tableSlack = markdownview.TableSlack

var actionAt = markdownview.ActionAt
var cardPad = markdownview.CardPad
var closesFence = markdownview.ClosesFence
var codeCard = markdownview.CodeCard
var codeCards = markdownview.CodeCards
var deco = markdownview.Deco
var displayWidth = markdownview.DisplayWidth
var expandTabsMap = markdownview.ExpandTabsMap
var fenceMarker = markdownview.FenceMarker
var fit = markdownview.Fit
var fitTable = markdownview.FitTable
var hitRun = markdownview.HitRun
var inlineMarkdown = markdownview.InlineMarkdown
var linked = markdownview.Linked
var overlay = markdownview.Overlay
var paint = markdownview.Paint
var plain = markdownview.Plain
var runsWidth = markdownview.RunsWidth
var slicesCloneRuns = markdownview.CloneRuns
var subAt = markdownview.ReplacementBounds
var subMap = markdownview.ReplacementSourceMap
var tableCells = markdownview.TableCells
var tableRowRule = markdownview.TableRowRule
var tableText = markdownview.TableText
var truncate = markdownview.Truncate
var truncateLeft = markdownview.TruncateLeft
var waterFill = markdownview.WaterFill
var widestGrapheme = markdownview.WidestGrapheme
var wrapRanges = markdownview.WrapRanges

func chatDecorator(me string) markdownview.Decorator {
	return func(text string, pieces []piece, theme spaceview.Theme, prose bool) []piece {
		return decorateTokens(text, pieces, theme, me, prose)
	}
}
func displayLines(wm ansi.Method, text string, source bool, theme spaceview.Theme, me string) []displayLine {
	return markdownview.Parse(text, markdownview.ParseOptions{Method: wm, Theme: theme, Source: source, Decorate: chatDecorator(me)}).Lines
}
func displayLinesMap(wm ansi.Method, text string, source bool, theme spaceview.Theme, me string, srcs *[]lineSrc) []displayLine {
	doc := markdownview.Parse(text, markdownview.ParseOptions{Method: wm, Theme: theme, Source: source, MapSource: srcs != nil, Decorate: chatDecorator(me)})
	if srcs != nil {
		if *srcs == nil {
			*srcs = doc.Sources
		} else {
			*srcs = append(*srcs, doc.Sources...)
		}
	}
	return doc.Lines
}
func bodyRows(wm ansi.Method, lines []displayLine, width int, theme spaceview.Theme) []bodyRow {
	return markdownview.Layout(markdownview.Document{Lines: lines}, width, markdownview.LayoutOptions{Method: wm, Theme: theme}).Rows
}
