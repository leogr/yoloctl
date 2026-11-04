# TUI close confirmation

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] Closing a pane or tab ends its local client. Only an established
holder-backed view is known to leave its session running: `box attach`, root
`attach`, or a lifecycle command once its managed-view handshake is
established (`term.Session.HolderView`). For any other live command the remote
outcome is not known. A confirmed close authorizes ending the local client; it
never claims that the remote process stopped. [1](#source-1) [2](#source-2)

[FACT] What closes at once and what asks (`closeEffect`):
- **At once:** an established holder-backed view, `space view`, `chat`, an
  exited pane, and a pane without a session.
- **Asks:** every other live route, classified from the pane's launch argv and
  never from its title, aliases included. An editor (`box`/`space`/`account`/
  catalog `edit`) says `unsaved changes are lost`; a follow (`box`/`space logs
  -f`) says `the follow ends`; `box use --stdin` and `account secret set` say
  `the input ends`. Everything else, including any live route the classifier
  does not name, says `its client ends; the remote side's outcome is unknown`.
[1](#source-1)

[FACT] The **popup** opens for a pane's close (`Ctrl+W`, prefix `x`, a bar's ✕) or a tab's
close (its strip ✕, prefix `d`), with one entry per running command that asks: its
immutable launch command (never a title the child sets) and what closing ends.
- **Place:** it is drawn in the [pane overlay](tui-pane-menu.md), one cell
  inside the pane being closed (the focused pane for a tab), centered over
  the tab area when that pane cannot hold it with its buttons on one row.
  Palette rows and focus stay as they were; focus reports keep the focus
  they had at open.
- **Buttons:** `[ Keep ]` (highlighted by default) and `[ Close ]`.
- **Keys:** Enter takes the highlighted button; `←`/`→`/Tab move the
  highlight; `y` closes; `n` and Esc keep; `↑`/`↓` scroll the list by row, so
  a long wrapped entry's warning is reachable, with the buttons pinned. Every
  other key is consumed, `Ctrl+D` and paste included.
- **Mouse:** only the buttons' own row is a hitbox: a press on a button acts
  on it, and a press anywhere else, list text included, keeps. The press
  stays chrome-owned through its release; gestures begun before it opened
  finish at their owners.
- **Binding:** it is bound to its pane or tab and to the commands it listed.
  At Close it revalidates: a running command added, moved in or replaced
  refreshes the list, closes nothing and says so above the buttons for at
  least 3 s; if the popup then closes because its object went or the tab
  switched, that notice shows in the inert notice popup for its rest. A listed command exiting turns its
  line `exited` and keeps Keep highlighted; the popup never proceeds on its
  own. Its tab or pane going away, a tab switch, or a browser prompt arriving
  cancels it (Keep).
[1](#source-1) [3](#source-3)

[FACT] Tests walk every `opensLiveSurface` route and its class, and close an
established view of a real holder without asking. They also cover each key,
the mouse on each button, outside and on list text, the launch command shown
over a spoofed title, the popup's place and its highlighted default, the
bounded list scrolling by row to a long entry's warning, refresh on an added command, an exit, a
tab switch and a browser prompt, other panes' output while it is open, and a
child gesture in flight at open finishing at its owner. [4](#source-4)

## Sources

1. <a id="source-1"></a> [Close confirmation](../../internal/tui/confirm_close.go#L1-L300) — `closeEffect`, `requestDetachPane`, `requestDetachTab`, `acceptConfirm`, `checkConfirm`, `confirmKey`, `confirmOverlay`, `confirmClick`.
2. <a id="source-2"></a> [Holder-backed view](../../internal/tui/term/session.go#L344-L346) — `Session.HolderView`.
3. <a id="source-3"></a> [Routing](../../internal/tui/mouse.go#L196-L211) — the popup's press handling in `handleMouse`; keys and paste in tui.go `Update`, focus in frames.go `syncFocus`, `Popup.Selected` in popup.go.
4. <a id="source-4"></a> [Popup tests](../../internal/tui/confirm_close_test.go#L1-L335) — `TestCloseClassificationWalksEveryLiveRoute`, `TestHolderBackedViewDetachesAtOnce`, `TestConfirmPopupBindsToItsList` and the others.
