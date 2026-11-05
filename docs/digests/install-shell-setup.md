# Install shell setup

Verification: `4bc09137374e91d374ba6454cd8cef3d3da44240` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] For bash, zsh (`ZDOTDIR`) or fish (`XDG_CONFIG_HOME`), a successful
install may append one marked block per purpose: the bin dir on PATH when
missing, and completion sourcing the install link when absent. A terminal asks
(default No); `--add-to-path`/`--completion` answer yes; print-only and dry-run
only describe. A matching block is skipped silently; another install's or a
broken block is left with a warning. Symlinked, hard-linked, foreign-owned or
over-1-MiB files are refused with the block printed; otherwise a no-follow read
and `atomicfile.Write` keep the mode (not ACLs/xattrs). Other shells get the
`installPathHint` line. [1](#source-1) [2](#source-2)

[FACT] zsh's completion line runs only when `compdef` exists, so it is silent
until `compinit` has run; install says so after adding it. The bash line evals the
link's completion output (`eval "$(LINK completion bash)"`), avoiding process
substitution; the earlier `source <(...)` line failed the bash sourcing test on
macOS CI (bash 3.2). Removal is manual:
delete the lines from `# >>> yoloctl` to `# <<< yoloctl` (install help and
`docs/install.md`). A declined or non-terminal PATH step prints the line for
the resolved file (`ZDOTDIR` included); an unusable `ZDOTDIR` or home gets
generic guidance, never a guessed file. An unreadable answer or an interrupted
command stops setup with that error (also on paths that neither ask nor
write, so an interrupted print-only run never exits 0); nothing later is
asked or written.
Every block has an exact string test; bash blocks are sourced in a clean bash,
zsh (before and after `compinit`) and fish blocks only where those shells are
installed (the author's environment had neither).
[1](#source-1) [2](#source-2)

## Sources

1. <a id="source-1"></a> [Shell setup](../../cmd/yoloctl/install_shell.go#L1-L438), `installShellSetup`, `blockState`, `readRC`, `addBlock`, `installShellSteps`, `manualPathCommand`; flags and help in install.go `newInstallCmd`.
2. <a id="source-2"></a> [Tests](../../cmd/yoloctl/install_shell_test.go#L1-L354), `TestAddBlockAppendsAtomicallyAndRefusesUnsafeFiles`, `TestInstallShellStepsAskWriteOnceAndPrintOnly`, `TestInstallShellBlocksSourceInBash`, `TestInstallShellBlocksSourceInZshAndFish`, `TestInstallShellStepsStopOnInputErrorOrInterrupt`.
