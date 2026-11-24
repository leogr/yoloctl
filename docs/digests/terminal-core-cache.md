# Terminal core compile cache

Verification: `64117765f5206b36cab025c10250229f3eb59b78` and accompanying changes.

[FACT] A process compiles the embedded libghostty-vt core once, for its first
terminal. With a compile cache selected it first tries the folder
`<cache>/<build key>`. The build key is 16 hex characters of SHA-256 over the
core bytes and the runtime options, so one build's entries stay together;
wazero's own entry key already covers the module, including the termination
option, and the CPU features. wazero adds a `wazero-<version>-<arch>-<os>`
folder and one file per module and CPU feature set. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Only the CLI selects a cache. A host process uses `terminal-core` under
the config root, which host grants already refuse. A process in a VM context,
in a box (`OPENSHELL_SANDBOX`) or with an in-box operator client uses none. The
VM's session holder, which runs as root, uses `/var/lib/yoloctl/terminal-core`,
a guest path box grants already refuse. Any other caller, tests included,
compiles in memory. A privileged box without the operator grant behaves like a
host for this cache: its endpoint gives children no `OPENSHELL_*` variable, and
it has no VM context or operator client, so a terminal there uses the box
user's own config root. [4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7) [8](#source-8) [12](#source-12)

[FACT] Both paths are defined in the CLI package, not in `internal/layout`:
the OpenShell helper links that package, and in `space upgrade` a change to the
helper's sources marks every confined box for recreation. [6](#source-6) [11](#source-11)

[FACT] The cache holds native code that the process runs, and no ownership or
permission check is made: yoloctl trusts whoever can write or replace the
selected cache folder. `YOLOCTL_HOME` or `XDG_CONFIG_HOME` can place the config
root outside the login home, including on shared or restored storage, and
wazero's names identify the build, OS, architecture and CPU features, not the
machine. The folders yoloctl creates are mode 0700. [2](#source-2)

[FACT] When wazero cannot write an entry (for example a full disk) it fails the
compile but keeps the compiled module in the cache's memory: one retry with a
new runtime reuses it without a second compile. Any other error, such as a
folder that cannot be created or a corrupt entry, ends in the in-memory compile.
A corrupt entry is left in place, because a concurrent process may have just
replaced it, so later processes compile in memory too until it is removed. Old
build-key folders are kept. [2](#source-2) [9](#source-9)

[FACT] At the verification base on AMD EPYC, one CPU, ten samples per case
(medians), a first-terminal core start took 244.2 ms and 20.8 MiB with the
in-memory compile, 254.9 ms and 23.6 MiB on an empty cache (compile and write),
and 10.8 ms and 2.0 MiB from a warm cache. [10](#source-10)

## Sources

1. <a id="source-1"></a> [Compile cache](../../internal/terminal/cache.go#L25-L43) — `UseCompilationCache`, `runtimeConfig`, `buildKey`.
2. <a id="source-2"></a> [Cached compile](../../internal/terminal/cache.go#L45-L78) — `compileCore`, `compileCached` (retry, in-memory fallback).
3. <a id="source-3"></a> [Process core](../../internal/terminal/terminal.go#L41-L44) — `core`, `buildCore`.
4. <a id="source-4"></a> [Host cache folder](../../cmd/yoloctl/terminal_cache.go#L31-L46) — `hostTerminalCacheDir`, selected at CLI start (main.go `UseCompilationCache`).
5. <a id="source-5"></a> [Holder cache folder](../../cmd/yoloctl/session_hold.go#L74) — `session-hold`.
6. <a id="source-6"></a> [Cache paths](../../cmd/yoloctl/terminal_cache.go#L11-L25) — `vmTerminalCacheDir`, `terminalCacheDir` (config root from `layout.ConfigRoot`).
7. <a id="source-7"></a> [Grant checks](../../cmd/yoloctl/space_lifecycle.go#L34-L90) — `rejectManagedHostPathWith` (config root), `rejectSpaceHostPathWith` (`/var/lib/yoloctl`).
8. <a id="source-8"></a> [Grant and host-only tests](../../cmd/yoloctl/vm_defaults_test.go#L58) — `TestHostGrantsCannotExposeTerminalCoreCache`, `TestHostTerminalCacheOnlyOnTheHost`.
9. <a id="source-9"></a> [Cache tests](../../internal/terminal/cache_test.go#L39-L144) — cold/warm/in-memory parity, corrupt entry, unwritable entry folder, uncreatable folder, build key.
10. <a id="source-10"></a> [Benchmark](../../internal/terminal/cache_test.go#L173) — `BenchmarkCoreStart` (`-benchtime=10x -count=10 -cpu=1`, GOMAXPROCS=2).
11. <a id="source-11"></a> [Helper check](../../internal/openshellrelease/release.go#L31-L42) — `EarlierHelper` compares the helper source hash and modules.
12. <a id="source-12"></a> [Privileged box child environment](../../internal/boxendpoint/boxendpoint.go#L151-L162) — `ChildEnvironment` (no `OPENSHELL_*` keys or sandbox marker).
