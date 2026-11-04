# TUI pane chrome

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] In a tab with two or more panes, zoomed or not, every visible pane has
a **bar**: the row below its content, across its width.
- A pane stacked above another uses the horizontal border row as its bar.
- A pane on the tab's bottom edge uses the tab's last row.
- Layout, PTY size, cursor, selection and hit-testing use the content
  rectangle (`Rect`); only the composer and bar hit-testing use `Bar`.
- An undivided tab has no bar.
[1](#source-1) [2](#source-2)

[FACT] A bar is a **band** across its row: orange 215 on the focused pane,
blue 67 (the inactive tab's) on the others. It reads, left to right:
- `▸` on the focused pane's bar;
- a bold orange `●` attention mark on an unfocused pane;
- the title (`Pane.Title`, strip width method) with `· zoomed` or `· exited`;
- band, then ` ⤢ ` (zoom) and ` ✕ ` (detach), each on its own background
  (208 with dark 235 glyphs on the focused bar, 60 with light 255 glyphs on
  the others) and followed by one band cell. No split buttons, no words. An empty pane's bar
  reads `empty` with `✕` only.

As columns run out, the title truncates with `…` and then goes; then `⤢`;
then `✕`. At two columns the bar is plain band. Text is dark: 235 with a
bold title on the focused bar, 232 on the others. No bar is underlined.
[2](#source-2) [3](#source-3)

[FACT] A bar above another pane is the horizontal border. Vertical borders
are blue, orange beside the focused pane from its first content row to its
own bar row (the bar row above it is that pane's, blue). A border cell on a
bar row is never painted by a band and has no junction glyph: beside the
focused bar it is a half block leaning toward it (`▐` when the bar is to its
right, `▌` to its left), elsewhere a `│` in the border's colour. There is no
outer frame. A dragged border or taken drop target turns its border glyphs
orange. [3](#source-3)

[FACT] A split counts the bars it adds: both halves must keep 20×5 cells of
content once laid out. A first horizontal split fits in 12 rows (5, a bar, 5,
the bottom bar), not in 11. [1](#source-1) [4](#source-4)

[FACT] Every tab label in the strip ends in ` ✕`: dim with the strip's blue
underline on an inactive tab, orange on the active one. The ✕ is drawn after
the (possibly clamped) label, so an oversized tab keeps its number and ✕;
windowing counts its two columns. [6](#source-6)

[FACT] A left press on the strip, a bar or a vertical border (its bar-row
cell included) is a **chrome gesture**: a consumed owner no child receives,
with its objects (`*Tab`, `*Pane`, split) captured at the press.
- **Release activation:** a tab label activates its tab; a tab's ✕ closes
  every pane's view without activating it; a bar's `⤢` zooms, its ✕ closes
  its pane (a running command that may end asks first, see
  [close confirmation](tui-close-confirm.md)). Each acts only when the release
  is over the same object's current button or label; anything else cancels.
- **Title:** a press on a bar's title or marker focuses its pane at once.
- **Dock:** a label moved by a cell docks its tab ([moves](tui-pane-moves.md)).
- **Resize:** a press on a border, or on a bar between stacked panes except
  `⤢`/`✕`, drags that split and focuses its pane (a click only focuses); a
  bottom-edge bar never resizes. The preview, in orange, takes the nearest
  position where a trial layout keeps every pane at the minimum; sizes commit
  once on release; the grab offset is kept; a host resize reruns the trial.
- **Cancel:** Esc (consumed), host focus loss, a tab switch, its objects
  disappearing, any other layout change (`Layout.Generation`; not a host
  resize), or another button's press (consumed).
- While held, hover and the wheel are consumed; earlier gestures of other
  buttons finish at their owners; a legacy buttonless release ends the
  chrome gesture first. A press in a child's content is never chrome.
[7](#source-7) [8](#source-8)

[FACT] Tests cover `CanSplit` with bars, hit-testing and the preview's trial
clamp. PTY model tests cover the buttons, the strip ✕, release activation, the
same-object rule, pixel mode, drags with every cancel, other buttons' and
legacy releases, the separator-bar handle and a child drag crossing a bar.
Goldens through the real terminal core cover three panes (colours, `│` and
`▌` on bar rows), five panes (`▐` and `▌` around the focused bar), a stacked
column with the focused pane in the middle, a short frame, zoom, the mark and `exited`, the preview, the
degradation order and wide titles.
[4](#source-4) [5](#source-5) [9](#source-9)

## Sources

1. <a id="source-1"></a> [Layout geometry](../../internal/tui/panes/layout.go#L195-L530) — `CanSplit`, `Split`, `Resize`, `place`; `Pane.Rect`/`Bar` at L56-L62.
2. <a id="source-2"></a> [Bar layout](../../internal/tui/panes/layout.go#L672-L795) — `BarPart`, `barButtons`, `barSegs`, `BarAt`.
3. <a id="source-3"></a> [Composer](../../internal/tui/panes/compose.go#L11-L319) — `barColors`, `Compose`, `drawBorders`, `drawBar`.
4. <a id="source-4"></a> [Layout tests](../../internal/tui/panes/layout_test.go#L221-L365) — `TestCanSplitCountsBars`, `TestBarAndBorderHitTesting`, `TestPreviewClampsAndCommitsOnce`, `TestPreviewKeepsEveryPaneAtSkewedRatios`.
5. <a id="source-5"></a> [Composer tests](../../internal/tui/panes/compose_test.go#L74-L386) — goldens in panes/testdata.
6. <a id="source-6"></a> [Strip](../../internal/tui/tui.go#L2343-L2517) — `tabCloseW`, `tabSegments`, `tabStrip`, `tabCloseAt`.
7. <a id="source-7"></a> [Chrome gestures](../../internal/tui/chrome_gestures.go#L1-L304) — `chromeAt`, `startChrome`, `chromeMotion`, `chromeRelease`, `cancelChrome`, `checkChrome`, `detachPane`, `detachTab`; `tabs.Model.CloseTab`.
8. <a id="source-8"></a> [Mouse routing](../../internal/tui/mouse.go#L339-L398) — `chromeMouse`; Esc and focus loss in tui.go `Update`.
9. <a id="source-9"></a> [Gesture tests](../../internal/tui/chrome_gestures_test.go#L1-L539) — `TestStripCloseAndReleaseActivation`, `TestResizeDragPreviewsThenCommitsOnce`, `TestSeparatorBarIsASplitHandle` and the others.
