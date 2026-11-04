# pip development environment

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The `pip` feature combines a Dockerfile setup step with `network.pypi`.
The base installs Python 3.13 alongside Ubuntu's Python, bootstraps pip with
ensurepip, and points `/usr/local/bin/python` and `python3` at
`/usr/bin/python3.13`. The feature requires those aliases and verifies pip plus
creation of a temporary venv with working pip. Custom bases need the same
interpreter paths and sandbox user/group. No interpreter or package-manager
upgrade is performed by this feature. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] Setup supplies `pip` and `pip3` launchers invoking
`/usr/local/bin/python3 -m pip`, and creates sandbox-owned `.local`, `.local/bin`,
`.cache` and `.cache/pip` directories. `.local/bin` is added to the image PATH.
The entrypoint aligns home ownership with the host UID/GID. Setup does not
modify pip configuration, force user installs, set a global install prefix, or
activate a venv. Standard pip settings remain overridable by the user.
[2](#source-2) [4](#source-4)

[FACT] With standard Python/pip configuration, `python3 -m venv .venv` creates
an isolated environment; `.venv/bin/python -m pip` works without activation.
Outside it, `pip install --user` uses `~/.local`, and pip's Linux cache defaults
to `~/.cache/pip`. Normal POSIX venv symlinks retain the granted interpreter
identity. Copied or independently installed interpreters have different
executable paths and are outside this feature's tested contract.
[5](#source-5) [6](#source-6) [7](#source-7) [8](#source-8) [12](#source-12)

[FACT] `pypi` remains policy-only. Both features require the same existing
HTTPS grant for `pypi.org`, `*.pypi.org` and `files.pythonhosted.org`, with no
additional filesystem or PTY grant. Its executable allow-list includes Python
and the existing pip/uv paths; it permits Python programs to reach those
endpoints, not just pip's package operations. This feature does not install uv.
Private indexes, Git dependencies, other download hosts and native-extension
system dependencies are not provisioned. [1](#source-1) [2](#source-2)
[8](#source-8) [9](#source-9) [13](#source-13)

[FACT] Enabling `pip` changes the composed image even when `pypi` or an agent
feature is already selected. Adding `pypi` to `pip` does not add another install
step or change that image. The separate `python-3.12` feature installs Ubuntu's
`python3`/`python3-pip` packages without changing the base's default aliases;
it is not an interpreter selector. [1](#source-1) [10](#source-10)
[11](#source-11)

[FACT] `make smoke-pip` uses an isolated generic-shell box through confined
SSH. It checks interpreter/command alignment, user commands and cache ownership,
venv isolation, registry downloads, hashed offline requirements, PEP 517 editable
installation, changed source and console scripts, project tests, per-exec
activation, attributed curl denial and reconcile consistency. Fixture package
versions are pinned. The smoke's existence does not establish a live pass.
[7](#source-7)

## Sources

1. <a id="source-1"></a> [internal/features/features.go](../../internal/features/features.go#L176-L214) — `pypi`, `pip` and `python-3.12` definitions.
2. <a id="source-2"></a> [internal/features/pip.go](../../internal/features/pip.go#L12-L28) — `pipInstallDockerfile` validation, launchers and writable directories.
3. <a id="source-3"></a> [assets/containers/agentbox/Dockerfile](../../assets/containers/agentbox/Dockerfile#L51-L66) — Python PPA, venv package, ensurepip and default aliases.
4. <a id="source-4"></a> [assets/containers/agentbox/yoloctl-entrypoint.sh](../../assets/containers/agentbox/yoloctl-entrypoint.sh#L28-L46) — UID/GID and home ownership alignment.
5. <a id="source-5"></a> [Python 3.13 venv documentation](https://docs.python.org/3.13/library/venv.html) — isolated environment, executable paths and activation.
6. <a id="source-6"></a> [pip caching documentation](https://pip.pypa.io/en/stable/topics/caching/) — pip 26.2.1 cache defaults.
7. <a id="source-7"></a> [scripts/smoke/pip.sh](../../scripts/smoke/pip.sh#L1) — complete isolated runtime gate.
8. <a id="source-8"></a> [internal/capabilities/capabilities.go](../../internal/capabilities/capabilities.go#L269) — unchanged `network.pypi` endpoint and executable grants.
9. <a id="source-9"></a> [pip policy golden](../../internal/agentbox/testdata/policy_golden/pip.yaml#L1) — complete composed policy.
10. <a id="source-10"></a> [internal/agentboximage/composed_test.go](../../internal/agentboximage/composed_test.go#L204) — `TestComposedImageRef_PipEnvironmentChangesImage`.
11. <a id="source-11"></a> [internal/features/features_test.go](../../internal/features/features_test.go#L100) — `TestPipEnvironmentIncludesPyPIWithoutChangingPolicyOnlyFeature`.
12. <a id="source-12"></a> [pip user installs](https://pip.pypa.io/en/stable/user_guide/#user-installs) — pip 26.2.1 user scheme and virtualenv restrictions.
13. <a id="source-13"></a> [internal/agentbox/policy_golden_test.go](../../internal/agentbox/policy_golden_test.go#L138) — `TestPolicyBinaryAttribution` independent pip binary assertions.
