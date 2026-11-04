package features

import (
	"os/exec"
	"testing"
)

func TestAgyLauncherPreservesSettingsAndExecsNativeCLI(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	// Mock only the exec boundary and home. Exercise the shipped initializer
	// twice: explicit user settings must survive concurrent-account launches.
	program := `import json, os, pathlib, sys, tempfile
from unittest.mock import patch
source = pathlib.Path("agy-launch.py").read_text()
with tempfile.TemporaryDirectory() as temp:
    home = pathlib.Path(temp)
    with patch("pathlib.Path.home", return_value=home), patch("os.execv") as launch, patch.object(sys, "argv", ["agy", "--sandbox=false", "--print", "hello"]):
        exec(source, {})
        settings = home / ".gemini/antigravity-cli/settings.json"
        assert json.loads(settings.read_text()) == {"enableTelemetry":False, "enableTerminalSandbox":False}
        settings.write_text('{"modelProvider":"gemini","customSetting":true}')
        exec(source, {})
        assert json.loads(settings.read_text()) == {"modelProvider":"gemini", "customSetting":True}
        launch.assert_called_with("/home/sandbox/.local/share/yoloctl/agy/agy", ["agy", "--sandbox=false", "--print", "hello"])
        assert not list(settings.parent.glob(".settings-*"))
    # With the observe launcher in the image, a NEW account's settings name
    # it; an existing file is never rewritten.
    observe = "/usr/local/libexec/yoloctl/agent-statusline-agy"
    real_access = os.access
    with patch("pathlib.Path.home", return_value=home / "new"), patch("os.execv"), patch.object(sys, "argv", ["agy"]), \
            patch("os.access", side_effect=lambda p, m: p == observe or real_access(p, m)):
        exec(source, {})
        seeded = json.loads((home / "new/.gemini/antigravity-cli/settings.json").read_text())
        assert seeded["statusLine"] == {"type": "command", "command": observe, "stack_with_default": True}, seeded
        exec(source, {})
    with patch("pathlib.Path.home", return_value=home), patch("os.execv"), patch.object(sys, "argv", ["agy"]), \
            patch("os.access", return_value=True):
        exec(source, {})
        assert json.loads(settings.read_text()) == {"modelProvider":"gemini", "customSetting":True}
`
	if out, err := exec.Command(python, "-I", "-c", program).CombinedOutput(); err != nil {
		t.Fatalf("agy launcher regression: %v\n%s", err, out)
	}
}
