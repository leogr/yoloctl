"""Exercise smoke routing/isolation/failure handling without launching a VM."""
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import unittest


SCRIPTS = Path(__file__).parent / "smoke"


def executable(path, content):
    if path.name == "test cli":
        first, rest = content.split("\n", 1)
        if "python" in first:
            probe = ('import sys, os, json\n'
                     'if sys.argv[1:] == ["version", "--json"]:\n'
                     '    print(json.dumps({"config_root": os.environ["YOLOCTL_HOME"]})); sys.exit()\n')
        else:
            probe = ('if [ "$1" = version ]; then\n'
                     'python3 -c \'import json,os; print(json.dumps({"config_root":os.environ["YOLOCTL_HOME"]}))\'\n'
                     'exit 0\nfi\n')
        content = first + "\n" + probe + rest
    path.write_text(content)
    path.chmod(0o700)


class VMSmokeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="vm smoke ")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("SMOKE_")}
        self.env["TMPDIR"] = str(self.root)
        self.env["YOLOCTL"] = str(self.root / "test cli")
        executable(Path(self.env["YOLOCTL"]), "#!/bin/sh\nexit 0\n")

    def run_script(self, name, *args):
        result = subprocess.run(["bash", str(self.scripts / name), *args], env=self.env,
                                capture_output=True, text=True, timeout=10)
        if name == "vm-backends.sh":
            for line in result.stdout.splitlines():
                marker = "backend acceptance logs and isolated state: "
                if marker in line:
                    self.addCleanup(shutil.rmtree, line.split(marker, 1)[1])
        return result

    def run_common(self, function, *args):
        return subprocess.run(
            ["bash", "-euo", "pipefail", "-c", 'source "$1"; shift; "$@"',
             "smoke-test", str(SCRIPTS / "common.sh"), function, *args],
            env=self.env, capture_output=True, text=True, timeout=10,
        )

    def test_hostile_home_is_replaced(self):
        sentinel = self.root / "sentinel"
        sentinel.mkdir()
        (sentinel / "keep").write_text("untouched")
        self.env.update(YOLOCTL_HOME=str(sentinel), SMOKE_ROOT=str(self.root / "owned"),
                        SMOKE_BACKEND="lima")
        tools = self.root / "tools"
        tools.mkdir()
        for name in ("limactl", "ssh"):
            executable(tools / name, "#!/bin/sh\nexit 0\n")
        self.env["PATH"] = str(tools) + os.pathsep + os.environ["PATH"]
        result = subprocess.run(
            ["bash", "-euo", "pipefail", "-c",
             'source "$1"; smoke_init; printf "%s\\n%s\\n" "$YOLOCTL_HOME" "$SMOKE_CONFIG_ROOT"',
             "test", str(SCRIPTS / "common.sh")], env=self.env,
            capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [str(self.root / "owned/home")] * 2)
        self.assertEqual(list(sentinel.iterdir()), [sentinel / "keep"])
        self.assertEqual((sentinel / "keep").read_text(), "untouched")
        # Every shell lifecycle entry delegates root ownership to common.sh;
        # the matrix driver delegates to those same entries in isolated phases.
        for script in SCRIPTS.glob("*.sh"):
            if script.name == "lima-stack.sh":
                self.assertIn("lima_stack.py", script.read_text(), script)
                continue  # Standalone runner has its own hostile-home regression.
            if script.name == "common.sh":
                continue
            self.assertIn("common.sh", script.read_text(), script)
            if script.name != "vm-backends.sh":
                self.assertIn("smoke_init", script.read_text(), script)

    def test_startup_probe_selects_lima_and_explicit_memory(self):
        for name in ("vm-startup.sh", "common.sh"):
            shutil.copy(SCRIPTS / name, self.scripts)
        tools = self.root / "tools"
        tools.mkdir()
        for name in ("limactl", "ssh"):
            executable(tools / name, "#!/bin/sh\necho fixture\n")
        self.env.update(PATH=str(tools) + os.pathsep + os.environ["PATH"],
                        VM_START_SAMPLES="1", VM_START_SETTLE_SECONDS="0",
                        SMOKE_VM_MEMORY="5GiB", AUDIT=str(self.root / "calls.jsonl"))
        executable(Path(self.env["YOLOCTL"]), f"#!{sys.executable}\n" + '''
import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ["AUDIT"], "a") as output:
    output.write(json.dumps(args) + "\\n")
if args[:2] == ["space", "create"]:
    manifest = pathlib.Path(os.environ["YOLOCTL_HOME"]) / "spaces" / args[2] / "space.yaml"
    manifest.parent.mkdir(parents=True)
    manifest.write_text("fixture\\n")
''')
        result = self.run_script("vm-startup.sh")
        fixture = None
        for line in result.stdout.splitlines():
            if line.startswith(">>> fixture: "):
                fixture, report = line.removeprefix(">>> fixture: ").split("; reports: ")
                self.addCleanup(shutil.rmtree, report, ignore_errors=True)
                self.addCleanup(shutil.rmtree, fixture, ignore_errors=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(line) for line in Path(self.env["AUDIT"]).read_text().splitlines()]
        create = calls[0]
        self.assertEqual(create[:3], ["space", "create", "smoke-startup"])
        self.assertEqual(create[-6:], ["--backend", "lima", "--memory", "5GiB", "--cpus", "4"])
        self.assertEqual(calls[-1], ["space", "destroy", "smoke-startup", "--force"])
        self.assertIsNotNone(fixture)
        self.assertFalse(Path(fixture).exists())
        # The probe uses systemd-specific guest checks. An inherited backend
        # must not silently switch it to Firecracker or create any fixture.
        self.env["SMOKE_BACKEND"] = "firecracker"
        before = Path(self.env["AUDIT"]).read_text()
        result = self.run_script("vm-startup.sh")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("measures Lima provisioning", result.stderr)
        self.assertEqual(Path(self.env["AUDIT"]).read_text(), before)

    def test_optional_pristine_lima_config_check_never_removes_conflicts(self):
        self.env.update(LIMA_HOME=str(self.root / "lima"), SMOKE_ASSERT_NO_LIMA_GLOBAL_CONFIG="1")
        self.assertEqual(self.run_common("smoke_assert_lima_globals_absent").returncode, 0)
        config = self.root / "lima/_config"
        config.mkdir(parents=True)
        override = config / "override.yaml"
        override.write_text("containerd: {}\n")
        self.assertNotEqual(self.run_common("smoke_assert_lima_globals_absent").returncode, 0)
        self.assertEqual(override.read_text(), "containerd: {}\n")
        self.env["SMOKE_ASSERT_NO_LIMA_GLOBAL_CONFIG"] = "0"
        self.assertEqual(self.run_common("smoke_assert_lima_globals_absent").returncode, 0)

    def test_image_identity_probe_uses_manifest_recipe_and_no_entrypoint(self):
        self.env["SMOKE_SPACE"] = "fixture"
        self.env["IMAGE_PROBE_STATE"] = str(self.root)
        executable(Path(self.env["YOLOCTL"]), '''#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
args = sys.argv[1:]
if args == ["box", "inspect", "coder", "--space", "fixture", "--json"]:
    print(json.dumps({"manifest": {"runtime_image": "yoloctl/agentbox-composed:prepared"}}))
else:
    assert args[:14] == ["space", "ssh", "fixture", "--root", "--", "nerdctl",
                         "run", "--rm", "--network", "none", "--entrypoint", "/bin/sh",
                         "yoloctl/agentbox-composed:prepared", "-ceu"], args
    assert args[15:] == ["image-identity", str(os.getuid()), str(os.getgid())], args
    env = os.environ.copy()
    env["PATH"] = str(pathlib.Path(env["IMAGE_PROBE_STATE"]) / "probe-tools") + ":" + env["PATH"]
    sys.exit(subprocess.run(["sh", "-ceu", *args[14:]], env=env).returncode)
''')
        probe_tools = self.root / "probe-tools"
        probe_tools.mkdir()
        executable(probe_tools / "stat", '#!/bin/sh\nid -u | tr -d "\\n"; printf ":"; id -g\n')
        # Execute the real probe's shell with simulated filesystem/identity
        # results. This checks parsing and failures, not a container runtime.
        executable(probe_tools / "id", '#!/bin/sh\n/usr/bin/id "$1"\n')
        executable(probe_tools / "find", '#!/bin/sh\nexit 0\n')
        result = self.run_common("smoke_check_image_identity", "coder")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("prepared image identity:", result.stdout)
        executable(probe_tools / "find", '#!/bin/sh\necho /home/sandbox/.npm-global/wrong-owner\n')
        result = self.run_common("smoke_check_image_identity", "coder")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("npm prefix ownership differs", result.stderr)

    def ensure_space_fixture(self, existing, kernel, lifecycle_status=0,
                             kernel_status=0, release="7.2.11"):
        root = Path(tempfile.mkdtemp(prefix="ensure ", dir=self.root))
        self.env.update(SMOKE_SPACE="fixture", SMOKE_WORKDIR=str(root / "workdir"),
                        SMOKE_CONFIG_ROOT=str(root / "home"), XDG_CONFIG_HOME=str(root / "config"), AUDIT=str(root / "calls.jsonl"),
                        LIFECYCLE_STATUS=str(lifecycle_status), KERNEL_STATUS=str(kernel_status),
                        KERNEL_RELEASE=release)
        self.env.pop("SMOKE_EXPECT_KERNEL_RELEASE", None)
        if kernel:
            self.env["SMOKE_EXPECT_KERNEL_RELEASE"] = "7.2.11"
        if existing:
            manifest = root / "home/spaces/fixture/space.yaml"
            manifest.parent.mkdir(parents=True)
            manifest.write_text("fixture\n")
        executable(Path(self.env["YOLOCTL"]), f"#!{sys.executable}\n" + '''
import json, os, sys
args = sys.argv[1:]
with open(os.environ["AUDIT"], "a") as output:
    output.write(json.dumps(args) + "\\n")
if args[:2] in (["space", "create"], ["space", "start"]):
    status = int(os.environ["LIFECYCLE_STATUS"])
    if status:
        print("fixture lifecycle failure", file=sys.stderr)
    sys.exit(status)
if args == ["space", "ssh", "fixture", "--", "uname", "-r"]:
    print(os.environ["KERNEL_RELEASE"])
    sys.exit(int(os.environ["KERNEL_STATUS"]))
sys.exit(37)
''')

    def run_ensure_space(self, conditional):
        # An if condition disables errexit inside the function even with -e.
        invocation = ('if smoke_ensure_space; then exit 0; else exit "$?"; fi'
                      if conditional else 'smoke_ensure_space; exit "$?"')
        return subprocess.run(
            ["bash", "-euo", "pipefail", "-c",
             ('set -e; ' if conditional else 'set +e; ') +
             'source "$1"; SMOKE_VM_ARGS=(--backend lima); ' + invocation,
             "smoke-test", str(SCRIPTS / "common.sh")],
            env=self.env, capture_output=True, text=True, timeout=10,
        )

    def ensure_space_calls(self):
        return [json.loads(line) for line in Path(self.env["AUDIT"]).read_text().splitlines()]

    def test_ensure_space_propagates_create_and_start_failures_without_probe(self):
        for existing in (False, True):
            for kernel in (False, True):
                for conditional in (False, True):
                    with self.subTest(existing=existing, kernel=kernel, conditional=conditional):
                        self.ensure_space_fixture(existing, kernel, lifecycle_status=23)
                        result = self.run_ensure_space(conditional)
                        self.assertEqual(result.returncode, 23, result.stderr)
                        self.assertIn("fixture lifecycle failure", result.stderr)
                        calls = self.ensure_space_calls()
                        self.assertEqual(len(calls), 1, calls)
                        self.assertEqual(calls[0][:3],
                                         ["space", "start" if existing else "create", "fixture"])

    def test_ensure_space_success_with_and_without_kernel_probe(self):
        for existing in (False, True):
            for kernel in (False, True):
                for conditional in (False, True):
                    with self.subTest(existing=existing, kernel=kernel, conditional=conditional):
                        self.ensure_space_fixture(existing, kernel)
                        result = self.run_ensure_space(conditional)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        calls = self.ensure_space_calls()
                        self.assertEqual(calls[0][:3],
                                         ["space", "start" if existing else "create", "fixture"])
                        self.assertEqual(len(calls), 2 if kernel else 1, calls)
                        if kernel:
                            self.assertEqual(calls[1],
                                             ["space", "ssh", "fixture", "--", "uname", "-r"])

    def test_ensure_space_propagates_failed_kernel_probe_even_with_expected_output(self):
        for existing in (False, True):
            for conditional in (False, True):
                with self.subTest(existing=existing, conditional=conditional):
                    self.ensure_space_fixture(existing, True, kernel_status=23)
                    result = self.run_ensure_space(conditional)
                    self.assertEqual(result.returncode, 23, result.stderr)
                    self.assertNotIn("expected kernel", result.stderr)
                    self.assertEqual(len(self.ensure_space_calls()), 2)

    def test_ensure_space_rejects_wrong_kernel_in_conditional_and_no_errexit_callers(self):
        for conditional in (False, True):
            with self.subTest(conditional=conditional):
                self.ensure_space_fixture(True, True, release="6.18.25")
                result = self.run_ensure_space(conditional)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("expected kernel 7.2.11, got 6.18.25", result.stderr)
                self.assertEqual(len(self.ensure_space_calls()), 2)

    def test_optional_kernel_gate_rejects_a_reverted_image(self):
        self.env.update(SMOKE_SPACE="fixture", SMOKE_CONFIG_ROOT=str(self.root / "home"), XDG_CONFIG_HOME=str(self.root / "config"),
                        SMOKE_EXPECT_KERNEL_RELEASE="7.2.11")
        manifest = self.root / "home/spaces/fixture/space.yaml"
        manifest.parent.mkdir(parents=True)
        manifest.write_text("fixture\n")
        for release in ("7.2.11", "6.18.25", ""):
            with self.subTest(release=release):
                executable(Path(self.env["YOLOCTL"]), f'''#!/bin/sh
case "$*" in
  "space start fixture") exit 0 ;;
  "space ssh fixture -- uname -r") printf '%s\\n' '{release}' ;;
  *) exit 37 ;;
esac
''')
                result = self.run_common("smoke_ensure_space")
                if release == "7.2.11":
                    self.assertEqual(result.returncode, 0, result.stderr)
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("expected kernel 7.2.11", result.stderr)
        # Normal smoke runs need no experiment and make no uname request.
        del self.env["SMOKE_EXPECT_KERNEL_RELEASE"]
        executable(Path(self.env["YOLOCTL"]), '#!/bin/sh\n[ "$*" = "space start fixture" ]\n')
        result = self.run_common("smoke_ensure_space")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_https_reachability_accepts_unauthenticated_provider_responses(self):
        # A real unauthenticated api.openai.com root request returned 421.
        # Redirects/rate limits also do not establish a policy denial.
        for status in ("200", "204", "307", "308", "401", "403", "421", "429"):
            with self.subTest(status=status):
                result = self.run_common("smoke_check_https_status", "api.openai.com", status)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("api.openai.com: HTTP " + status, result.stdout)

    def test_https_reachability_reports_upstream_proxy_and_invalid_results(self):
        for status in ("", "199", "407", "500", "502", "503", "200\n502", "invalid"):
            with self.subTest(status=status):
                result = self.run_common("smoke_check_https_status", "api.openai.com", status)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("api.openai.com", result.stderr)
                self.assertIn(status, result.stderr)

    def test_https_requires_allowance_for_each_host_binary_and_policy(self):
        allowed = ("OCSF NET:OPEN [INFO] ALLOWED /usr/bin/node(156) -> "
                   "api.openai.com:443 [policy:pi engine:opa]")
        result = self.run_common("smoke_require_allowed_egress", allowed,
                                 "pi", "/usr/bin/node", "api.openai.com")
        self.assertEqual(result.returncode, 0, result.stderr)
        for logs in ("", allowed.replace("NET:OPEN", "NET:DENY"),
                     allowed.replace("ALLOWED", "DENIED"),
                     allowed.replace("/usr/bin/node", "/usr/bin/curl"),
                     allowed.replace("policy:pi", "policy:other"),
                     allowed.replace("api.openai.com:443", "other.example:443")):
            with self.subTest(logs=logs):
                result = self.run_common("smoke_require_allowed_egress", logs,
                                         "pi", "/usr/bin/node", "api.openai.com")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("missing attributed pi egress allowance", result.stderr)
        result = self.run_common("smoke_require_allowed_egress", allowed,
                                 "pi", "/usr/bin/node", "api.openai.com", "auth.openai.com")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("auth.openai.com", result.stderr)

    def matrix_fixture(self):
        shutil.copy(SCRIPTS / "vm-backends.sh", self.scripts)
        (self.scripts / "common.sh").write_text('''
smoke_require_backend() { :; }
smoke_info() { printf '%s\\n' "$*"; }
smoke_fail() { printf '%s\\n' "$*" >&2; exit 1; }
''')
        for script in ["live-vm.sh", "space-archive.sh"]:
            executable(self.scripts / script, f"#!{sys.executable}\n" + '''
import json, os
from pathlib import Path
with open(os.environ['AUDIT'], 'a') as f:
    f.write(json.dumps({k: v for k, v in os.environ.items()
                        if k.startswith('SMOKE_') or k == 'YOLOCTL'}) + '\\n')
root = Path(os.environ['SMOKE_ROOT'])
root.mkdir()
(root / 'retained.log').write_text('diagnostic bytes')
print('fixture output')
if os.environ.get('FAIL_PHASE') == root.name:
    raise SystemExit(23)
''')
        self.env["AUDIT"] = str(self.root / "audit.jsonl")
        # Deliberately inject caller state; the runner must not use it.
        self.env.update(SMOKE_ROOT="/unrelated", SMOKE_WORKDIR="/unrelated",
                        SMOKE_SPACE="unrelated", SMOKE_PRESERVE_SPACE="1",
                        SMOKE_CLEANUP_FAILED="1", SMOKE_OWNS_ROOT="1")

    def test_matrix_uses_separate_fixtures_and_expected_backend_pairs(self):
        self.matrix_fixture()
        result = self.run_script("vm-backends.sh")
        self.assertEqual(result.returncode, 0, result.stderr)
        records = [json.loads(line) for line in Path(self.env["AUDIT"]).read_text().splitlines()]
        self.assertEqual([(r["SMOKE_BACKEND"], r["SMOKE_IMPORT_BACKEND"]) for r in records],
                         [("firecracker", "firecracker"), ("lima", "lima"),
                          ("lima", "firecracker"), ("firecracker", "lima")])
        roots = [Path(r["SMOKE_ROOT"]) for r in records]
        self.assertEqual(len(set(roots)), 4)
        phases = ["firecracker", "lima", "lima-to-firecracker", "firecracker-to-lima"]
        for r, root, phase in zip(records, roots, phases):
            self.assertEqual(root.parent.parent, Path("/tmp"))
            self.assertEqual(r["SMOKE_WORKDIR"], str(root / "workdir"))
            self.assertEqual(r["SMOKE_SPACE"], "smoke-live-vm")
            self.assertEqual(r["SMOKE_PRESERVE_SPACE"], "0")
            self.assertEqual(r["SMOKE_KEEP_ON_FAILURE"], "1")
            self.assertNotIn("SMOKE_CLEANUP_FAILED", r)
            self.assertNotIn("SMOKE_OWNS_ROOT", r)
            self.assertEqual(r["YOLOCTL"], self.env["YOLOCTL"])
            self.assertEqual((root.parent / (phase + ".log")).read_text(), "fixture output\n")
        self.assertEqual((roots[0].parent / "summary.tsv").read_text().count("\tPASS\t0\n"), 4)

    def test_matrix_stops_on_failure_and_keeps_diagnostics(self):
        self.matrix_fixture()
        self.env["FAIL_PHASE"] = "li"
        result = self.run_script("vm-backends.sh")
        self.assertEqual(result.returncode, 23, result.stderr)
        records = [json.loads(line) for line in Path(self.env["AUDIT"]).read_text().splitlines()]
        self.assertEqual(len(records), 2)
        root = Path(records[-1]["SMOKE_ROOT"])
        self.assertEqual((root / "retained.log").read_text(), "diagnostic bytes")
        self.assertIn("lima\tFAIL\t23\n", (root.parent / "summary.tsv").read_text())
        self.assertNotIn("PASS: both", result.stdout)

    def test_matrix_socket_paths_fit_even_with_long_tmpdir_and_restored_names(self):
        self.matrix_fixture()
        long_tmp = self.root / ("long-temp-root-" * 10)
        long_tmp.mkdir()
        self.env["TMPDIR"] = str(long_tmp)
        result = self.run_script("vm-backends.sh")
        self.assertEqual(result.returncode, 0, result.stderr)
        records = [json.loads(line) for line in Path(self.env["AUDIT"]).read_text().splitlines()]
        for r in records:
            for suffix in ["", "-b", "-restored"]:
                path = (Path(r["SMOKE_ROOT"]) / "home/lima" /
                        ("yolo-" + r["SMOKE_SPACE"] + suffix) / "ssh.sock.1234567890123456")
                # Includes Lima's reserved temporary suffix. Darwin's limit is
                # stricter than Linux's 108 bytes; both must fit, including NUL.
                self.assertLess(len(os.fsencode(path)), 104, str(path))
                path.parent.mkdir(parents=True, exist_ok=True)
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
                    sock.bind(str(path))

    def test_archives_only_does_not_repeat_full_gates_or_claim_their_success(self):
        self.matrix_fixture()
        result = self.run_script("vm-backends.sh", "--archives-only")
        self.assertEqual(result.returncode, 0, result.stderr)
        records = [json.loads(line) for line in Path(self.env["AUDIT"]).read_text().splitlines()]
        self.assertEqual([(r["SMOKE_BACKEND"], r["SMOKE_IMPORT_BACKEND"]) for r in records],
                         [("lima", "firecracker"), ("firecracker", "lima")])
        summary = (Path(records[0]["SMOKE_ROOT"]).parent / "summary.tsv").read_text()
        self.assertEqual(summary, "phase\tresult\texit_code\n"
                         "lima-to-firecracker\tPASS\t0\nfirecracker-to-lima\tPASS\t0\n")
        self.assertIn("PASS: both cross-backend archive directions", result.stdout)
        self.assertNotIn("PASS: both complete live gates", result.stdout)

    def test_lima_interruption_explicitly_selects_lima(self):
        for name in ["common.sh", "space-lifecycle.sh"]:
            shutil.copy(SCRIPTS / name, self.scripts)
        stub_bin = self.root / "bin"
        stub_bin.mkdir()
        for name in ["limactl", "ssh"]:
            executable(stub_bin / name, "#!/bin/sh\nexit 0\n")
        self.env["PATH"] = str(stub_bin) + os.pathsep + self.env["PATH"]
        self.env.update(SMOKE_BACKEND="lima", SMOKE_ROOT=str(self.root / "isolated"))
        executable(Path(self.env["YOLOCTL"]), f"#!{sys.executable}\n" + '''
import json, os, subprocess, sys
from pathlib import Path
a = sys.argv[1:]
if a[:2] == ['space', 'create']:
    root = Path(os.environ['SMOKE_ROOT'])
    (root / 'create.json').write_text(json.dumps(a))
    if '--backend' not in a or a[a.index('--backend') + 1] != 'lima':
        print('wrong backend: defaulted to Firecracker', file=sys.stderr)
        raise SystemExit(12)
    manifest = Path(os.environ['YOLOCTL_HOME']) / 'spaces' / a[2] / 'space.yaml'
    manifest.parent.mkdir(parents=True)
    manifest.write_text('initializing: true\\n')
    raise SystemExit(subprocess.run(['limactl', 'start', a[2]]).returncode)
if a[:2] == ['space', 'start']:
    print('reached explicit recovery', file=sys.stderr)
    raise SystemExit(66)
''')
        result = self.run_script("space-lifecycle.sh")
        self.assertEqual(result.returncode, 66, result.stdout + result.stderr)
        self.assertIn("reached explicit recovery", result.stderr)
        args = json.loads((Path(self.env["SMOKE_ROOT"]) / "create.json").read_text())
        self.assertEqual(args[args.index("--backend") + 1], "lima")

    def test_live_gate_cleanup_failure_is_not_a_pass(self):
        shutil.copy(SCRIPTS / "live-vm.sh", self.scripts)
        (self.scripts / "common.sh").write_text('''
smoke_init() { SMOKE_ROOT=/unused; }
smoke_info() { printf '%s\\n' "$*"; }
smoke_destroy_space() { return 1; }
smoke_remove_owned_root() { :; }
''')
        for path in SCRIPTS.glob("*.sh"):
            if path.name not in ("common.sh", "live-vm.sh"):
                executable(self.scripts / path.name, "#!/bin/sh\nexit 0\n")
        result = self.run_script("live-vm.sh")
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("PASS:", result.stdout)

    def test_archive_failure_keeps_fixtures_when_requested(self):
        shutil.copy(SCRIPTS / "space-archive.sh", self.scripts)
        (self.scripts / "common.sh").write_text('''
smoke_init() { SMOKE_BACKEND=lima; SMOKE_ROOT=/unused; SMOKE_SPACE=fixture; }
smoke_require_backend() { :; }
smoke_ensure_space() { return 19; }
''')
        self.env["SMOKE_KEEP_ON_FAILURE"] = "1"
        self.env["AUDIT"] = str(self.root / "destroy-called")
        executable(Path(self.env["YOLOCTL"]), '#!/bin/sh\nprintf called >"$AUDIT"\n')
        result = self.run_script("space-archive.sh")
        self.assertEqual(result.returncode, 19, result.stderr)
        self.assertIn("retained", result.stderr)
        self.assertFalse(Path(self.env["AUDIT"]).exists())


if __name__ == "__main__":
    unittest.main()
