# Welcome tips

Verification: `f3a4aa1ef43a52b2066320fd00990d6cd6527109` and the accompanying welcome-tip change.

[FACT] The bare host TUI runs a background observation for its pinned space
when it first shows the idle no-tabs screen, returns to it after tabs or a
palette command, or switches spaces while it is showing. There is no polling.
Leaving the screen cancels its pending read, and replies from an earlier visit
cannot populate a later one. While the read is outstanding or inconclusive,
the screen shows the original two-line command hint. Bound native/operator
clients keep that hint. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The first confirmed missing step wins in this order. No spaces in
the host inventory, selected space stopped, older running release, no
operator declaration, managed chat setup required, operator unenrolled,
operator the only box besides the chat server. A selected space missing from
a nonempty inventory keeps the generic hint and the selection stays pinned.
An error checking a candidate bundle leaves the inventory unknown and keeps
the generic hint. [9](#source-9)
The observer uses existing VM lookup, guest version, controller box inventory
and chat status reads, without boot or controller installation. Bundle IDs do
not express ordering. The upgrade tip requires validated release tags and a
guest version response that matches the selected bundle. [4](#source-4)
[5](#source-5) [6](#source-6)

[FACT] Operator eligibility uses the effective policy grant. Active enrollment
matches the operator's declaration identity. The chat server is counted
separately using its name and declaration identity. An incomplete observation
or a change in that identity between reads keeps the generic hint. When all
steps are complete the view picks one random tip for the visit.
[2](#source-2) [6](#source-6)

[FACT] Plain Right fills an empty palette input when the shown tip has a
command. Enter is still required to submit. The first-space tip uses the
working directory and the host login if it is a valid account name. Otherwise
the account suggestion is `personal`. The shown directory stays explicit.
For a directory name the whitespace-split palette cannot express in one
token, the filled command uses `.` in the same directory. [7](#source-7)
[8](#source-8)

## Sources

1. <a id="source-1"></a> [TUI lifecycle](../../internal/tui/tui.go#L206-L249) — `Run`.
2. <a id="source-2"></a> [Welcome model](../../internal/tui/welcome.go#L59) — `syncWelcome`, `makeWelcomeTip`, `welcomeRows`.
3. <a id="source-3"></a> [TUI update](../../internal/tui/tui.go#L907-L978) — scheduling and reply handling.
4. <a id="source-4"></a> [Welcome observation](../../cmd/yoloctl/tui_welcome.go#L23-L63) — host bootstrap and read paths.
5. <a id="source-5"></a> [Version ordering](../../cmd/yoloctl/tui_welcome.go#L69-L84) — `olderWelcomeRelease`.
6. <a id="source-6"></a> [Box and chat observation](../../cmd/yoloctl/tui_welcome.go#L86) — `welcomeBoxStatus`.
7. <a id="source-7"></a> [Palette Right](../../internal/tui/tui.go#L1611-L1620) — fill tip or move cursor.
8. <a id="source-8"></a> [Palette editor](../../internal/tui/palette/palette.go#L114-L127) — `Submit` and whitespace tokenization.
9. <a id="source-9"></a> [Complete space listing](../../internal/spaceconfig/spaceconfig.go#L182) — `ListStrict`.
