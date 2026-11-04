# Claude state vectors

`payloads.json` labels each case's evidence. Four cases derive from devOpUs's
D6 capture of Claude Code 2.1.312 (2026-10-28): UserPromptSubmit, Stop with empty
task arrays, Stop with an in-flight shell task, and Notification `idle_prompt`.
Their raw filenames are in the evidence fields; originals remain in the team's
`tasks/feat-agent-stats/devops-capture/raw/`.

Two cases are **captured** from devOpUs's A+ run of Claude Code 2.1.312 on
2026-10-28 (`tasks/feat-agent-stats/devops-sd-raw/RESULT.md` and `raw/`):

| Case | Raw Notification | Trigger |
|---|---|---|
| `permission_prompt` | `Notification-1793210329031263284.json` | Bash permission prompt |
| `permission_prompt-ask-user-question` | `Notification-1793210062200102297.json` | AskUserQuestion dialog |

Each Notification was paired by session/prompt ID with the PermissionRequest
named in its evidence field; `raw_sha256` hashes the verbatim Notification.
Both Notifications actually say `permission_prompt`, so both must produce
`waiting_permission`. AskUserQuestion is not evidence of `agent_needs_input`
or `elicitation_dialog`. Claude PermissionRequest is contextual capture
metadata here; it is not an installed Claude reporter event.

The A+ run used the documented exec path with prompts on and session-only
capture hooks in a disposable box. Its managed session never started; account
settings were untouched and the box was destroyed. Notifications arrived about
six seconds after unanswered prompts in this run; answering sooner yielded
only PermissionRequest. These are observations, not a timing guarantee or a
proof of managed permission-prompt/tag behavior.

Privacy/normalization: prompts, assistant/notification messages, paths,
prompt IDs and task commands/descriptions are stripped. For A+ only, the
session ID is replaced by the shared fixture UUID. Event/type strings are
unchanged. The payload fixtures are sanitized derivatives, not verbatim copies.

Stop with a session cron, `agent_needs_input`, `elicitation_dialog`,
`elicitation_url_dialog` and StopFailure remain explicitly **synthetic**, backed
by the official [hooks reference](https://code.claude.com/docs/en/hooks)
(`stop-input`, `notification-input`, `stopfailure-input`), checked 2026-10-28.
No live input/dialog/failure claim is made for these types. A separate ordinary
managed-launch AskUserQuestion run showed `waiting_permission` and cleared to
idle on answer; it did not retain raw inputs (`devops-sd-live/RESULT.md`).

The writer test normalizes PID to 42 and time to `2026-10-28T00:00:00Z`, removes
`dur_ms`, and compares output to `expected.jsonl`. Go reader tests parse those
same lines. Tasks/crons influence only Stop's state; contents are never written.
