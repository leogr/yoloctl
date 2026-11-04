#!/usr/bin/env bash
# Prompt-free macOS host gate; Python selects YOLOCTL_HOME and queries roots.
# --check only observes an explicitly selected existing space.
set -euo pipefail
exec python3 -B "$(dirname "${BASH_SOURCE[0]}")/lima_stack.py" "$@" </dev/null
