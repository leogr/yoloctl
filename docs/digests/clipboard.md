# Clipboard output and image-input boundary

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] OSC 52 output is intercepted at the host boundary. Evaluation permits
only enabled writes to clipboard selection `c`, with valid base64 and at most
1 MiB decoded data. Reads (`?`), other selections and oversized/malformed
payloads are rejected. A valid empty payload clears the clipboard. Configuration
uses one default-on `seams.clipboard` switch for local copy, application writes
and explicit PNG reads. Legacy `osc52_clipboard: false` or `image_paste: false`
also denies all integration; replace those old keys to use the new switch alone.
Host-terminal copy/paste is independent. Embedding flags prevent nested handling.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The scanner preserves ordinary output and carries partial UTF-8/OSC
across chunks. Emulator interception happens before VT decoding. The holder
removes clipboard writes from state/replay. Managed viewers receive separate
chunked live events; compatibility viewers receive live controls;
reattachment must not repeat clipboard side effects. [4](#source-4)
[5](#source-5) [6](#source-6) [12](#source-12)

[FACT] Ctrl+V normally reaches the child through its negotiated keyboard
encoder. Managed views can instead read a host PNG after that explicit local
gesture and paste its guest path in input order. Copied workdir files are reused
after confined verification; other PNGs use private upload frames. An absent
PNG/backend preserves the encoded key pair. OSC 52
never initiates image reads; ordinary text paste is independent. Passthrough
has no image-transfer handling. [7](#source-7) [8](#source-8) [13](#source-13)

[FACT, version-specific upstream] In Codex `rust-v0.158.0`, image paste reads
through arboard, preferring a clipboard file list before pixel data, encodes PNG,
and writes a temporary file. Linux has a WSL fallback. This evidence applies to
that inspected version; yoloctl's installed agent package tracks latest. A
pasted path is normalized by separate code, but normalization alone does not
make a host file visible inside a box. [9](#source-9)

[FACT, upstream interface] Claude's official interactive-mode reference lists
Ctrl+V for image paste. That documents the UI action, not its closed-source
clipboard detection or file-transfer implementation. [10](#source-10)

[FACT about a report] Upstream issue 29204 reports xclip/wl-paste detection on
Linux for Claude 2.1.62. It is an investigation lead, not independently audited
current implementation. Do not turn that report into a guaranteed agent
integration API. [11](#source-11)

[FACT] The implemented image transport inserts a file path; it does not expose
the host clipboard to guest libraries or emulate provider image-attachment
APIs. PNG-only host adapters, bounds and tests are described separately.
Codex `rust-v0.162.1` reads text using its native clipboard library (with a WSL
fallback), so executable shims alone cannot implement its right-click paste.
[14](#source-14) Provider recognition and graphical clipboard behavior need live acceptance;
recheck the exact installed agent before diagnosing those failures.
[13](#source-13)

## Sources

1. <a id="source-1"></a> [internal/termio/osc52.go](../../internal/termio/osc52.go#L46-L64) — `EvaluateOSC52`.
2. <a id="source-2"></a> [internal/config/tui.go](../../internal/config/tui.go#L217) — `ClipboardEnabled` and deny-only legacy opt-outs.
3. <a id="source-3"></a> [cmd/yoloctl/session_client.go](../../cmd/yoloctl/session_client.go#L42) — `attachToSession`.
4. <a id="source-4"></a> [internal/termio/osc52.go](../../internal/termio/osc52.go#L1-L291) — `OSC52Scanner`, `EvaluateOSC52`, `OSC52Filter`.
5. <a id="source-5"></a> [internal/tui/term/term_test.go](../../internal/tui/term/term_test.go#L17-L60) — `TestOSC52InterceptedBeforeCore` through `TestOSC52InterceptorDoesNotHoldUTF8Prompt`.
6. <a id="source-6"></a> [internal/session/holder.go](../../internal/session/holder.go#L388) — `broadcast` and separate clipboard events.
7. <a id="source-7"></a> [internal/tui/terminal_keys.go](../../internal/tui/terminal_keys.go#L38) — `childKey`, `kittyKey`.
8. <a id="source-8"></a> [internal/session/protocol.go](../../internal/session/protocol.go#L1-L132) — frame types, `readFrame`, `writeFrame`.
9. <a id="source-9"></a> [Codex clipboard_paste.rs](https://github.com/openai/codex/blob/rust-v0.158.0/codex-rs/tui/src/clipboard_paste.rs#L49-L270) — `paste_image_as_png`, `paste_image_to_temp_png`, `normalize_pasted_path`; pinned release source.
10. <a id="source-10"></a> [Claude interactive controls](https://code.claude.com/docs/en/interactive-mode#general-controls) — General controls; consulted 2026-09-28.
11. <a id="source-11"></a> [Claude issue 29204](https://github.com/anthropics/claude-code/issues/29204) — reporter’s Linux clipboard-detection observations, not a source audit.

12. <a id="source-12"></a> [Managed view transport](session-views.md) — separate clipboard events and native continuation.
13. <a id="source-13"></a> [Explicit host integrations](host-integrations.md) — user-initiated PNG read/transfer, ordered path paste and validation limits.

14. <a id="source-14"></a> [Codex text clipboard reader](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/tui/src/clipboard_paste/text.rs#L5) — native text reads, inspected at release 0.162.1.
