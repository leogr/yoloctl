# VM, box and AIRC trust stores

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; recipe identity `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; AIRC selection `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Firecracker assembly pins Ubuntu's `ca-certificates`
20260816ubuntu0.24.04.1 by SHA-256. That release distrusts emSign ECC Root CA -
G3 (LP: #2171507). Assembly runs no package scripts. It first drops the base
image's Mozilla certificates, generated links and bundle, refusing local CAs
and unmanaged store entries. After extraction it regenerates
`/etc/ca-certificates.conf`, one `.pem` link and bundle entry per packaged
certificate, and OpenSSL subject-hash links, as a fresh installation would.
A store that still holds emSign ECC Root CA - G3 or AAA Certificate Services
is refused. The current image recipe version is 3; automatic image selection
includes it in the cache identity.
[1](#source-1) [2](#source-2) [3](#source-3) [4](#source-4) [5](#source-5)

[FACT] Subject hashes implement OpenSSL's canonical-name hash. Tests pin
OpenSSL 3.0.13 values for string types, whitespace, case and multi-valued names.
The Python reference builder mirrors the regeneration: it uses `openssl` for
hashes and refuses redirected store directories. The opt-in real-input assembly
test reads the resulting ext4 store. [6](#source-6) [7](#source-7)
[8](#source-8) [9](#source-9)

[FACT] Lima provisioning upgrades an installed `ca-certificates` older than
that release through apt, whose package scripts regenerate `/etc/ssl/certs`
while keeping local CAs and deselections. A current package costs no apt work
on later boots. Provisioning fails if apt cannot supply the release. Existing
instances keep their stored provisioning; `space recreate` builds the instance
from the current template. Script tests use fake package tools.
[10](#source-10) [11](#source-11) [12](#source-12) [26](#source-26)

[FACT] Firecracker guests run containerd, nerdctl and BuildKit from
nerdctl-full 2.3.5, whose Dockerfile selects Go 1.26; their containerd and
BuildKit configurations name no registry CA. Lima installs its own containerd
(`containerd.system`); Lima v2.2.1 defaults to nerdctl-full 2.4.1. Both nerdctl
releases predate the Go 1.26.10/1.27.3 verifier fix. Go 1.26.5 trusts the first
readable system bundle plus every `/etc/ssl/certs` entry except links within
that directory. New VM images carry no OpenShell archive any more (Firecracker recipe
version 4); the native 0.1.6 runtime is embedded in yoloctl instead of
fetched, and only two historical v0.0.72 binary hashes remain in a read-only
compatibility guard. These tests use no
live registry.
[13](#source-13) [14](#source-14) [15](#source-15) [16](#source-16)
[17](#source-17) [18](#source-18) [19](#source-19) [20](#source-20)

[FACT] The base box image installs `ca-certificates` from refreshed indexes, so
a forced `image refresh` selects the current package. Composed tags hash the
base reference, not its image digest. Existing composed images, plain
`box recreate`, and new boxes with the same features therefore keep the
previous base until `box reconcile BOX --rebuild` rebuilds the tag on the
refreshed base. CLI guidance names that sequence.
[21](#source-21) [22](#source-22) [23](#source-23) [24](#source-24) [25](#source-25)

[FACT] The AIRC source pin selects rustls-webpki 0.103.16 and webpki-roots
1.0.10. Its verifier rejects compromised emSign intermediate keys even under
supplied G3/AAA anchors; unrelated custom trust remains supported. Upstream
regressions use a fixed verification time and require `CertDistrusted`, not
missing-root rejection. This is separate from VM registry/OpenShell trust.
The pin enters feature image recipes and their composed identity; existing
boxes change only through an explicit rebuild/recreate.
[27](#source-27) [28](#source-28) [29](#source-29) [30](#source-30) [31](#source-31)

## Sources

1. <a id="source-1"></a> [Ubuntu changes](https://launchpad.net/ubuntu/+archive/primary/+files/ca-certificates_20260816ubuntu0.24.04.1_source.changes) — noble-security upload, G3 blacklist and Mozilla 2.90.
2. <a id="source-2"></a> [Runtime pins](../../assets/vm/firecracker/runtime-pins.json#L2-L5) — `ca-certificates.deb` URL and SHA-256.
3. <a id="source-3"></a> [Trust store](../../internal/firecracker/truststore_linux.go#L54) — `distrustedRoots`, `dropManagedCertificates` at L65, `rebuildTrustStore` at L91, `opensslSubjectHash` at L225.
4. <a id="source-4"></a> [Assembly order](../../internal/firecracker/assemble_linux.go#L650-L662) — drop before package extraction, regenerate after.
5. <a id="source-5"></a> [Recipe identity](../../internal/firecracker/recipe_linux.go#L19) — `imageRecipeVersion`, `defaultRecipe` and `recipeIdentity` include the version in the hashed cache identity.
6. <a id="source-6"></a> [Hash and store tests](../../internal/firecracker/truststore_linux_test.go#L29) — `TestOpenSSLSubjectHash`, `TestTrustStoreRegeneration` at L121, `TestTrustStoreRefusals` at L211, `TestRecipePinsPatchedCACertificates` at L263.
7. <a id="source-7"></a> [Reference builder](../../assets/vm/firecracker/build.py#L198) — `drop_managed_certificates`, `rebuild_trust_store` at L235.
8. <a id="source-8"></a> [Reference builder tests](../../scripts/vm/firecracker/test_build.py#L42) — `TrustStoreTests`.
9. <a id="source-9"></a> [Real-input check](../../internal/firecracker/assemble_linux_test.go#L127) — `verifyImageTrustStore` from `TestPortableImageAssembly`.
10. <a id="source-10"></a> [Lima provisioning](../../assets/lima/yoloctl.yaml.tmpl#L69-L96) — minimum CA release, upgrade and failure.
11. <a id="source-11"></a> [Provisioning tests](../../internal/lima/provision_test.go#L129) — `TestCACertificatesProvisioningUpgrade`.
12. <a id="source-12"></a> [Lima templates](../../cmd/yoloctl/space.go#L592) — create-time template; a missing instance is created at L681.
13. <a id="source-13"></a> [nerdctl v2.3.5 Dockerfile](https://github.com/containerd/nerdctl/blob/v2.3.5/Dockerfile#L20-L46) — containerd v2.3.3, BuildKit v0.31.2, Go 1.26.
14. <a id="source-14"></a> [Guest containerd configuration](../../internal/firecracker/assemble_linux.go#L733).
15. <a id="source-15"></a> [Guest BuildKit configuration](../../internal/vmguest/buildkit_linux.go#L22).
16. <a id="source-16"></a> [Lima containerd selection](../../assets/lima/yoloctl.yaml.tmpl#L29-L31).
17. <a id="source-17"></a> [Lima v2.2.1 default archive](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/limayaml/containerd.yaml#L1-L7) — nerdctl-full 2.4.1, released 2026-10-02; v2.3.5 was released 2026-07-20.
18. <a id="source-18"></a> [Go security release](https://groups.google.com/g/golang-announce/c/66pojueaQek) — Go 1.26.10/1.27.3, released 2026-10-19.
19. <a id="source-19"></a> [Go 1.26.5 system roots](https://github.com/golang/go/blob/go1.26.5/src/crypto/x509/root_unix.go#L32-L104) — first bundle file, unique directory entries; paths are in `root_linux.go`.
20. <a id="source-20"></a> [Historical OpenShell hashes](../../internal/container/container.go#L1) — the two retained v0.0.72 binary hashes in `legacy_openshell.go` L20–L21 (retirement delta); `assets/vm/firecracker/pins.json` no longer lists `openshell.tgz` (retirement tree `3cc5e841`).
21. <a id="source-21"></a> [Base Dockerfile](../../assets/containers/agentbox/Dockerfile#L27-L31) — index refresh and `ca-certificates`.
22. <a id="source-22"></a> [Composed identity](../../internal/agentboximage/composed.go#L69) — hash inputs; an existing tag is reused at L109.
23. <a id="source-23"></a> [Forced rebuild](../../cmd/yoloctl/agentbox_reconcile.go#L162) — `runRebuild`.
24. <a id="source-24"></a> [Refresh notice](../../cmd/yoloctl/image.go#L124).
25. <a id="source-25"></a> [Recreate help](../../cmd/yoloctl/agentbox.go#L2491).
26. <a id="source-26"></a> [Ubuntu source package](https://launchpad.net/ubuntu/+archive/primary/+sourcefiles/ca-certificates/20260816ubuntu0.24.04.1/ca-certificates_20260816ubuntu0.24.04.1.tar.xz) — `update-ca-certificates` and maintainer scripts (sha256 f592cceb…).
27. <a id="source-27"></a> [Pinned AIRC verifier](https://github.com/leogr/airc/blob/b76ba355863cc3eb51900a313d7647d9190eedfa/Cargo.lock#L1403-L1412) — rustls-webpki version/checksum.
28. <a id="source-28"></a> [Pinned AIRC roots](https://github.com/leogr/airc/blob/b76ba355863cc3eb51900a313d7647d9190eedfa/Cargo.lock#L2073-L2080) — webpki-roots version/checksum.
29. <a id="source-29"></a> [Pinned distrust controls](https://github.com/leogr/airc/blob/b76ba355863cc3eb51900a313d7647d9190eedfa/tests/tls_verifier.rs#L11-L135) — fixed time, supplied anchors, unrelated trust and root removal.
30. <a id="source-30"></a> [AIRC installer](../../internal/features/airc.go#L36-L82) — selected manifest, verified archive and locked build.
31. <a id="source-31"></a> [Composed identity](../../internal/agentboximage/composed.go#L48-L76) — feature Dockerfile hashing and create/recreate selection.
