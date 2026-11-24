# Welcome tips

Verification: `dfd9bbff5905889ba48bdfbe17b78ee31a844b15` and the accompanying welcome interaction change; the missing-space and save-default tips on `b44aebcad9b93b2efaee1db48e34cb2f5982d4df` and their accompanying change.

[FACT] The bare host TUI runs a background observation for its pinned space
when it first shows the idle no-tabs screen, returns to it after tabs or a
palette command, or switches spaces while it is showing. There is no polling.
Leaving the screen cancels its pending read, and replies from an earlier visit
cannot populate a later one. While the read is outstanding or inconclusive,
the screen shows a generic command hint with three examples on separate lines.
Bound native/operator clients keep that hint. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The first confirmed missing step wins in this order. No spaces in
the host inventory, selected space stopped, older running release, no
operator declaration, managed chat setup required, operator unenrolled,
operator the only box besides the chat server. A selected space missing from
a nonempty inventory gets a tip that names the space chip and the pane prefix
then `s`; the selection stays pinned until the user switches. An error
checking a candidate bundle leaves the inventory unknown and keeps the generic
hint. [4](#source-4) [9](#source-9)
The observer uses existing VM lookup, guest version, controller box inventory
and chat status reads, without boot or controller installation. Bundle IDs do
not express ordering. The upgrade tip requires validated release tags and a
guest version response that matches the selected bundle. [4](#source-4)
[5](#source-5) [6](#source-6)

[FACT] After the TUI moves to a space it just created from its palette, its
next idle visit reads nothing and shows "Tip: to save SPACE as your default,
type:" with `space use SPACE` to fill. The following visit reads again.
[2](#source-2) [11](#source-11)

[FACT] Operator eligibility uses the effective policy grant. Active enrollment
matches the operator's declaration identity. The chat server is counted
separately using its name and declaration identity. An incomplete observation
or a change in that identity between reads keeps the generic hint. When all
steps are complete the view picks one random tip for the visit.
[2](#source-2) [6](#source-6)

[FACT] A click on underlined command text fills an empty palette input and
keeps any open notice. In the enrollment tip both the enroll and attach
commands are clickable. Enter submits the input when the palette has keyboard
focus. Dragging over welcome text selects it for copying
through the existing host clipboard policy. [7](#source-7) [10](#source-10)
The first-space tip uses the
working directory and the host login if it is a valid account name. Otherwise
the account suggestion is `personal`. The shown directory stays explicit.
For a directory name the whitespace-split palette cannot express in one
token, the filled command uses `.` in the same directory. [2](#source-2)
[8](#source-8)

## Sources

1. <a id="source-1"></a> [TUI lifecycle](../../internal/tui/tui.go#L212-L258) — `Run`.
2. <a id="source-2"></a> [Welcome model](../../internal/tui/welcome.go#L63) — `syncWelcome`, `makeWelcomeTip`, `welcomeRows`.
3. <a id="source-3"></a> [TUI update](../../internal/tui/tui.go#L917-L988) — scheduling and reply handling.
4. <a id="source-4"></a> [Welcome observation](../../cmd/yoloctl/tui_welcome.go#L24-L66) — host bootstrap and read paths.
5. <a id="source-5"></a> [Version ordering](../../cmd/yoloctl/tui_welcome.go#L102-L117) — `olderWelcomeRelease`.
6. <a id="source-6"></a> [Box and chat observation](../../cmd/yoloctl/tui_welcome.go#L119) — `welcomeBoxStatus`.
7. <a id="source-7"></a> [Welcome mouse interaction](../../internal/tui/welcome_mouse.go#L21) — hit testing, click to fill and drag to copy.
8. <a id="source-8"></a> [Palette editor](../../internal/tui/palette/palette.go#L114-L127) — `Submit` and whitespace tokenization.
9. <a id="source-9"></a> [Complete space listing](../../internal/spaceconfig/spaceconfig.go#L182) — `ListStrict`.
10. <a id="source-10"></a> [Host clipboard](../../internal/tui/tui.go#L296) — `osc52HostMsg` applies the clipboard setting.
11. <a id="source-11"></a> [Created-space follow](../../internal/tui/space_follow.go#L1) — `followCreated` and `acceptSpaceSwitch` set `saveDefault`.
