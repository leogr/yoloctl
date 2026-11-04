package features

// Node.js, npm and npx are supplied together by the base image. Reuse that
// runtime so enabling development tooling cannot replace an agent's Node
// version. Validate the paths used by network.npm, including on custom bases.
//
// Configure the user's npmrc rather than an image ENV: root-run feature builds
// must keep their own cache, and confined SSH commands must see the defaults
// without relying on inherited environment variables or shell startup files.
// npm config set preserves unrelated settings on a customized base image.
const npmInstallDockerfile = `RUN /usr/bin/node --version \
    && /usr/bin/npm --version \
    && /usr/bin/npx --version \
    && install -d -o sandbox -g sandbox /home/sandbox/.npm-global /home/sandbox/.cache /home/sandbox/.cache/npm \
    && /usr/bin/npm config set \
        prefix=/home/sandbox/.npm-global \
        cache=/home/sandbox/.cache/npm \
        --location=user --userconfig=/home/sandbox/.npmrc \
    && chown sandbox:sandbox /home/sandbox/.npmrc
ENV PATH=/home/sandbox/.npm-global/bin:$PATH`
