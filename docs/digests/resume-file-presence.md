# Saved conversation presence before resume

Verification: `f5928f39223f8851c230b898d6eb2fcfab16cc2c` and accompanying changes;
upstream documentation checked 2026-11-06. Re-read cited symbols when changing behavior.

[FACT] Claude documents local transcripts under
`~/.claude/projects/<project>/<session-id>.jsonl`; `CLAUDE_CONFIG_DIR`
relocates that root. Resume by ID searches projects. Persistence can be
disabled and saved histories can be deleted or expire, so absence does not
prove a conversation was never used. [1](#source-1) [2](#source-2)

[FACT] The builtin Claude profile declares
`session.resume_file: projects/*/{{session_id}}.jsonl` beside its assign/resume
arguments. This optional profile field requires a native state root and both
assign and resume support. Validation permits fixed relative directory
components, exactly one single-directory wildcard immediately before the
filename, one UUID placeholder and a literal filename suffix. Absolute or
parent paths, recursive globs, shell syntax and other templates are rejected.
Codex and agy omit the field. [3](#source-3) [4](#source-4) [5](#source-5)

[FACT] Before a new resume allocation, the controller checks ownership of
`Target` (the last observed ID, otherwise the assigned one), then requests
`resume-file` for that account/agent leaf. The request has the existing
10-second guest-probe deadline; only explicit `present` or `absent` outcomes
are accepted. Errors, including an empty response, refuse dispatch. Already
allocated launches keep their recorded kind, ID, generation and token without
another presence check. Ordinary live-holder, uncertain-launch and other-owner
refusals remain; manual/never, explicit new-session and profiles without this
field retain their paths. [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] The native helper verifies the ext4 disk UUID, ownership marker and
live account-state bind before opening the selected leaf. Its probe creates
nothing. Directory traversal and candidate opens use no-follow descriptors;
matching candidates must be readable regular files and are closed without
reading their contents. It examines one project-directory level and at most
10,000 entries. A missing fixed prefix or complete search without a match
returns absent. Missing leaf, inaccessible storage, linked entries, nonregular
matches, I/O errors and exhausted bounds refuse. Returned data contains only
the outcome, never project names or transcript contents. The ordinary disk
ownership-marker read is unchanged. [9](#source-9)

[FACT] Confirmed absence retires assigned/current IDs into bounded `previous`
history with `saved-conversation-missing`, then allocates one new UUID through
the ordinary atomic record lifecycle. Generations and original closed-holder
proof remain intact. Failed probe/save dispatches nothing. The notice says
`no saved conversation found for <id>; starting a new conversation <new-id>`
before the single start attempt; it does not claim that attempt succeeded.
A fresh assignment retains box model/effort arguments without carrying the
missing target's reported values.
Shared status/history explains the recorded replacement. Failure after dispatch
never triggers another launch or an output-based fallback.
[6](#source-6) [10](#source-10) [11](#source-11)

[FACT] Presence is not a resumability test: empty, malformed or duplicate
files follow ordinary Claude resume handling. No transcript is parsed, no
provider/authentication success is inferred, and no new account-wide
transaction prevents a concurrent user change between probe and launch.
[6](#source-6) [9](#source-9)

[FACT] Regression fixtures exercise native temporary files (including
unprivileged permission failures), strict patterns, missing/saved/unknown
Target decisions, history/status, failed saves and allocated replay. These are
filesystem tests and fake VM/holder tests; they do not prove a real Claude
conversation resumes or validate login/provider behavior.
[12](#source-12) [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Claude session documentation](https://code.claude.com/docs/en/sessions#where-transcripts-are-stored) — local layout, ID lookup and retention/persistence limits.
2. <a id="source-2"></a> [Claude directory documentation](https://code.claude.com/docs/en/claude-directory) — `CLAUDE_CONFIG_DIR` relocation.
3. <a id="source-3"></a> [Claude profile](../../internal/agentprofile/builtin/claude.yaml#L56) — session pattern and argv.
4. <a id="source-4"></a> [Profile validation](../../internal/agentprofile/profile.go#L155) — `SessionSpec`, `validateSession`.
5. <a id="source-5"></a> [Restricted pattern](../../internal/agentprofile/resume_file.go#L11) — `validateResumeFile`.
6. <a id="source-6"></a> [Tracked launch](../../cmd/yoloctl/session_identity.go#L283) — `launch`, owner/preflight ordering and ordinary allocation/settlement.
7. <a id="source-7"></a> [Presence request](../../cmd/yoloctl/session_resume_file.go#L15) — `resumeFilePresent`.
8. <a id="source-8"></a> [Probe deadline](../../internal/lima/probe.go#L5) — `GuestProbeTimeout`, re-exported by `internal/vm`.
9. <a id="source-9"></a> [Native helper](../../internal/accountstate/guest.py#L236) — `resume_file_present`, `run`, descriptor helpers.
10. <a id="source-10"></a> [Conversation record](../../internal/agentsession/record.go#L72) — replacement reason, validation, `retire`, `save`.
11. <a id="source-11"></a> [Shared status](../../internal/agentsession/status.go#L181) — `PreviousView.Text`, `Describe` note.
12. <a id="source-12"></a> [Native fixtures](../../internal/accountstate/testdata/test_guest.py#L21) — read-only presence, permissions, links/types and incomplete search.
13. <a id="source-13"></a> [Launch regressions](../../cmd/yoloctl/session_resume_file_test.go#L21) — pre-dispatch decisions, replay and explicit native outcomes.
14. <a id="source-14"></a> [Pattern regressions](../../internal/agentprofile/resume_file_test.go#L1) — valid/invalid grammar and state/argv requirements.
