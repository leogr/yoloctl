# Chat participant model and effort

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; state row `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; conversation row `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] A roomy Participants panel's card detail line is exactly
`Model (effort) · away reason`, such as `Opus 5.5 (max) · reviewing C`, below
the name row. Each part is left out when uncertain, so the line may be empty. It never shows the agent type, `none`,
an `unverified` label, a staleness marker, the context window or an effort
without its model. A trusted human's line instead reads
`human`, then any away reason. The model comes first, so a
narrow panel truncates the away reason. Only known Claude model IDs get
friendly names; others stay raw, sanitized to one line of at most 64 cells. Nothing is inferred from an agent name, a provider default
or launch arguments. [1](#source-1) [2](#source-2) [6](#source-6)

[FACT] The line shows a model only while it is an agent observation and the
last read is current: a failed read or one over two minutes old empties model
and effort with no marker. Compact one-row lists show
only a ` you` tag and the away reason after the name. The details card keeps
its Identity (`agent` or `human`, omitted when unconfirmed) and Agent rows, shows
each observed raw value with its report (status line, hook, or a read of the
agent's own server for Codex), a Context row
(`84k of 200k, 42%`, or `40k used` without a size) only beside an observed
model, a State row and wait tag ([agent state](agent-state.md)), a Conversation row
(`0b8f6e3c… · resumed`; see [following switches](conversation-following.md))
only for a certain one, then an Evidence row (read time, or `stale:` with the
failure or last read time) and why a value is missing. Without observed values it has none of these rows; a disconnected
participant's card omits the channel role. Help says unknown or
stale values are left out. [1](#source-1) [2](#source-2) [6](#source-6)

[FACT] Hovering a participant whose card detail line is clipped scrolls only
that line, one grapheme per step: a 600 ms hold, 120 ms steps until its end
shows, a 1 s pause, then back to the clipped start while still hovered. An
ellipsis marks text cut on either side; each frame keeps the line's width and
splits no grapheme. Fitting or empty lines, compact one-row
lists, hidden panels and lines with under six cells to move never start a tick.
One chain carries a generation bound to the channel, participant, text and
width: any update that changes them (another hover, a press, resize, modal,
channel switch or new label) resets it, and an older tick does nothing; moving
between a card's two rows keeps it. Terminals report no pointer leave, so a line
keeps scrolling while the last reported pointer stays on it. With NoMotion, or a
line too narrow to scroll, nothing moves and the footer hover hint names the
full line (an empty one adds nothing); a notice keeps priority. [9](#source-9) [10](#source-10)

[FACT] One worker reads values with one finite `box.inventory` request (5 s
bound) at connect and reconnect, after a membership change and every minute. Requests coalesce; a read made while the trusted catalog
moved to another room, disk, account or generation is discarded and repeated.
A failed read keeps earlier values for the details card only; a successful one
replaces them all. Values are keyed by the trusted catalog's immutable box ID of
non-revoked members, so a same-name box with a new ID gets none.
[3](#source-3) [4](#source-4) [5](#source-5)

[FACT] An observed value is the agent's own report for the launch running now:
a running box and session whose recorded launch is `launched`, is the running
holder's launch (no untracked note) and has the observation's generation, read
live in this request (`Live` is `current`) with `ok` health. A stored earlier
launch, an unreadable live read or a gap is not current evidence. The time of a
model report does not age it; the read time does. Missing effort stays unknown
beside a known model. Nothing else fills a gap: `--model` in a box's
`agent_extra_args` yields no value and the note `no agent reports for this box`.
The space telemetry worker is not used: its live-read budget is process-wide. [3](#source-3) [5](#source-5) [7](#source-7)

[FACT] Claude boxes report through the reporter their profile requires; a box
created before it reports after its next recreate, a print-mode session never.
Codex boxes report through their hook reporter ([Codex reporter](codex-observation.md)). [1](#source-1) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Runtime display](../../internal/chatui/runtime.go#L13-L251) — `AgentRuntime`, `RuntimeValue`, `SourceHook`/`SourceStatusLine`/`SourceQuery`, `RuntimeContext`, `modelNames`, `agents`, `runtimeStale`, `runtimeSummary`, `reportedBy`, `tokens`, `RuntimeContext.text`, `runtimeRows`; card rows in overlay.go `cardRows` L726; Help row in overlay.go L121.
2. <a id="source-2"></a> [Participant detail line](../../internal/chatui/roster.go#L178-L241) — `memberCard`, `memberRuns`; the line from `roleLine` in marquee.go L66; `agentBox` in runtime.go; model-first truncation and compact rows in presence_panel_test.go `TestParticipantAwayDetails` L159.
3. <a id="source-3"></a> [Participant runtime](../../cmd/yoloctl/chat_participant_runtime.go#L11-L135) — `chatParticipantRuntime`, `participantRuntime`, `participantConversation`, `observedRuntime`.
4. <a id="source-4"></a> [Serial reads](../../cmd/yoloctl/chat_view.go#L399-L500) — `refreshAgents`, `agentsLoop`, `loadAgents`, `adoptCatalogFor`.
5. <a id="source-5"></a> [Host regressions](../../cmd/yoloctl/chat_participant_runtime_test.go#L73-L163) — box IDs, observed values with sources and context, launch arguments ignored, superseded and failed reads.
6. <a id="source-6"></a> [UI regressions](../../internal/chatui/ui_package_test.go#L487-L682) — `cardRole`, `TestParticipantsShowObservedModelAndEffortOnly`: every line case, banned panel words (context included), stale and failed reads, sources and the Context row, effort-only, no-report, unverified and disconnected cards; `TestRuntimeContextText`.
7. <a id="source-7"></a> [Observation regressions](../../cmd/yoloctl/chat_participant_runtime_test.go#L18-L71) — `currentObservation`, `values`, `TestObservedRuntimeIsCurrentLaunchOnly`.
8. <a id="source-8"></a> [Claude reporter](claude-reporter.md) — the required status line and hook reporter.
9. <a id="source-9"></a> [Hover scrolling](../../internal/chatui/marquee.go#L12-L202) — `marqueeHold`/`marqueeStep`/`marqueeEnd`/`marqueeMin`, `hoveredLabel`, `marqueeSteps`, `marqueeWindow`, `roleText`, `settleMarquee`, `marqueeAdvance`; `cardWidth` set in roster.go `participantRows` L127, the footer fallback in hover.go `hoverHint` L192, the update hook in model.go L386 and L442.
10. <a id="source-10"></a> [Hover regressions](../../internal/chatui/marquee_test.go#L88-L319) — injected ticks: fitting, clipped and empty rows, delays, width, true suffix, invalidation and stale ticks, compact/NoMotion/narrow, CJK, combining and ZWJ graphemes.
