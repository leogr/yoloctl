# Claude profile storage and local credential status

Verification: `702c6ac7087f9c18a86edb0427fd4349cd82e788` and accompanying changes.

[FACT] Claude keeps its primary native leaf bound at `/home/sandbox/.claude`.
The profile additionally binds its `anthropic` subdirectory at
`/home/sandbox/.config/anthropic`. Both sources belong to the selected
account/agent leaf on the existing native disk; no host login directory or
whole `.config` directory is shared. The second view applies to managed launches
and ordinary commands in the same container. `CLAUDE_CONFIG_DIR` still controls
related legacy configuration; it does not replace the Anthropic bind.
[1](#source-1) [2](#source-2)

[FACT] Profiles declare additional views with `state_mounts` entries containing
`source` and `destination`. Sources are clean relative directories beneath the
primary leaf. Additional destinations are clean sandbox-home paths, and managed
destinations cannot overlap. Normalization replaces every managed slot from the
current account; account-none removes both stale Claude views. Host grant
checks and copying exclude these slots; runtime preparation creates their
native directories through the verified no-follow helper. Show/inspect include
the views. Other builtin agents retain one primary view. [2](#source-2)
[3](#source-3) [4](#source-4)

[FACT] `auth.format` selects bounded native recognition (`claude` or `codex`);
blank retains ordinary file-presence status. Claude declares
`auth.profiles_dir: anthropic`, backed by its additional view. The helper reads
at most 1 MiB per credential/config and 256 bytes for the active selector,
without following links. Claude legacy recognition requires a nonempty
`claudeAiOauth.accessToken`. Anthropic recognition uses `active_config` (or
`default` when absent), its matching `configs/<name>.json` with
`authentication.type=user_oauth`, and `credentials/<name>.json` with
`type=oauth_token` and nonempty `access_token`. Configuration alone is not
credential material; external/unknown formats remain explicitly unverified.
Empty/malformed JSON and missing values cannot establish credentials.
Only source names/classifications leave the helper; access/storage errors remain
unavailable. Neither expiry nor provider acceptance is checked.
[1](#source-1) [5](#source-5) [6](#source-6)

[FACT] `account auth import ACCOUNT claude --anthropic-dir DIR` copies only the
selected profile's config, credential and active selection into native state.
The caller reads bounded files; the worker rechecks the profile digest,
relationship and fixed destination paths before effects. Both source layouts
present require explicit `--file` or `--anthropic-dir`; new-format files cannot
be mistaken for legacy credentials. Legacy `--file` and API-key extraction
remain supported. Import refuses active consumers and uses the existing account
lock and native atomic-file writes. The selector is written last, without
claiming a cross-file transaction. Existing native legacy bytes are not changed
by a new-profile import. Inactive profiles/external identity files are not
imported. Whole-space archives retain the native subtree and translate links
through the additional view back into the same leaf. Account deletion removes
the whole leaf after stopping consumers. [7](#source-7) [8](#source-8)
[9](#source-9)

[FACT] Applying the additional binding to an existing container requires normal
explicit recreation. Before any box/space recreation, the human/operator must
privately preserve a disposable Anthropic login first. Stop every consumer of
the selected account, import the saved profile with the updated controller, then
recreate the box. Preserve inactive profiles separately. Mounting new native
state does not recover already-lost container files; a new login may be needed.
Source regressions establish storage/classification/orchestration behavior,
not a live provider login surviving recreation. [7](#source-7) [8](#source-8) [10](#source-10)

## Sources

1. <a id="source-1"></a> [Claude profile](../../internal/agentprofile/builtin/claude.yaml#L1) — state views, launch environment, auth conventions and existing API-key injection.
2. <a id="source-2"></a> [Resolver](../../cmd/yoloctl/agentstate.go#L1) — view derivation, replacement, show and host-source exclusion.
3. <a id="source-3"></a> [Profile validation](../../internal/agentprofile/state_mounts.go#L1) — source/destination constraints; `Profile.validateAuth` links profile storage to a declared view.
4. <a id="source-4"></a> [Runtime preparation](../../cmd/yoloctl/agentbox.go#L1955) — `finishAgentboxRuntimePreparation`, directories before launch.
5. <a id="source-5"></a> [Native recognition](../../internal/accountstate/guest.py#L131) — `auth_read`, `auth_status`; existing verified disk/leaf path.
6. <a id="source-6"></a> [Native regressions](../../internal/accountstate/testdata/test_guest.py#L1) — format, selection, bounded input, links and errors.
7. <a id="source-7"></a> [Import shape](../../cmd/yoloctl/account_auth_anthropic.go#L1) — bounded source reads, selection and destination validation.
8. <a id="source-8"></a> [Controller import](../../cmd/yoloctl/controller_documents.go#L275) — `applyControllerAuthImport`, profile digest, native-write scope and consumers; CLI help in `account_auth.go` states upgrade order.
9. <a id="source-9"></a> [Archive views](../../internal/spacearchive/native_export.go#L50) — `relativeLink` keeps additional view aliases in the same leaf.

10. <a id="source-10"></a> [Start preserves containers](../../internal/agentbox/reconcile.go#L90) — ReconcileForStart and CheckStart refuse recreation-only drift.
