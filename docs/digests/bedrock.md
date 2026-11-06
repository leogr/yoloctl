# AWS CLI and Bedrock setup

Verification: `cae7f0d106ee85c3193168becc12a3ee8fdc25c6` and accompanying changes.

[FACT] Builtin Claude and Codex profiles project exactly four optional AWS account
secrets: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, and
`AWS_BEARER_TOKEN_BEDROCK`. Delivery uses the existing private child-environment
channel at container creation; missing keys remain nonfatal. Both agents on the
same account receive configured keys. Use separate accounts when that sharing
is unwanted. Changed secrets require explicit box recreation; temporary material
still expires. No host credential discovery or automatic renewal occurs.
[1](#source-1) [2](#source-2) [9](#source-9)

[FACT] `account auth status` adds **AWS secrets present (not verified)** when
any declared AWS key has nonempty stored material, even alongside unavailable
native storage. This checks neither a complete pair, selected provider, current
box delivery nor AWS validity. Native classifications and errors remain separate.
`account auth import` remains a native-agent import; it does not import AWS
profiles/SSO caches. [3](#source-3)

[FACT] Optional `--features aws` installs official AWS CLI v2 for Linux
amd64/arm64 on a compatible glibc/apt base. The recipe installs unpacking/runtime
dependencies, checks the embedded public-key fingerprint, verifies the official
ZIP's detached signature in an isolated keyring, then runs its installer in
update mode. Verification failure prevents extraction/execution. Latest means
when the installer runs; cached images stay cached until explicit refresh/rebuild.
Script and key bytes enter the composed-image hash, so their changes invalidate
AWS recipes without changing unrelated images. The feature grants no egress or
credentials and does not persist `~/.aws`. Agentless AWS commands acquire no
account secrets merely from installing it. [4](#source-4) [5](#source-5) [11](#source-11)

## User-owned provider and egress setup

[FACT] Assume a running space `dev` and declared account `aws`. Store only the
needed keys privately; for temporary credentials also set the session token,
or use the Bedrock bearer key as appropriate. Never put secret values in --env.
[6](#source-6)

```sh
yoloctl account secret set aws AWS_ACCESS_KEY_ID --space dev --from-env
yoloctl account secret set aws AWS_SECRET_ACCESS_KEY --space dev --from-env
```

[FACT] Copy/edit a custom capability, replacing the copied grants with this
illustrative regional Runtime example. The source schema and compiler accept
these exact host/port/binary entries. Select endpoints for the actual region,
provider and SDK flow; this example does not promise universal AWS access.
There is no builtin Bedrock capability or endpoint catalog. [7](#source-7)

```sh
yoloctl capability copy network.anthropic network.my-bedrock
yoloctl capability edit network.my-bedrock
```

```yaml
schema_version: 1
name: network.my-bedrock
grants:
  network:
    egress:
      - hosts:
          - bedrock-runtime.us-east-1.amazonaws.com
          - bedrock.us-east-1.amazonaws.com
          - sts.us-east-1.amazonaws.com
        ports: [443]
        binaries:
          - /usr/bin/node
          - /home/sandbox/.npm-global/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe
          - /home/sandbox/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex
          - /home/sandbox/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-arm64/vendor/aarch64-unknown-linux-musl/bin/codex
          - /home/sandbox/.codex/packages/app-server-daemon/releases/*-x86_64-unknown-linux-musl/bin/codex
          - /home/sandbox/.codex/packages/app-server-daemon/releases/*-aarch64-unknown-linux-musl/bin/codex
          - /usr/local/aws-cli/v2/*/dist/aws
```

[FACT] These commands use repeatable --env and singular --capability. Provider
and region configuration is user-owned, never automatically selected.
[6](#source-6) [8](#source-8)

```sh
yoloctl box create claude-aws --space dev --agent claude --account aws --features aws --capability network.my-bedrock --env CLAUDE_CODE_USE_BEDROCK=1 --env AWS_REGION=us-east-1 --no-attach
yoloctl box create codex-aws --space dev --agent codex --account aws --features aws --capability network.my-bedrock --env AWS_REGION=us-east-1 --no-attach
```

[FACT] Per upstream documentation, Claude uses `CLAUDE_CODE_USE_BEDROCK=1` and a
region. For Codex, edit the account's native `.codex/config.toml` with:

```toml
model_provider = "amazon-bedrock-runtime"
web_search = "disabled"
```

Choose a model/inference profile available to the AWS identity and region.
Codex's `amazon-bedrock` selects Mantle instead and needs its own endpoint grant.
SSO, global STS, FIPS and private endpoints also require explicit host selection;
middle-label host wildcards are rejected [12](#source-12). Neither provider permissions nor live
SigV4/streaming, inference, expiry/refresh or recreate survival follows from source
fixtures. No AWS account was available for live provider testing.
[7](#source-7) [8](#source-8) [10](#source-10)

## Sources

1. <a id="source-1"></a> [Claude profile](../../internal/agentprofile/builtin/claude.yaml#L1-L154) — explicit optional AWS and existing Anthropic-key declarations.
2. <a id="source-2"></a> [Private projection](../../internal/agentbox/secret_injection.go#L1-L73) — profile narrowing and create-time account reload; native/chat regression in `aws_environment_linux_test.go`.
3. <a id="source-3"></a> [Auth status](../../cmd/yoloctl/account_auth.go#L1-L431) — value-free summary through `awsSecretsPresent` and retained native errors.
4. <a id="source-4"></a> [Verified installer](../../internal/features/aws-install.sh#L1-L36) — signing-key fingerprint, architecture selection and verification before execution.
5. <a id="source-5"></a> [AWS invalidation test](../../internal/agentboximage/aws_recipe_test.go#L1-L56) — installer/key bytes in the recipe consumed by `composedImageRef`.
6. <a id="source-6"></a> [Secret input](../../cmd/yoloctl/account.go#L673) — `newAccountSecretSetCmd`, private stdin/from-env.
7. <a id="source-7"></a> [Example validation](../../cmd/yoloctl/account_auth_aws_test.go#L1-L128) — actual Cobra flags, strict custom-grant loading and policy composition; implementations in `agentbox.go`, `internal/capabilities/user.go` and `internal/agentbox/compose.go`.
8. <a id="source-8"></a> [Official OpenAI Bedrock guidance](https://learn.chatgpt.com/docs/amazon-bedrock) — documented provider/region configuration, checked 2026-11-06; not installed-provider execution proof.
9. <a id="source-9"></a> [Codex profile](../../internal/agentprofile/builtin/codex.yaml#L1-L123) — four-key AWS declarations; OpenAI auth remains native.
10. <a id="source-10"></a> [Claude Bedrock guidance](https://code.claude.com/docs/en/amazon-bedrock) — documented provider/region configuration, checked 2026-11-06.
11. <a id="source-11"></a> [AWS installation reference](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) — official architectures, runtime requirements and signing key, checked 2026-11-06.
12. <a id="source-12"></a> [Host wildcard rule](../../internal/agentbox/compose.go#L300-L331) — `lintHostPattern`.
