#!/usr/bin/env bash
# Isolated Python development environment and confined PyPI smoke.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

BOX=smoke-pip
cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
		if [[ "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 && "${SMOKE_PASSED:-0}" != 1 ]]; then
			printf 'Retaining failed fixture at %s\n' "$SMOKE_ROOT" >&2
			return
		fi
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

smoke_ensure_space
smoke_destroy_box "$BOX"
smoke_info "pip: Python environment with confined PyPI access"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent none \
	--features pip --workdir-mode ephemeral --no-attach --no-autoupdate
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }

smoke_info "pip: interpreter, aliases, user installs and cache ownership"
in_box sh -se <<'SH'
test "$(id -u)" != 0
python --version
python3 --version
test "$(readlink -f "$(command -v python)")" = /usr/bin/python3.13
test "$(readlink -f "$(command -v python3)")" = /usr/bin/python3.13
test "$(pip --version)" = "$(python3 -m pip --version)"
test "$(pip3 --version)" = "$(python3 -m pip --version)"
test "$(pip cache dir)" = /home/sandbox/.cache/pip
test "$(python3 -m site --user-base)" = /home/sandbox/.local

# Fixture versions stay fixed; the feature reuses the base's pip version.
pip install --user --disable-pip-version-check --retries 0 --timeout 15 wheel==0.45.1
test "$(command -v wheel)" = /home/sandbox/.local/bin/wheel
test "$(wheel version)" = 'wheel 0.45.1'
python3 - <<'PY'
import os
from pathlib import Path
for name in ('.local', '.local/bin', '.cache', '.cache/pip'):
    path = Path('/home/sandbox') / name
    assert os.access(path, os.W_OK), path
    assert (path.stat().st_uid, path.stat().st_gid) == (os.getuid(), os.getgid()), path
assert any(p.is_file() for p in Path('/home/sandbox/.cache/pip').rglob('*')), 'pip cache is empty'
PY
SH

smoke_info "pip: venv, locked requirements, offline install and project tests"
in_box sh -se <<'SH'
cd /workdir
python3 -m venv .venv
test "$(readlink -f .venv/bin/python)" = /usr/bin/python3.13
test "$(.venv/bin/pip --version)" = "$(.venv/bin/python -m pip --version)"
.venv/bin/python - <<'PY'
import importlib.util
import sys
assert sys.prefix != sys.base_prefix, 'venv is not active'
assert importlib.util.find_spec('wheel') is None, 'venv inherited user site-packages'
PY

# This request is made by the venv's interpreter, not by the global pip alias.
.venv/bin/python -m pip download --disable-pip-version-check --retries 0 --timeout 15 \
    --only-binary=:all: --no-deps --dest wheelhouse packaging==25.0
python3 - <<'PY'
from hashlib import sha256
from pathlib import Path
wheel = next(Path('wheelhouse').glob('packaging-25.0-*.whl'))
Path('requirements.txt').write_text('packaging==25.0 --hash=sha256:' + sha256(wheel.read_bytes()).hexdigest() + '\n')
PY
cp requirements.txt expected-requirements.txt
.venv/bin/python -m pip install --disable-pip-version-check --no-index --find-links=wheelhouse \
    --require-hashes -r requirements.txt
cmp requirements.txt expected-requirements.txt
.venv/bin/python -c 'from packaging.version import Version; assert Version("1.2.3") < Version("2.0")'
.venv/bin/python -m pip check

# Editable installation exercises PEP 517 build isolation and its PyPI access.
cat > pyproject.toml <<'TOML'
[build-system]
requires = ["setuptools==80.9.0"]
build-backend = "setuptools.build_meta"

[project]
name = "yoloctl-pip-smoke"
version = "0.0.1"
dependencies = ["packaging==25.0"]

[project.scripts]
yoloctl-pip-smoke = "smoke_module:main"

[tool.setuptools]
py-modules = ["smoke_module"]
TOML
cat > smoke_module.py <<'PY'
def answer():
    return 41

def main():
    print(answer())
PY
.venv/bin/python -m pip install --disable-pip-version-check --retries 0 --timeout 15 -e .
test "$(.venv/bin/yoloctl-pip-smoke)" = 41
python3 - <<'PY'
from pathlib import Path
path = Path('smoke_module.py')
path.write_text(path.read_text().replace('return 41', 'return 42  # edited'))
Path('test_smoke.py').write_text('import unittest\nimport smoke_module\nclass Smoke(unittest.TestCase):\n    def test_answer(self):\n        self.assertEqual(smoke_module.answer(), 42)\n')
PY
# Run outside the source directory to ensure the editable install is used.
cd /tmp
test "$(/workdir/.venv/bin/yoloctl-pip-smoke)" = 42
cd /workdir
.venv/bin/python -m unittest discover -v
.venv/bin/python -m pip check
SH

smoke_info "pip: activated and fresh exec environments remain independent"
in_box sh -se <<'SH'
cd /workdir
. .venv/bin/activate
test "$(command -v pip)" = /workdir/.venv/bin/pip
test "$(pip --version)" = "$(python -m pip --version)"
test "$(python -c 'import sys; print(sys.prefix)')" = /workdir/.venv
SH
in_box sh -se <<'SH'
test "$(command -v pip)" = /usr/local/bin/pip
python3 -c 'import sys; assert sys.prefix == sys.base_prefix'
test "$(wheel version)" = 'wheel 0.45.1'
SH

smoke_info "pip: curl cannot borrow the PyPI grant"
if in_box curl -fsS --max-time 5 -o /dev/null https://pypi.org/simple/packaging/ 2>/dev/null; then
	smoke_fail "curl borrowed PyPI egress"
fi
logs="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1)"
grep -q "binary '/usr/bin/curl' not allowed in policy 'pypi'" <<<"$logs" ||
	smoke_fail "missing attributed curl denial"

diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE" 2>&1)"
[[ "$diff_output" == "PLAN: no changes" ]] || {
	printf '%s\n' "$diff_output" >&2
	smoke_fail "pip environment has unexpected reconcile drift"
}
SMOKE_PASSED=1
smoke_info "PASS: pip/venv, user commands, hashed requirements, editable projects and confined PyPI access"
