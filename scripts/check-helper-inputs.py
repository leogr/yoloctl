#!/usr/bin/env python3
"""Explicit maintenance check of compiled helper inputs, never an installer.

Uses Go's real package selection to audit the hand-maintained embedded closure.
With --mutations, plain go build (without make/generate) must observe an added
helper production file and preserve identity across an unrelated CLI edit.
All mutated files live in one new private temporary source copy.
"""
import argparse
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def run(root, *args, env=None):
    selected = dict(os.environ)
    selected.update(GOFLAGS='', GOWORK='off', GOENV='off', GOTOOLCHAIN='local', CGO_ENABLED='0')
    for key in ('GOOS', 'GOARCH', 'GOEXPERIMENT', 'GOAMD64', 'GOARM64', 'GOFIPS140', 'GODEBUG'):
        selected.pop(key, None)
    if env:
        selected.update(env)
    result = subprocess.run(args, cwd=root, env=selected, capture_output=True, text=True, timeout=300)
    if result.returncode:
        raise ValueError(f'{args[0]} helper input check failed ({result.returncode}): {result.stderr[-4000:]}')
    return result.stdout


def json_stream(text):
    decoder = json.JSONDecoder()
    while text.strip():
        text = text.lstrip()
        value, size = decoder.raw_decode(text)
        yield value
        text = text[size:]


def packages(root, arch):
    return list(json_stream(run(root, 'go', 'list', '-deps', '-json', './cmd/yoloctl-openshell-helper', env={'GOOS': 'linux', 'GOARCH': arch})))


def identity(root):
    return json.loads(run(root, 'go', 'run', '-buildvcs=false', './scripts/openshell-helper-inputs.go'))


def check(root, mutations=False):
    recipe = json.loads((root / 'scripts/openshell-helper-recipe.json').read_bytes())
    actual = identity(root)
    closure = {}
    local_dirs = set()  # every local package directory in the actual closure
    for arch in recipe['architectures']:
        selected = packages(root, arch)
        modules = sorted({p['Module']['Path'] for p in selected if not p.get('Standard') and p.get('Module', {}).get('Path') != 'github.com/leogr/yoloctl'})
        if modules != recipe['modules']:
            raise ValueError(f'{arch}: actual helper dependency closure differs: {modules}')
        local = []
        for p in selected:
            if p.get('Module', {}).get('Path') == 'github.com/leogr/yoloctl':
                local_dirs.add(Path(p['Dir']).resolve().relative_to(root))
            if p.get('Module', {}).get('Path') != 'github.com/leogr/yoloctl':
                continue
            directory = Path(p['Dir']).resolve()
            if not directory.is_relative_to(root):
                raise ValueError('helper local dependency is outside the selected checkout')
            relative = directory.relative_to(root)
            # The runtime source walker independently follows all imports and
            # refuses any internal package absent from the embedding patterns.
            names = set(p.get('GoFiles', []) + p.get('CgoFiles', []) + p.get('SFiles', []) + p.get('HFiles', []) + p.get('SysoFiles', []))
            local.extend(str(relative / name) for name in names)
        closure[arch] = sorted(local)
    result = {'inputs': actual, 'actual_local_build_inputs': closure, 'mutations': 'NOT RUN'}
    if not mutations:
        return result
    with tempfile.TemporaryDirectory(prefix='yoloctl-helper-inputs-') as temporary:
        target = Path(temporary) / 'source'
        target.mkdir()
        # Explicit maintained paths only; no ignored worktrees, caches, .git,
        # account state or runtime records are copied or scanned.
        for name in ('go.mod', 'go.sum'):
            shutil.copyfile(root / name, target / name)
        for path in root.glob('*.go'):
            if not path.name.endswith('_test.go'):
                shutil.copyfile(path, target / path.name)
        # Exactly the local packages Go selected for the helper (both
        # architectures), so a new helper import is copied automatically.
        for relative in sorted(local_dirs):
            if relative == Path('.'):
                continue  # root package files are copied above
            shutil.copytree(root / relative, target / relative, symlinks=True, dirs_exist_ok=True)
        # Preserve unrelated local module replacements required by go.mod's
        # graph, without folding their sources into the helper identity.
        for name in ('third_party/ultraviolet', 'third_party/sftp-v2'):
            shutil.copytree(root / name, target / name, symlinks=True)
        (target / 'scripts').mkdir()
        for name in ('openshell-helper-recipe.json', 'openshell-helper-inputs.go'):
            shutil.copyfile(root / 'scripts' / name, target / 'scripts' / name)
        executable = Path(temporary) / 'input-inspector'

        def compile_identity():
            run(target, 'go', 'build', '-buildvcs=false', '-o', str(executable), './scripts/openshell-helper-inputs.go')
            return json.loads(run(target, str(executable)))

        baseline = compile_identity()
        if baseline != actual:
            raise ValueError('plain build source copy differs from selected inputs')
        added = target / 'internal/openshell/qualification_added_linux.go'
        added.write_text('package openshell\nconst qualificationInputMutation = 1\n')
        changed = compile_identity()
        if changed == baseline:
            raise ValueError('plain go build missed a newly added helper production source')
        added.write_text('package openshell\nconst qualificationInputMutation = 2\n')
        edited = compile_identity()
        if edited == changed:
            raise ValueError('plain go build missed an edited helper production source')
        # The cold-operator reader imports this production package. Added
        # files must enter the actual compiled closure without generating a
        # snapshot, just as they do in the runtime package itself.
        operator_added = target / 'internal/operator/qualification_operator_added_linux.go'
        operator_added.write_text('package operator\nconst qualificationOperatorInputMutation = 1\n')
        operator_changed = compile_identity()
        if operator_changed == edited:
            raise ValueError('plain go build missed a newly added operator production source')
        operator_added.write_text('package operator\nconst qualificationOperatorInputMutation = 2\n')
        operator_edited = compile_identity()
        if operator_edited == operator_changed:
            raise ValueError('plain go build missed an edited operator production source')
        outside = target / 'cmd/yoloctl'
        outside.mkdir()
        (outside / 'qualification_outside.go').write_text('package main\nconst outsideHelperClosure = 1\n')
        if compile_identity() != operator_edited:
            raise ValueError('outside-closure CLI edit changed helper source identity')
        # The production loader and its final manifest/helper hashes must not
        # become inputs to the helper that the manifest describes. Copy actual
        # outside source into this private checkout before mutating selection.
        for name in ('assets/native_openshell.go', 'internal/openshellrelease/release.go'):
            destination = target / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(root / name, destination)
        if compile_identity() != operator_edited:
            raise ValueError('outside release source entered the helper identity')
        selector = target / 'assets/native_openshell.go'
        selection = selector.read_text()
        controls = []
        for name in ('nativeOpenShellManifest', 'nativeOpenShellManifestSHA256', 'nativeOpenShellRego'):
            # Source changes to this selection shape must deliberately update
            # the audit; silently missing a new embed/selector is not proof.
            pattern = r'(' + name + r'\s*=\s*)("(?:\\.|[^"\\])*"|`[^`]*`)'
            changed, count = re.subn(pattern, lambda match: match[1] + '"outside-closure-audit"', selection)
            if count != 1:
                raise ValueError(f'cannot audit actual compiled selection {name}')
            selector.write_text(changed)
            if compile_identity() != operator_edited:
                raise ValueError(f'outside {name} edit changed helper identity')
            controls.append(name)
        loader = target / 'internal/openshellrelease/release.go'
        loader.write_text(loader.read_text() + '\nconst qualificationOutsideLoaderAudit = 1\n')
        if compile_identity() != operator_edited:
            raise ValueError('outside production-loader edit changed helper identity')
        controls.append('actual production loader source')
        # These are prospective outside data fixtures, not selected shipping
        # assets. Final published bytes still need the same audit at release.
        for name, contents in (('qualification-audit.json', '{"audit":"outside"}'),
                               ('qualification-audit.rego', 'package outside_audit\n')):
            (target / 'assets' / name).write_text(contents)
            if compile_identity() != operator_edited:
                raise ValueError(f'outside {name} data entered helper identity')
            controls.append(name + ' (unselected data fixture)')
        result['outside_release_controls'] = controls
        result['mutations'] = 'PASS: runtime/operator added/edited production source bound; outside CLI/loader/manifest/hash/Rego edits unchanged; no make/generate'
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mutations', action='store_true')
    parser.add_argument('--root', type=Path, default=ROOT)
    args = parser.parse_args()
    try:
        result = check(args.root.resolve(strict=True), args.mutations)
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        parser.exit(1, f'OpenShell helper input check FAILED: {error}\n')
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
