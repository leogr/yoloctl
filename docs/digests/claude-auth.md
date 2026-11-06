# Claude profile storage and local credential status

Verification: `05865b85c7daae8778e234cd0d0693971c64e6d2` and accompanying corrections.

[FACT] Claude keeps its original native leaf bound at `/home/sandbox/.claude`.
Managed sessions prefix both `CLAUDE_CONFIG_DIR=/home/sandbox/.claude` and
`ANTHROPIC_CONFIG_DIR=/home/sandbox/.claude/anthropic`. The latter relocates the
Anthropic profile tree into the ordinary `anthropic` subtree of that leaf.
There is no second VM bind, entrypoint alias or host config share. A workload
can replace its own child entries, but none of those child paths is handed to
the VM container runtime as a source bind. Native state remains selected by
account/agent through the original resolver. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] `launch_env` is a nonsecret managed-command prefix. It does not change
arbitrary shell or `box exec` environments. Direct login/auth invocations use
an explicit prefix, following the legacy config convention:

```sh
yoloctl box exec BOX -- env CLAUDE_CONFIG_DIR=/home/sandbox/.claude ANTHROPIC_CONFIG_DIR=/home/sandbox/.claude/anthropic claude auth status
```

The same prefix applies to manual login commands. Bare direct invocations can
still write disposable default locations. No container-env propagation, forced
external-profile selection or new drift rule is added. [4](#source-4)
[5](#source-5)

[FACT] `auth.format` selects bounded native recognition (`claude` or `codex`);
blank retains ordinary file-presence status. Claude declares
`auth.profiles_dir: anthropic` relative to the native leaf. The helper reads at
most 1 MiB per credential/config and 256 bytes for the active selector without
following links. Legacy recognition requires nonempty
`claudeAiOauth.accessToken`. Anthropic recognition uses `active_config` (or
`default` when absent), matching `configs/<name>.json` with
`authentication.type=user_oauth`, and `credentials/<name>.json` with
`type=oauth_token` and nonempty `access_token`. Configuration alone is not
credential material; external/unknown formats remain unverified. Only source
labels/classifications leave the helper; storage errors remain unavailable.
Neither expiry nor provider acceptance is checked. [1](#source-1)
[6](#source-6) [7](#source-7)

[FACT] `account auth import ACCOUNT claude --anthropic-dir DIR` copies only the
selected profile's config, credential and active selection into native state.
The caller reads bounded files; the worker rechecks the profile digest,
relationship and fixed destinations before effects. Both source layouts present
require explicit `--file` or `--anthropic-dir`. Legacy file/API-key import remains
supported. Import refuses active consumers and uses the existing account lock
and native atomic-file writes, creating profile directories for the account
UID/GID. The selector is written last, without a cross-file transaction.
Inactive profiles/external identity files are not imported. Whole-space archives
retain the ordinary subtree; deletion removes the complete account leaf.
[6](#source-6) [8](#source-8) [9](#source-9)

[FACT] Before redirecting an existing login or recreating its box/space, the
human/operator must privately preserve disposable Anthropic state first. Stop
every consumer of the account, import the saved selected profile using the
updated controller, then use the managed launch (or explicit direct prefix).
Recreate explicitly when required by the normal lifecycle. Preserve inactive
profiles separately. Already-lost files cannot be recovered by relocation;
a new login may be needed. Source regressions are not proof of a provider login
surviving recreation. [4](#source-4) [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [Claude profile](../../internal/agentprofile/builtin/claude.yaml#L1) — original state bind, nonsecret launch paths and native auth conventions.
2. <a id="source-2"></a> [Resolver](../../cmd/yoloctl/agentstate.go#L1) — original whole-leaf account selection and normalization.
3. <a id="source-3"></a> [Source-swap regression](../../internal/agentbox/account_state_source_test.go#L1) — runtime mount source remains the whole leaf after its child is replaced; CLI normalization control in `account_auth_profiles_test.go`.
4. <a id="source-4"></a> [Launch prefix](../../cmd/yoloctl/launchenv.go#L1) — applyLaunchEnv; ordinary session path in `session_client.go` and tracked initial/resumed path in `session_identity.go`.
5. <a id="source-5"></a> [Launch regressions](../../cmd/yoloctl/launchenv_test.go#L1) — managed/explicit selection fixture and bare-exec limit; tracked resume coverage in `session_identity_test.go`.
6. <a id="source-6"></a> [Native helper](../../internal/accountstate/guest.py#L1) — auth_read/auth_status, account ownership and atomic no-follow writes.
7. <a id="source-7"></a> [Native regressions](../../internal/accountstate/testdata/test_guest.py#L1) — formats, bounds, links, private classifications and imported-directory ownership.
8. <a id="source-8"></a> [Import shape](../../cmd/yoloctl/account_auth_anthropic.go#L1) — bounded source reads, selection and destination validation.
9. <a id="source-9"></a> [Controller import](../../cmd/yoloctl/controller_documents.go#L275) — applyControllerAuthImport, digest, scope and consumers; CLI help in `account_auth.go` states preservation order.
