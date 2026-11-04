package features

// Gemini needs image-owned settings in addition to its npm package. Keep the
// recipe in the existing Dockerfile installer so custom features can express the
// same setup without a new schema or an agent-name branch in the image builder.
//
// System settings override user/workspace settings; system defaults can be
// overridden. yoloctl owns CLI updates. Shell PTYs remain opt-in: users may
// enable tools.shell.enableInteractiveShell after granting fs.pty.allocate.
// Usage statistics default off: the built-in policy does not grant Clearcut
// egress, so its event-triggered retries would otherwise generate denials.
// Neither file writes into the account's agent-owned ~/.gemini directory.
// Upstream v0.62.0: packages/cli/src/config/{settings,settingsSchema}.ts.
var geminiInstallDockerfile = npmPackageInstallDockerfile("@google/gemini-cli", "latest") + `
RUN mkdir -p /etc/gemini-cli \
    && printf '%s\n' '{"general":{"enableAutoUpdate":false,"enableAutoUpdateNotification":false}}' > /etc/gemini-cli/settings.json \
    && printf '%s\n' '{"privacy":{"usageStatisticsEnabled":false},"tools":{"shell":{"enableInteractiveShell":false}}}' > /etc/gemini-cli/system-defaults.json \
    && chmod 0644 /etc/gemini-cli/settings.json /etc/gemini-cli/system-defaults.json`
