"""Initialize missing defaults in the selected native account, then exec agy."""
import json
import os
from pathlib import Path
import sys
import tempfile

# yolo.agent.observe-agy's launcher. agy has no system settings layer, so
# a new account reports through its own status line when the image has it.
OBSERVE = '/usr/local/libexec/yoloctl/agent-statusline-agy'

os.environ["AGY_CLI_DISABLE_AUTO_UPDATE"] = "true"
args = sys.argv[1:]
if not args or args[0] not in ("--help", "-h", "--version", "help", "update"):
    state = Path.home() / ".gemini" / "antigravity-cli"
    state.mkdir(parents=True, exist_ok=True)
    # No system defaults are documented by upstream. Seed a new account once;
    # never replace an existing settings file or import host settings implicitly.
    seed = {"enableTelemetry": False, "enableTerminalSandbox": False}
    if os.access(OBSERVE, os.X_OK):
        # The launcher prints nothing of its own: stacking keeps agy's line.
        seed["statusLine"] = {"type": "command", "command": OBSERVE, "stack_with_default": True}
    with tempfile.NamedTemporaryFile(mode="w", prefix=".settings-", dir=state) as out:
        try:
            json.dump(seed, out)
            out.flush()
            os.fsync(out.fileno())
            os.link(out.name, state / "settings.json")
        except FileExistsError:
            pass
    # The helper/skill is a read-only infrastructure mount only with yolo.space.operator.
    # A symlink avoids root-created directories inside the native account bind.
    # An old symlink after revocation grants no authority and has no target.
    source = Path("/opt/yoloctl/helper/skill")
    if (source / "SKILL.md").is_file():
        skills = state / "skills"
        skills.mkdir(exist_ok=True)
        try:
            (skills / "yolo-box-operator").symlink_to(source, target_is_directory=True)
        except FileExistsError:
            pass

os.execv("/home/sandbox/.local/share/yoloctl/agy/agy", ["agy", *args])
