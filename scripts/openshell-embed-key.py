#!/usr/bin/env python3
"""Print the key that decides whether `make native-embed` must re-run native-qualify.

It joins the OpenShell cache key (pins, patches, build script) with a hash of everything
native-qualify builds the helpers from: the repository files of the helper's real Go
dependency closure for linux/amd64 and linux/arm64 (from `go list -deps`, so new imports are
covered without a hand-kept list), go.mod/go.sum, the Go version and the qualify scripts.
Kept apart from openshell-build.py so editing it never changes the OpenShell cache key.
"""
import hashlib
import importlib.util
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
HELPER = './cmd/yoloctl-openshell-helper'
FIXED = ('go.mod', 'go.sum', 'scripts/native-artifacts.go', 'scripts/openshell-helper-inputs.go',
         'scripts/openshell-helper-recipe.json', 'scripts/openshell-embed-key.py')


def closure_files(goarch):
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=goarch, GOFLAGS='')
    out = subprocess.run(['go', 'list', '-deps', '-json=Dir,Standard,GoFiles,EmbedFiles', HELPER], cwd=ROOT, env=env,
                         check=True, stdout=subprocess.PIPE, text=True).stdout
    files = set()
    for package in json.loads('[' + out.replace('}\n{', '},\n{') + ']'):
        directory = Path(package['Dir'])
        if package.get('Standard') or ROOT not in directory.parents and directory != ROOT:
            continue  # standard library and module dependencies are covered by the Go version and go.sum
        for name in package.get('GoFiles', []) + package.get('EmbedFiles', []):
            files.add((directory / name).relative_to(ROOT).as_posix())
    return files


def helper_hash():
    files = set(FIXED)
    for goarch in ('amd64', 'arm64'):
        files |= closure_files(goarch)
    h = hashlib.sha256(subprocess.run(['go', 'version'], check=True, stdout=subprocess.PIPE).stdout)
    for name in sorted(files):
        h.update(name.encode() + b'\0' + (ROOT / name).read_bytes() + b'\0')
    return h.hexdigest()[:24]


def main():
    spec = importlib.util.spec_from_file_location('openshell_build', ROOT / 'scripts' / 'openshell-build.py')
    build = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(build)
    print('%s-%s' % (build.cache_key(build.load_pins()), helper_hash()))
    return 0


if __name__ == '__main__':
    sys.exit(main())
