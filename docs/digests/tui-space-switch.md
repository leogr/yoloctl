# Per-TUI space selection

Verification: `88595d59669105bfdd8988628f77098ebbd318d0` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] A full TUI resolves its default once at startup. `YOLOCTL_SPACE` is a
non-secret process default: explicit flag/positional target wins, then a
nonempty environment pin, then the global context, then `default`. Environment
pins use existing-space validation, including legacy names; an invalid nonempty
pin is an error, not permission to fall back. Trusted native and operator
bindings remain fixed independently of this environment variable.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The model supplies a fresh child environment for pane launches, palette
console commands and every completion path. It replaces inherited
`YOLOCTL_SPACE` without changing the process environment or rewriting argv.
Thus an explicit `--space B` or `space view B` still wins over default A.
Completion retains its two-second deadline. Standalone attachment retains its
original explicit/inherited selection and has no switcher.
[3](#source-3) [4](#source-4) [5](#source-5) [12](#source-12)

[FACT] Click the top-right `space ▾` chip or press the configured pane prefix
then `s`. The dropdown lists the local bootstrap inventory, sorted with the
current space marked, including stopped spaces. Up/down or the wheel moves;
Enter or a row click selects; Esc/outside click keeps. Loading, unavailable and
empty inventories are shown explicitly. Selecting the current space does
nothing. A bound native/operator client does not offer cross-space choices.
This inventory does not probe or boot VMs. The footer names `yoloctl space use`
as the command that changes the default. It wraps clauses first, keeping the
command together when it fits, then wraps at word boundaries; words
wider than the available width are ellipsized.
[3](#source-3) [6](#source-6) [13](#source-13)

[FACT] A different target always opens one all-tabs confirmation, even with no
tabs. **Keep** is the default; **Close tabs & switch** closes local views using
normal tab teardown. It shows tab/pane counts, warns that chat drafts and local
view state are lost, and reuses the editor/input/follow/unknown-outcome warnings
of ordinary close confirmation. Established managed holder sessions survive;
closing other clients does not establish their remote outcome. Added or
replaced panes/sessions refresh the review instead of closing an unreviewed
set. A browser prompt retains modal priority.
[3](#source-3) [7](#source-7) [14](#source-14)

[FACT] Switching is refused while a palette command runs: finish or interrupt
it with the normal controls first. Cancellation keeps the pin, tabs, focus and
palette draft. Acceptance clears local tabs, palette draft/output/completion
and transient overlays/selection, keeps command history, changes only the model
pin and focuses an empty palette. It opens no view and replays no command.
Completion/list generations and source-session membership reject stale replies
and attach requests; exits still locate their original session.
[3](#source-3) [4](#source-4) [8](#source-8)

[FACT] `space use` still writes the global default. It does not change an open
TUI's pin; use the dropdown for that. A fresh TUI resolves the then-current
selection. Switching neither writes global context nor grants authority,
creates accounts, stops boxes or persists/restores pane layouts.
[1](#source-1) [2](#source-2) [3](#source-3) [11](#source-11)

[FACT] Source fixtures cover resolver precedence, legacy/invalid pins,
native/operator boundaries, independent models, actual pane/console/completion
child environments, captured asynchronous environments, keyboard/mouse/modal
behavior, all-tabs revalidation, stale replies and reattachment to the same real
fixture holder after switching. These tests are not native macOS interaction
proof.
[9](#source-9) [10](#source-10) [15](#source-15) [16](#source-16)

## Sources

1. <a id="source-1"></a> [Shared resolver](../../internal/config/context.go#L107) — `SpaceEnv`, `ResolveSpace`, `SourceEnvironment`.
2. <a id="source-2"></a> [CLI selection](../../cmd/yoloctl/context.go#L25) — `resolveSpacePositional`, `selectedSpace`.
3. <a id="source-3"></a> [Switcher](../../internal/tui/space_switch.go#L25) — `initSpace`, `childEnv`, picker, snapshot, `acceptSpaceSwitch`.
4. <a id="source-4"></a> [TUI](../../internal/tui/tui.go#L180) — `Run`, `dispatch`, `startPane`, `Update`, `suggestionsCommand`, `completePalette`, `contextChip`.
5. <a id="source-5"></a> [Completion subprocess](../../internal/tui/yoloctlcli/complete.go#L47) — `Complete`.
6. <a id="source-6"></a> [Pane bindings](../../internal/tui/pane_prefix.go#L425) — `paneKeys`.
7. <a id="source-7"></a> [Close confirmation](../../internal/tui/confirm_close.go#L62) — aggregate confirmation, `closeEffect`, `checkConfirm`, labels.
8. <a id="source-8"></a> [Private requests](../../internal/tui/tab_requests.go#L15) — captured generation; `Update` validates generation and source membership.
9. <a id="source-9"></a> [UI regressions](../../internal/tui/space_switch_test.go#L22) — `TestSpace*`.
10. <a id="source-10"></a> [CLI regressions](../../cmd/yoloctl/space_pin_test.go#L17) — host/native/operator selection.
11. <a id="source-11"></a> [Space use](../../cmd/yoloctl/space.go#L1468) — global write and resolved-source display.
12. <a id="source-12"></a> [Standalone attachment](../../internal/tui/attach.go#L21) — `RunAttached`.
13. <a id="source-13"></a> [Pane menu](../../internal/tui/pane_menu.go#L46) — `menuRows`, `bindingWhy`.
14. <a id="source-14"></a> [Normal teardown](../../internal/tui/chrome_gestures.go#L295) — `detachTab`.
15. <a id="source-15"></a> [Startup isolation](../../internal/tui/tui_test.go#L909) — `TestTUIPinsStartupSpace`.
16. <a id="source-16"></a> [Resolver regressions](../../internal/config/context_test.go#L142) — `TestResolveSpaceEnvironment`.
