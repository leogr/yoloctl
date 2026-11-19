# Agent maintenance in running boxes

Verification: `c6e67053e4fd40a8987dc6be1c3f34bb4d6b1380` and accompanying changes.

[FACT] Agent installers, metadata reads and process probes use outer
`nerdctl exec --user sandbox` with a fixed system PATH. npm targets latest
with `/home/sandbox/.npm-global` and a private maintenance cache, bypassing the
agent-writable user and global npmrc files. Unreadable version metadata skips
autoupdate with a notice. Install errors propagate with
`--no-autoupdate` guidance for npm. [1](#source-1) [2](#source-2) [3](#source-3)
[4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7)
[8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [internal/features/features.go](../../internal/features/features.go#L1-L135) — `Feature`, builtin install and capability declarations.
2. <a id="source-2"></a> [cmd/yoloctl/autoupdate.go](../../cmd/yoloctl/autoupdate.go#L50-L172) — `updateAgentInRun`, `agentMaintenanceExecArgs`, npm install and metadata reads.
3. <a id="source-3"></a> [Gemini adapter](gemini.md) — latest npm install with image-owned update/shell settings.
4. <a id="source-4"></a> [Maintenance argv and failure regressions](../../cmd/yoloctl/container_target_test.go#L61-L167) — `TestMaintenanceExecsUseSystemPathAndExactlySelectedContainer`, `TestNpmMaintenanceFailureExplainsSystemPathAndAutoupdateOptOut`.
5. <a id="source-5"></a> [Native install argv](../../cmd/yoloctl/agent_distribution.go#L102-L118) — `nativeInstallArgs` uses the shared exec builder.
6. <a id="source-6"></a> [nerdctl v2.3.5](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/cmd/container/exec.go#L171-L179) — exec applies environment overrides to the process spec.
7. <a id="source-7"></a> [Process probe](../../cmd/yoloctl/agent.go#L278-L286) — `agentProcessRunning` uses the shared exec builder.
8. <a id="source-8"></a> [System search path](../../internal/container/container.go#L886-L888) — `SystemExecPath`, also used by legacy DNAT.
9. <a id="source-9"></a> [npm maintenance configuration](npm.md#npm-development-environment) — runtime config overrides, prefix and cache.
