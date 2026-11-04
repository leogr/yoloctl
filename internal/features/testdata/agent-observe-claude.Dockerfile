RUN <<'AGENT_OBSERVE_INSTALL'
#!/bin/sh
set -eu
python=
for candidate in /usr/local/bin/python3 /usr/bin/python3; do
    if [ -x "$candidate" ] && "$candidate" -I -S -c 'import fcntl, json, os, re, sys, time; sys.exit(sys.version_info < (3, 8))'; then python=$candidate; break; fi
done
if [ -z "$python" ] && command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y --no-install-recommends python3
    rm -rf /var/lib/apt/lists/*
    if /usr/bin/python3 -I -S -c 'import fcntl, json, os, re, sys, time; sys.exit(sys.version_info < (3, 8))'; then python=/usr/bin/python3; fi
fi
if [ -z "$python" ]; then
    echo 'yolo.agent.observe needs Python 3.8+ (python3) in the image; use a base that provides it or apt-get' >&2
    exit 1
fi
install -d /usr/local/lib/yoloctl /usr/local/libexec/yoloctl /etc/claude-code/managed-settings.d
base64 -d > /usr/local/lib/yoloctl/agent-observe.py <<'AGENT_OBSERVE_SOURCE'
{{AGENT_OBSERVE_SOURCE}}AGENT_OBSERVE_SOURCE
printf '%s\n' '#!/bin/sh' "exec $python -I -S /usr/local/lib/yoloctl/agent-observe.py" > /usr/local/libexec/yoloctl/agent-observe
printf '%s\n' '#!/bin/sh' "exec $python -I -S /usr/local/lib/yoloctl/agent-observe.py statusline" > /usr/local/libexec/yoloctl/agent-statusline
chmod 0755 /usr/local/libexec/yoloctl/agent-observe /usr/local/libexec/yoloctl/agent-statusline
printf '%s\n' '{"hooks":{"Notification":[{"hooks":[{"type":"command","command":"/usr/local/libexec/yoloctl/agent-observe","async":true}]}],"PostModelSwitch":[{"hooks":[{"type":"command","command":"/usr/local/libexec/yoloctl/agent-observe","async":true}]}],"SessionEnd":[{"hooks":[{"type":"command","command":"/usr/local/libexec/yoloctl/agent-observe","async":true}]}],"SessionStart":[{"hooks":[{"type":"command","command":"/usr/local/libexec/yoloctl/agent-observe","async":true}]}],"Stop":[{"hooks":[{"type":"command","command":"/usr/local/libexec/yoloctl/agent-observe","async":true}]}],"StopFailure":[{"hooks":[{"type":"command","command":"/usr/local/libexec/yoloctl/agent-observe","async":true}]}],"UserPromptSubmit":[{"hooks":[{"type":"command","command":"/usr/local/libexec/yoloctl/agent-observe","async":true}]}]},"statusLine":{"type":"command","command":"/usr/local/libexec/yoloctl/agent-statusline"}}' > /etc/claude-code/managed-settings.d/40-yoloctl-agent.json
AGENT_OBSERVE_INSTALL