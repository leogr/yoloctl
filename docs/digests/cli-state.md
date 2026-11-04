# CLI selection, runtime views, and output

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Flag/context space selection is centralized: explicit flag, current
pin, then `default`; explicit positional targets also win over context.
`box create NAME` resolves space before its default account and refuses an
existing declaration or runtime rather than rewriting it. [1](#source-1)
[2](#source-2)

[FACT] Box names match `^[a-zA-Z0-9][a-zA-Z0-9._-]{1,62}$`, retaining
reserved-name exclusions and the 63-character cap. Other entity kinds retain
their one-character minimum. This matches nerdctl 2.3.5's minimum/characters;
its validator has no maximum. Create/copy's shared Args validate names before
controller selection; native RunE and manifest validation remain. One-character
box declarations now fail validation without renaming or deletion.
[25](#source-25) [2](#source-2) [26](#source-26) [27](#source-27) [28](#source-28)

[FACT] `box create` uses the standard agentbox image for an agentless box;
a real agent supplies its profile image unless `--image` is explicit. Agentless
boxes retain `session_command` or fall back to a login shell. `box run`, root
`run` and root `ps` are unavailable in help/completion and controller operations;
use named `box create`, `box attach`/`start`, `box exec` and `box list`.
[2](#source-2) [8](#source-8) [16](#source-16) [17](#source-17)

[FACT] Shared Cobra definitions route box/account/runtime commands to the
selected running controller. Schema/build mismatch or stopped spaces return
actionable errors without booting. Host catalog/context commands remain local.
Operator metadata fixes the current space independently of host context.
[12](#source-12) [13](#source-13)

[FACT] Shared box resolution keeps declaration, runtime, session and drift as
separate dimensions. It combines configured boxes with runtime orphans, without
adopting those orphans as declarations. Summary views degrade to unavailable
runtime information when probes fail. FullDiff adds actual drift inspection.
RuntimeInventory instead returns probe errors and omits session probes, so
mutation safety checks can distinguish an empty inventory from unknown state.
`ResolveNamed`, which `ResolveOne` uses, lists the space once but runs session
probes, drift and conversation only for the requested boxes; each result
equals that box's `ResolveSpace` entry, and a name with neither a declaration
nor a container is missing. [3](#source-3) [24](#source-24)

[FACT] `space show --json` reports disk/VM metadata and, when reachable, native
declarations. The top-level `declarations_available` boolean is always present.
When false, workdirs and box inventories are null and their counts are -1,
rather than presenting unavailable declarations as empty. [4](#source-4)
`--all-spaces` emits available results plus diagnostics and a nonzero status
when any space is unavailable. [14](#source-14)

[FACT] `space list --json` lists VM runtime metadata, not that full
bundle contract. Box state JSON includes dimensions and optional reasons.
Read each command's writer for keys, omission rules and units; JSON shape is
not inferred from its human table. The cited list writers produce `[]` when
empty. [4](#source-4) [5](#source-5) [6](#source-6)

[FACT] Exit classification returns zero for success, preserves agent-session
and explicit passthrough child statuses, returns one for tagged user errors,
and two for other errors. Plain untagged Cobra failures can reach the fallback;
there is no universal promise that every usage error exits one. [7](#source-7)

[FACT] Bare CLI starts the TUI only when both stdin and stdout are terminals
and YOLOCTL_TUI is not set to `1`; otherwise it renders help. TUI completion
runs the current executable's Cobra `__complete` entry point with a two-second
deadline, parsing values, descriptions and the directive line. The palette
uses whitespace-separated argv, not shell quoting or expansion. [8](#source-8)
[9](#source-9) [10](#source-10)

[FACT] Entity completion uses a bounded controller query, without booting. Host
path completion uses local grants/filesystem. Operator path completion maps only
its actual granted binds. All-space completion combines reachable controllers
within one deadline. Unavailable spaces yield no entity suggestions.
[15](#source-15)

[FACT] Chat box/member completion uses that worker and existing operator method allowlist.
Enroll suggests declarations except the owned server; connect/disconnect suggest
active enrolled names only when declaration IDs match. Revoke suggests immutable
member IDs with names, including deleted/replaced boxes. Free names and exhausted
positionals suppress file completion. No room repair or mutation is performed.
Private native fixtures exercise the actual client Cobra protocol and worker;
transport/principal verification is stubbed. [22](#source-22) [23](#source-23)

[FACT] `space create/import --backend` completes adapters linked into the host
binary, filtering by prefix without probing hypervisors, KVM or controllers.
`--vm-image` completes local directories. Completion registration also runs
after host-only commands are added, so import receives the shared callbacks.
Shells and the TUI use the same Cobra completion endpoint.
[18](#source-18) [19](#source-19) [20](#source-20) [21](#source-21)

[FACT] Entity flags, including persistent `chat`/`account --space`, complete on
every command; free-form chat `--box/--human/--client` suppress files. Export
and operation complete spaces; import an archive after its new name; `account
observe` an account, then an agent. Tree-walking tests require a completer for
every entity flag and leading positional. [18](#source-18) [29](#source-29)

[FACT] Read-only views and completion do not take the mutation space guard.
Safety inventories and presentation views therefore have different failure
contracts; a UI fallback is not evidence that destroying an apparently unused
account is safe. The guard's command classification is the place to check when
adding a new command. [3](#source-3) [11](#source-11)

## Sources

1. <a id="source-1"></a> [cmd/yoloctl/context.go](../../cmd/yoloctl/context.go#L14) — selection helpers.
2. <a id="source-2"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L31) — box command tree, create defaults and `ensureAgentboxAbsent`; `newAgentboxCreateCmd` at L849, Args/RunE validation at L886–898.
3. <a id="source-3"></a> [internal/boxstate/boxstate.go](../../internal/boxstate/boxstate.go#L17-L322) — `Depth` through `ResolveSpace`.
4. <a id="source-4"></a> [cmd/yoloctl/space.go](../../cmd/yoloctl/space.go#L1328) — space JSON and unavailable declarations.
5. <a id="source-5"></a> [cmd/yoloctl/space.go](../../cmd/yoloctl/space.go#L1521) — runtime list JSON.
6. <a id="source-6"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L2639) — box state JSON.
7. <a id="source-7"></a> [cmd/yoloctl/main.go](../../cmd/yoloctl/main.go#L87) — remote/user/runtime exit classification.
8. <a id="source-8"></a> [cmd/yoloctl/main.go](../../cmd/yoloctl/main.go#L112) — shared root definitions and TUI.
9. <a id="source-9"></a> [internal/tui/yoloctlcli/complete.go](../../internal/tui/yoloctlcli/complete.go#L1-L100) — `Complete` and completion result parsing.
10. <a id="source-10"></a> [internal/tui/palette/palette.go](../../internal/tui/palette/palette.go#L95-L129) — `Submit` through `CompletionInput`.
11. <a id="source-11"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L29) — native mutation guard.
12. <a id="source-12"></a> [cmd/yoloctl/controller_client.go](../../cmd/yoloctl/controller_client.go#L96) — public execution routing.
13. <a id="source-13"></a> [cmd/yoloctl/controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L82) — running/build checks.
14. <a id="source-14"></a> [cmd/yoloctl/controller_views.go](../../cmd/yoloctl/controller_views.go#L52) — aggregation and space metadata views.
15. <a id="source-15"></a> [cmd/yoloctl/controller_completion.go](../../cmd/yoloctl/controller_completion.go#L34) — completion routing.
16. <a id="source-16"></a> [cmd/yoloctl/run.go](../../cmd/yoloctl/run.go#L43) — `applyAgentProfileDefaults` preserves explicit image selection.
17. <a id="source-17"></a> [cmd/yoloctl/box_run_removed_test.go](../../cmd/yoloctl/box_run_removed_test.go#L1) — actual CLI/help/completion and controller removal regressions; supported agentless session flags.
18. <a id="source-18"></a> [Completion registration](../../cmd/yoloctl/completion.go#L197) — `registerFlagCompleters`, `completeVMBackends`, `completeHostDirs`.
19. <a id="source-19"></a> [Linked adapters](../../internal/vm/vm.go#L47) — `BackendNames`.
20. <a id="source-20"></a> [Completion regressions](../../cmd/yoloctl/completion_test.go#L24) — actual Cobra protocol for create/import, prefixes, aliases, unavailable host tools and image directories.
21. <a id="source-21"></a> [Host command assembly](../../cmd/yoloctl/controller_lifecycle.go#L25) — `newClientRootCmd`.
22. <a id="source-22"></a> [Chat callbacks](../../cmd/yoloctl/chat_completion.go#L17) — native manifest/declaration completion; registered by `registerCompletions`.
23. <a id="source-23"></a> [Chat completion regression](../../cmd/yoloctl/chat_completion_test.go#L28) — host/operator routing, identities, prefix, cancellation and offline refusal.
24. <a id="source-24"></a> [Named resolution](../../internal/boxstate/boxstate.go#L225-L421) — `ResolveOne`, `ResolveNamed`, `resolveSpace`; `TestResolveNamedLimitsPerBoxWorkToTheNamedBoxes` in boxstate_test.go L245.
25. <a id="source-25"></a> [Name validator](../../internal/layout/layout.go#L59-L98) — `ValidateName`, patterns and reserved names.
26. <a id="source-26"></a> [Copy validation](../../cmd/yoloctl/agentbox_copy.go#L69-L88) — shared Args and native RunE validate destination/source.
27. <a id="source-27"></a> [Manifest validation](../../internal/agentbox/agentbox.go#L519-L520) — `Config.Validate` uses the shared box rule.
28. <a id="source-28"></a> [nerdctl v2.3.5 validator](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/identifiers/validate.go#L25-L45) — `AllowedIdentifierPattern`, `ValidateDockerCompat`.
29. <a id="source-29"></a> [Completion audit regressions](../../cmd/yoloctl/completion_audit_test.go#L37) — host `__complete` for persistent `--space` and backup/observe positionals; tree-wide entity flag and positional wiring.
