# Embedded terminal core

Normal builds embed `wasm/ghostty-vt.wasm` and execute it with the Go wazero
runtime. They need neither Zig, CGO nor a system Ghostty installation. Each
terminal has its own module instance, with a 256 MiB memory limit and only three
host callbacks (reply bytes, formatted row, terminal event). There are no WASI,
filesystem or network imports. Owners serialize access and close instances.

The source pin, source archive hash, Zig version, bridge hash, artifact hash and
private ABI version are recorded in [manifest.json](wasm/manifest.json).
[bridge.zig](wasm/bridge.zig) adapts that exact revision of libghostty-vt; it
contains no terminal parser. It selects grapheme mode 2027 by default, including
after RIS. Its history budget is 64 MiB; the Go adapter also limits visible
history to the configured line count. Managed views apply their own visible
limit after restore; this never changes shared native retention or reflow.

Display frames export viewport-sized styled rows with cursor state in one
serialized operation. Native tracked positions anchor history through output
and reflow; the TUI adapter handles synchronized-output holds and focus reports.
Managed views receive the holder's ordered hold-timeout event; ordinary local
PTYs retain their local timeout.
See [display evidence](../../docs/digests/terminal-display.md) for contracts,
tests and current limits.

Mouse reports use the native encoder. Local selection uses native tracked pins
and wrap/grapheme metadata; the formatter returns bounded plain text for explicit
copy. The private bridge ABI is 6 (snapshots, measured geometry and mouse state).
See [mouse](../../docs/digests/terminal-mouse.md) and
[selection](../../docs/digests/terminal-selection.md) evidence for boundaries.

Rows retain host palette indices except application overrides, which render as
local RGB. Measured host colors answer child queries without changing the host
palette. Live bell events reach the outer terminal; a separate allocation-free
native stream neutralizes historical bells before retention. See
[colors and bells](../../docs/digests/terminal-colors-bells.md) for evidence.

`Snapshot`/`RestoreSnapshot` capture native state and restore a new isolated
instance with callbacks and parser continuation. The envelope requires the exact
embedded core hash; size, memory and decode time are bounded. Managed attachment uses this API over private channels. It does not serialize
frontend filters or image resources. See [snapshot evidence](../../docs/digests/terminal-snapshots.md).

Compatibility attachment uses a separate 1×1 instance without scrollback or reply output
to model the evicted replay prefix. Its VT prelude restores input modes, both
Kitty stacks and a native parser continuation before the retained byte tail.
Continuation retention is bounded to 32 KiB; the holder advances through the
tail's next parser boundary to recover an unavailable continuation, refusing a
new attach if the control remains unfinished. This does not restore complete screen contents. A separate native stream
filters queries and opaque extensions before compatibility output and retention.
Managed views instead receive full snapshots and sequenced events; the holder
answers queries once. See
[input replay evidence](../../docs/digests/session-replay.md).

To check a reproducible build, install Zig **0.16.0** and use Python 3.12+:

```sh
python3 scripts/build-terminal.py --zig /path/to/zig --check
```

Run from the repository root. The script downloads and verifies the pinned
source archive; Zig resolves content-addressed dependencies from its manifest.
Use `--source-archive /path/to/archive.tar.gz` to reuse the verified archive.
Omit `--check` to regenerate the artifact and manifest after an intentional
bridge change. Keep both with the implementation. An upstream upgrade also
requires reviewing private API assumptions, licenses, and the acceptance corpus.

`go test ./internal/terminal ./internal/tui/...` checks the manifest and terminal
contracts, including the strict corpus under `internal/tui/term/testdata`.
Artifact hashes check integrity; only a rebuild checks reproducibility. These
checks do not establish native graphical-terminal fidelity.

`make test-terminal-fidelity` runs the strict core corpus and the host-input/PTY
acceptance cases. The latter covers legacy input, committed Unicode/IME text,
bracketed paste, focus ownership and host-facing cursor/cell placement. It uses
scripted host bytes, not a desktop IME. See the
[compatibility evidence](../../docs/digests/terminal-compatibility.md) for scope.

Third-party notices are retained beside the artifact and embedded in the Go
binary: Ghostty (MIT), Zig standard library/compiler runtime (MIT), uucode (MIT),
Unicode data (Unicode license), and its UTF-8 decoder (Bjoern Hoehrmann license).
The license paths named in uucode's notice correspond to the adjacent
`LICENSE_unicode` and `LICENSE_Bjoern_Hoehrmann` files. Dependency license texts
remain authoritative.
