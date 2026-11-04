# syntax=docker/dockerfile:1
# yoloctl default workdir container image.
# openshell-sandbox is NOT baked in — it is bind-mounted from the VM at
# /opt/openshell/bin/openshell-sandbox when the container starts.

FROM ubuntu:24.04

# A fresh value reruns installers without --no-cache, which also resets
# BuildKit download-cache mounts. This is a build argument, not runtime ENV.
ARG YOLOCTL_IMAGE_REFRESH

ENV DEBIAN_FRONTEND=noninteractive
ENV NODE_MAJOR=22
ENV PYTHON_VERSION=3.13

# Required by openshell-sandbox + dev convenience.
# - iproute2, iptables: ip netns + veth (sandbox proxy mode)
# - ca-certificates: TLS upstream
# - software-properties-common: add-apt-repository for the Python PPA
# - build-essential: native module compilation (npm, pip)
# - git, curl: dev tooling
# - procps, dnsutils, netcat-openbsd: diagnostics
# - gnupg: NodeSource setup script
# Keep downloaded debs across failed builds, outside image layers. The custom
# archive path also avoids Ubuntu's docker-clean hook deleting the cache.
# Refresh indexes normally; only package archives are shared, under a lock.
RUN --mount=type=cache,id=yoloctl-apt-archives-v1,target=/var/cache/yoloctl/apt,sharing=locked \
    apt-get -o Acquire::Retries=3 update --error-on=any \
    && apt-get -o Acquire::Retries=3 -o Dir::Cache::Archives=/var/cache/yoloctl/apt \
        -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends \
        ca-certificates \
        iproute2 \
        iptables \
        software-properties-common \
        build-essential \
        procps \
        dnsutils \
        netcat-openbsd \
        curl \
        git \
        gnupg \
    && rm -rf /var/lib/apt/lists/*

# Node.js 22 from NodeSource.
RUN --mount=type=cache,id=yoloctl-apt-archives-v1,target=/var/cache/yoloctl/apt,sharing=locked \
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get -o Acquire::Retries=3 -o Dir::Cache::Archives=/var/cache/yoloctl/apt \
        -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Python 3.13 from deadsnakes. 3.13 lives alongside Ubuntu's system 3.12;
# /usr/local/bin symlinks make `python`/`python3` resolve to 3.13 first.
# ensurepip bootstraps pip into the new interpreter.
# Register without the implicit best-effort update: retry transient index
# failures, then fail here instead of reporting misleading missing packages.
RUN --mount=type=cache,id=yoloctl-apt-archives-v1,target=/var/cache/yoloctl/apt,sharing=locked \
    add-apt-repository -y --no-update ppa:deadsnakes/ppa \
    && apt-get -o Acquire::Retries=3 update --error-on=any \
    && apt-get -o Acquire::Retries=3 -o Dir::Cache::Archives=/var/cache/yoloctl/apt \
        -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends \
        "python${PYTHON_VERSION}" \
        "python${PYTHON_VERSION}-venv" \
    && rm -rf /var/lib/apt/lists/* \
    && "python${PYTHON_VERSION}" -m ensurepip --upgrade \
    && ln -sf "/usr/bin/python${PYTHON_VERSION}" /usr/local/bin/python \
    && ln -sf "/usr/bin/python${PYTHON_VERSION}" /usr/local/bin/python3

# ubuntu user (UID/GID 1000) ships with the base image but yoloctl never
# uses it; remove it so the entrypoint shim can remap `sandbox` to UID 1000
# (the typical host developer UID) without colliding.
RUN userdel -r ubuntu 2>/dev/null || true \
    && groupdel ubuntu 2>/dev/null || true

# sandbox user with fixed UID/GID 2000 at build time. openshell-sandbox
# looks up the user by NAME, so the entrypoint can remap the runtime UID.
#
# HOME is /home/sandbox (not /workdir) so:
#   - openshell-sandbox SSH sessions land with HOME=/home/sandbox, so
#     `~/.codex/auth.json`, `~/.claude/.credentials.json`, and Gemini's
#     `~/.gemini/oauth_creds.json` resolve to /home/sandbox/.{codex,claude,gemini}
#     — the paths the agent-state bind
#     mount targets. With HOME=/workdir they'd land under /workdir
#     and the agent-state mount would be invisible.
#   - /workdir remains the WORKDIR (host-mounted code), separate from
#     long-lived agent state. Mirrors how host-native installs separate
#     `$HOME/.<agent>` from the working tree.
# `-m` creates /home/sandbox with sandbox:sandbox ownership at build
# time; the agent-variant Dockerfiles no longer need to provision it.
RUN groupadd -g 2000 sandbox \
    && useradd -u 2000 -g sandbox -d /home/sandbox -m -s /bin/bash sandbox \
    && mkdir -p /workdir /home/sandbox/.npm-global \
    && chown sandbox:sandbox /workdir /home/sandbox/.npm-global

# Global npm prefix for the sandbox user's CLI installs. Agent-CLI features
# (claude, codex, gemini, opencode, pi) `npm install -g --prefix=/home/sandbox/.npm-global` into a
# feature-composed image FROM this base, and the runtime autoupdate
# (`nerdctl exec --user sandbox npm install -g --prefix=…`) refreshes them in
# place. Putting the dir + PATH here (instead of in the retired agent-variant
# images) is what makes `image = base + composed agent feature` work. The
# npm feature install sets sandbox ownership in its own layer, so the
# sandbox-user autoupdate can overwrite it without another image-sized copy.
ENV PATH=/home/sandbox/.npm-global/bin:$PATH

WORKDIR /workdir

# yoloctl box entrypoint shim. Runs as root before openshell-sandbox,
# remaps the sandbox user's UID/GID to match the host developer (sourced
# from YOLOCTL_HOST_UID/GID env vars). No-op when those vars are unset.
COPY yoloctl-entrypoint.sh /usr/local/bin/yoloctl-entrypoint
RUN chmod 0755 /usr/local/bin/yoloctl-entrypoint

# No ENTRYPOINT / CMD: the runtime supplies the full argv (entrypoint shim
# + side-loaded openshell-sandbox binary + its policy flags + the child).
