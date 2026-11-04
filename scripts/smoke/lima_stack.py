#!/usr/bin/env python3
"""Lima stack acceptance: owned host fixture, bounded children, retained evidence."""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time

REPO = Path(__file__).resolve().parents[2]
TEMP_ROOT = Path('/private/tmp')
VERSIONS = {'nerdctl': '2.3.5', 'containerd': '2.3.3', 'runc': '1.5.1',
            'buildctl': '0.31.2', 'buildkitd': '0.31.2'}
PINS = {'x86_64': ('amd64', 'b697295c623639734aaab737523c808fd3cc8d3046039fd94fff1744e4c317aa'),
        'aarch64': ('arm64', '6e4b687f1d138e750a3c8372abc0f81d3d7490b6359c48c0562fc7dfe98859b2')}
# Project only non-secret fields, never guest environment/account contents.
LIMA_VIEW = ('{{""}}{"name":{{json .Name}},"status":{{json .Status}},"arch":{{json .Arch}},'
             '"containerd":{{json .Config.Containerd}},"mounts":{{json .Config.Mounts}},'
             '"disks":{{json .Config.AdditionalDisks}}}' + '{{"\\n"}}')
INSPECT = '''import json,sys
v=json.load(sys.stdin)
assert isinstance(v,list) and len(v)==1
v=v[0]
print(json.dumps({"id":v["Id"],"privileged":v.get("HostConfig",{}).get("Privileged")}))'''
PTY = '''import errno,os
try:
 m,s=os.openpty()
except OSError as e:
 if e.errno not in (errno.EACCES,errno.EPERM): raise
 print("LIMA_STACK_PTY_DENIED:"+str(e.errno))
 raise SystemExit(73)
print("LIMA_STACK_PTY_ALLOWED:"+os.ttyname(s))
os.close(m); os.close(s)'''
FEATURE = '''name: lima-stack-probe
install:
  kind: dockerfile
  dockerfile: |
    FROM busybox:1.37.0 AS stack-source
    RUN printf 'lima-stack-marker\\n' > /stack-marker
    FROM yoloctl/agentbox:ubuntu-24.04
    COPY --from=stack-source /stack-marker /usr/local/share/lima-stack-marker
    RUN test "$(cat /usr/local/share/lima-stack-marker)" = lima-stack-marker
'''

def file_digest(path):
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for chunk in iter(lambda: source.read(8 * 1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


class Failure(Exception):
    def __init__(self, message, code=1):
        super().__init__(message)
        self.code = code


def require(ok, message):
    if not ok:
        raise Failure(message)


def canonical(path):
    p = Path(path).expanduser()
    require(p.is_absolute(), 'absolute path required: ' + str(p))
    # Reject redirection, including dangling links, before any owned write.
    for part in (p, *p.parents):
        require(not part.is_symlink(), 'symlink path refused: ' + str(part))
    return p.resolve()


def overlaps(a, b):
    return a == b or a in b.parents or b in a.parents


def separated(path, protected):
    for other in protected:
        require(not overlaps(path, other.resolve()), f'overlap refused: {path} and {other}')


def scalar(text, key):
    # Only generated top-level identity scalars, never a general YAML decoder.
    values = re.findall(r'^' + re.escape(key) + r':\s*([^\n]+)$', text, re.M)
    require(len(values) == 1, 'missing/ambiguous bootstrap ' + key)
    value = values[0].strip()
    if value.startswith('"'):
        value = json.loads(value)
    elif value.startswith("'") and value.endswith("'"):
        value = value[1:-1].replace("''", "'")
    require(isinstance(value, str) and re.fullmatch(r'[a-zA-Z0-9_.-]+', value) is not None, 'invalid bootstrap ' + key)
    return value


def go_release(version):
    match = re.fullmatch(r'(?:go)?([1-9][0-9]*)\.([0-9]+)(?:\.([0-9]+))?', version)
    require(match, 'unsupported stable Go version: ' + version)
    return tuple(int(part or '0') for part in match.groups())


def module_toolchain():
    text = (REPO / 'go.mod').read_text()
    minimum = re.findall(r'^go[ \t]+(\S+)[ \t]*(?://[^\n]*)?$', text, re.M)
    toolchains = re.findall(r'^toolchain[ \t]+(\S+)[ \t]*(?://[^\n]*)?$', text, re.M)
    require(len(minimum) == 1 and len(toolchains) <= 1, 'go.mod must declare one Go version and at most one toolchain')
    required = minimum[0]
    version = go_release(required)
    selected = toolchains[0] if toolchains else 'go' + required
    chosen = go_release(selected)
    require(selected.startswith('go') and chosen >= version, 'go.mod toolchain is older than its required Go version')
    # Since 1.21, a language version (1.N) names no downloadable release.
    if chosen[:2] >= (1, 21) and selected.count('.') == 1:
        selected += '.0'
    return required, selected


def box_inventory(rows):
    require(isinstance(rows, list), 'UNKNOWN: box inventory unavailable')
    result = []
    for row in rows:
        require(isinstance(row, dict) and isinstance(row.get('name'), str) and isinstance(row.get('state'), str),
                'UNKNOWN: invalid box inventory row')
        dims = row.get('dimensions')
        require(isinstance(dims, dict) and dims.get('runtime') in ('running', 'stopped', 'missing'),
                'UNKNOWN: box runtime evidence unavailable for ' + str(row.get('name')))
        result.append({'name': row['name'], 'state': row['state'],
                       'dimensions': {k: dims[k] for k in ('runtime', 'session', 'drift') if k in dims}})
    return result


def host_preflight():
    require(platform.system() == 'Darwin', 'requires a macOS host; never run in a team box')
    require(not any(os.environ.get(k) for k in ('YOLOCTL_CONTROLLER_CONTEXT', 'YOLOCTL_BOX', 'YOLOCTL_SPACE'))
            and not Path('/run/yoloctl/operator').exists()
            and not Path('/etc/yoloctl/controller.json').exists(), 'in-box/controller context refused')
    for tool in ('limactl', 'ssh', 'python3', 'git', 'make', 'go'):
        require(shutil.which(tool), 'missing prerequisite: ' + tool)


def environment(home):
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(('SMOKE_', 'YOLOCTL_', 'GO', 'LIMA_')) and k not in ('YOLOCTL', 'BASH_ENV', 'ENV', 'MAKEFLAGS', 'GNUMAKEFLAGS', 'MFLAGS', 'PYTHONPATH', 'PYTHONHOME')}
    env.update(YOLOCTL_HOME=str(home), XDG_CONFIG_HOME=str(home.parent / 'config'),
               XDG_CACHE_HOME=str(home.parent / 'cache'), LIMA_HOME=str(home / 'lima'), GIT_TERMINAL_PROMPT='0', GOENV='off',
               SSH_ASKPASS_REQUIRE='never')
    return env


class Runner:
    def __init__(self):
        self.report = {'schema': 1, 'phases': [], 'result': 'FAIL', 'limits':
                       'Exercised Lima stack only; no native-byte preservation, amd64/Rosetta or historical-cause proof.'}
        self.output = None
        self.pending = []
        self.root = None
        self.created = False
        self.env = os.environ.copy()
        self.deadline = time.monotonic() + 3600

    def save(self):
        if self.output:
            (self.output / 'report.json').write_text(json.dumps(self.report, indent=2) + '\n')
            (self.output / 'phases.tsv').write_text('phase\tresult\texit_code\n' + ''.join(
                f'{p["phase"]}\t{p["result"]}\t{p["exit_code"]}\n' for p in self.report['phases']))

    def logs(self, name, out, err):
        if not self.output:
            self.pending.append((name, out, err))
        else:
            (self.output / (name + '.stdout')).write_bytes(out)
            (self.output / (name + '.stderr')).write_bytes(err)

    def group_alive(self, pgid):
        try:
            os.killpg(pgid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            # Darwin can deny the null signal for a zombie-only group. The
            # inventory must still distinguish zombies from live members.
            pass
        # An exited leader's children may be zombies awaiting their new parent.
        # They cannot write; inspect only PID/group/state, never process argv.
        snapshot = subprocess.run(['/bin/ps', '-axo', 'pid=,pgid=,stat='],
            stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=1)
        require(snapshot.returncode == 0, 'cannot inspect owned command group')
        for line in snapshot.stdout.splitlines():
            fields = line.split()
            require(len(fields) == 3 and fields[0].isdigit() and fields[1].isdigit(),
                    'invalid command-group process inventory')
            if int(fields[1]) == pgid and not fields[2].startswith('Z'):
                return True
        return False

    def settle_command(self, proc):
        # The group outlives its leader. Never gate cancellation on poll().
        try:
            for sig, grace in ((signal.SIGTERM, .2), (signal.SIGKILL, 2)):
                try:
                    os.killpg(proc.pid, sig)
                except ProcessLookupError:
                    break
                until = time.monotonic() + grace
                while self.group_alive(proc.pid) and time.monotonic() < until:
                    time.sleep(.02)
                if not self.group_alive(proc.pid):
                    break
            proc.wait(timeout=1)
            require(not self.group_alive(proc.pid), 'owned command group still active')
        except (Failure, OSError, subprocess.TimeoutExpired) as exc:
            return str(exc)
        return None

    def command(self, args, seconds=120, allow_failure=False):
        require(not self.report.get('unsettled_commands'), 'owned command cancellation is unverified')
        args = list(map(str, args))
        remaining = min(seconds, self.deadline - time.monotonic())
        require(remaining > 0, 'overall deadline exhausted')
        index = len(self.report.setdefault('commands', [])) + 1
        name = f'{index:03d}-{Path(args[0]).name}'
        record = {'argv': args, 'log': name, 'exit_code': None}
        self.report['commands'].append(record)
        # Separate spools avoid pipe backpressure and unbounded RAM capture.
        with contextlib.ExitStack() as stack:
            out = stack.enter_context(tempfile.TemporaryFile(dir=self.output)) if self.output else subprocess.PIPE
            err = stack.enter_context(tempfile.TemporaryFile(dir=self.output)) if self.output else subprocess.PIPE
            proc = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                    env=self.env, cwd=REPO, start_new_session=True)
            code, error, drained = None, None, False
            raw, errors = b'', b''
            until = time.monotonic() + remaining
            try:
                if self.output:
                    code = proc.wait(timeout=remaining)
                else:
                    # Also notice an unsuccessful leader while descendants hold
                    # its pipes. communicate() alone waits for those descendants.
                    while True:
                        left = until - time.monotonic()
                        if left <= 0:
                            raise subprocess.TimeoutExpired(args, remaining)
                        try:
                            raw, errors = proc.communicate(timeout=min(left, .05))
                            code, drained = proc.returncode, True
                            break
                        except subprocess.TimeoutExpired as exc:
                            raw, errors = exc.output or b'', exc.stderr or b''
                            code = proc.poll()
                            if code is not None and code != 0:
                                break
            except subprocess.TimeoutExpired:
                error = Failure('timeout: ' + shlex.join(args), 124)
            except BaseException as exc:
                error = exc
            finally:
                # A failing command must settle its whole transient group before
                # fixture teardown, even after its leader has exited. Successful VM
                # launches retain their intentional lifecycle-owned services.
                if error is not None or code != 0:
                    problem = self.settle_command(proc)
                    record['cancellation'] = {'pgid': proc.pid, 'settled': problem is None}
                    if problem:
                        self.report.setdefault('unsettled_commands', []).append(
                            {'pgid': proc.pid, 'log': name, 'reason': problem})
                if self.output:
                    out.seek(0); err.seek(0)
                    raw, errors = out.read(), err.read()
                elif not drained:
                    try:
                        raw, errors = proc.communicate(timeout=1)
                    except BaseException as exc:
                        if isinstance(exc, subprocess.TimeoutExpired):
                            raw, errors = exc.output or raw, exc.stderr or errors
                        else:
                            error = error or exc
                        self.report.setdefault('unsettled_commands', []).append(
                            {'pgid': proc.pid, 'log': name, 'reason': 'command pipe closure is unverified after cancellation'})
                        # No unbounded final drain if a descendant escaped the group.
                        proc.stdout.close(); proc.stderr.close()
                record['exit_code'] = proc.poll()
                safe_raw = raw
                if self.report.get('mode') == 'check' and args[1:3] in (['box', 'list'], ['space', 'list'], ['account', 'list']):
                    keys = ('name', 'state', 'dimensions') if args[1] == 'box' else (('name', 'status') if args[1] == 'space' else ('name',))
                    try:
                        rows = [{k: row[k] for k in keys} for row in json.loads(raw)]
                        if args[1] == 'box':
                            for row in rows:
                                row['dimensions'] = {k: row['dimensions'][k] for k in ('runtime', 'session', 'drift') if k in row['dimensions']}
                        safe_raw = json.dumps(rows).encode() + b'\n'
                    except (ValueError, KeyError, TypeError):
                        safe_raw = b'inventory stdout omitted: invalid JSON names/status data\n'
                self.logs(name, safe_raw, errors)
                self.save()
        if error is not None:
            raise error
        if code and not allow_failure:
            raise Failure('command failed: ' + shlex.join(args) + '; see ' + name, code if code > 0 else 128-code)
        require(not self.report.get('unsettled_commands'), 'owned command cancellation is unverified')
        return code, raw.decode(errors='replace'), errors.decode(errors='replace')

    def phase(self, name, action):
        entry = {'phase': name, 'result': 'FAIL', 'exit_code': 1}
        self.report['phases'].append(entry)
        self.save()
        outer_deadline = self.deadline
        self.deadline = min(outer_deadline, time.monotonic() + {'build': 1800, 'create': 1800, 'operator': 1800, 'images': 1800}.get(name, 300))
        try:
            action()
        except Failure as e:
            entry['exit_code'] = e.code
            raise
        else:
            entry.update(result='PASS', exit_code=0)
        finally:
            self.deadline = outer_deadline
            self.save()

    def cli(self, *args, seconds=120, allow_failure=False):
        return self.command([self.binary, *args], seconds, allow_failure)

    def guest(self, *args):
        return self.cli('space', 'ssh', self.space, '--root', '--', *args)[1].strip()

    def globals(self, absent):
        result = {}
        for name in ('default.yaml', 'override.yaml'):
            p = self.config / 'lima/_config' / name
            canonical(p)
            require(not p.exists() or p.stat().st_size <= 1024 * 1024, 'oversized global configuration')
            if absent:
                require(not p.exists(), 'isolated global configuration refused: ' + str(p))
            result[name] = hashlib.sha256(p.read_bytes()).hexdigest() if p.exists() else None
        return result

    def lima(self):
        self.report.setdefault('host_lima_versions', []).append(self.command(['limactl', '--version'])[1].strip())
        _, raw, _ = self.command(['limactl', 'list', '--format', LIMA_VIEW, 'yolo-' + self.space])
        info = json.loads(raw)
        self.report.setdefault('lima_observations', []).append(info)
        require(info['name'] == 'yolo-' + self.space, 'wrong Lima instance')
        return info

    def stack(self, pinned=True):
        self.globals(absent=bool(self.root))
        info = self.lima()
        require(info['status'] == 'Running', 'UNKNOWN: target is not already running')
        if pinned:
            arch, sha = PINS[info['arch']]
            archives = info['containerd']['archives']
            selected = next((a for a in archives if a.get('arch') in (None, info['arch'])), {})
            expected = {'arch': info['arch'], 'location': 'https://github.com/containerd/nerdctl/releases/download/v2.3.5/nerdctl-full-2.3.5-linux-' + arch + '.tar.gz', 'digest': 'sha256:' + sha}
            require(info['containerd']['system'] is True and info['containerd'].get('user', False) is False
                    and all(selected.get(k) == v for k, v in expected.items()), 'effective stack pin differs')
            if self.root:
                self.cache_evidence(selected)
        found = {}
        for tool, want in VERSIONS.items():
            raw = self.guest(tool, '--version')
            matches = re.findall(r'(?<![0-9.])v?([0-9]+\.[0-9]+\.[0-9]+)(?![0-9.])', raw)
            require(matches, 'missing version: ' + tool)
            found[tool] = matches[0]
            if pinned:
                require(found[tool] == want, 'unexpected ' + tool + ' version: ' + raw)
        self.guest('systemctl', 'is-active', 'containerd.service', 'buildkit.service', 'yoloctl-controller.service')
        show = json.loads(self.cli('space', 'show', self.space, '--json')[1])
        require(show['declarations_available'] is True, 'controller unavailable')
        self.report['versions'] = found
        disk = {k: show[k] for k in ('account_disk_id', 'account_disk_uuid')}
        if self.root and 'disk' in self.report:
            require(disk == self.report['disk'], 'native disk identity changed across recreation')
        self.report['disk'] = disk
        return show

    def identity(self):
        version = json.loads(self.cli('version', '--json')[1])
        # The query is read-only and authoritative for both build channels.
        config, cache = (canonical(version[key]) for key in ('config_root', 'cache_root'))
        require(config == Path(self.env['YOLOCTL_HOME']) and cache == config / 'cache',
                'binary roots differ from the explicitly selected YOLOCTL_HOME')
        self.config, self.cache = config, cache
        self.report.update(config_root=str(config), cache_root=str(cache))
        require(version.get('bundle'), 'missing bundle identity')
        self.report['bundle'] = version['bundle']
        self.report['binaries'] = {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(self.binary.parent.glob('yoloctl*')) if p.is_file()}

    def candidate(self):
        sha = self.command(['git', 'rev-parse', 'HEAD'])[1].strip()
        require(not self.command(['git', 'status', '--porcelain', '--untracked-files=no'])[1].strip(), 'tracked candidate is dirty')
        if 'sha' in self.report:
            require(sha == self.report['sha'], 'candidate HEAD changed')
        self.report['sha'] = sha
        self.report['branch'] = self.command(['git', 'rev-parse', '--abbrev-ref', 'HEAD'])[1].strip()

    def setup(self):
        self.candidate()
        home = Path.home()
        protected = [REPO, Path.cwd(), home / 'Library/Application Support', home / 'Library/Caches', home / '.config', home / '.cache', home / '.lima']
        protected += [Path(os.environ[k]).expanduser() for k in ('XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'LIMA_HOME', 'YOLOCTL_HOME', 'SMOKE_WORKDIR') if os.environ.get(k)]
        temp = canonical(TEMP_ROOT)
        require(all(p.resolve() != temp and p.resolve() not in temp.parents for p in protected), 'scratch parent overlaps protected root')
        self.root = canonical(tempfile.mkdtemp(prefix='yls-', dir=temp))
        self.root_identity = self.root.stat().st_ino
        self.output = canonical(tempfile.mkdtemp(prefix='ylr-', dir=TEMP_ROOT))
        separated(self.root, protected); separated(self.output, [*protected, self.root])
        self.config = self.root / 'home'
        self.cache = self.config / 'cache'
        self.space = 'stack-qa'
        self.binary = self.root / 'bin/yoloctl'
        for p in (self.config / 'lima', self.cache, self.root / 'workdir', self.config / 'features', self.root / 'go-cache', self.root / 'go-mod', self.root / 'tmp'):
            p.mkdir(parents=True, exist_ok=True)
        require(not list((self.config / 'lima').iterdir()), 'isolated Lima home is not empty')
        self.env = environment(self.config)
        self.env.update(GOCACHE=str(self.root / 'go-cache'), GOMODCACHE=str(self.root / 'go-mod'),
                        GOPATH=str(self.root / 'go'), TMPDIR=str(self.root / 'tmp'),
                        SMOKE_ROOT=str(self.root), SMOKE_SPACE=self.space, SMOKE_WORKDIR=str(self.root / 'workdir'),
                        SMOKE_BACKEND='lima', SMOKE_PRESERVE_SPACE='1', SMOKE_ASSERT_NO_LIMA_GLOBAL_CONFIG='1', YOLOCTL=str(self.binary))
        self.globals(absent=True)
        self.wrap_lima()
        self.report.update(fixture=str(self.root), config_root=str(self.config), lima_home=str(self.config / 'lima'), space=self.space)
        self.report['recovery'] = shlex.join(['env', 'PATH=' + self.env['PATH'], 'LIMA_STACK_CHILD_HOME=' + self.env['LIMA_STACK_CHILD_HOME'], 'LIMA_HOME=' + str(self.config / 'lima'), 'YOLOCTL_HOME=' + self.env['YOLOCTL_HOME'], 'XDG_CONFIG_HOME=' + self.env['XDG_CONFIG_HOME'], 'XDG_CACHE_HOME=' + self.env['XDG_CACHE_HOME'], str(self.binary), 'space', 'destroy', self.space, '--force'])
        for name, out, err in self.pending:
            self.logs(name, out, err)
        self.pending.clear()
        self.save()
        print(f'fixture: {self.root}; reports: {self.output}', flush=True)

    def wrap_lima(self):
        real = Path(shutil.which('limactl')).resolve(strict=True)
        separated(real, [self.root, self.output])
        with real.open('rb') as source:
            require(b'LIMA_STACK_WRAPPER_V1' not in source.read(65536), 'recursive Lima wrapper refused')
        version = self.command([real, '--version'])[1].strip()
        child = canonical(self.root / 'lima-home')
        cache = canonical(child / 'Library/Caches/lima')
        cache.mkdir(parents=True)
        wrapper_dir = self.root / 'libexec'
        wrapper_dir.mkdir()
        wrapper = wrapper_dir / 'limactl'
        script = ('#!/usr/bin/env python3\n# LIMA_STACK_WRAPPER_V1\n'
                  'import os,sys\nfrom pathlib import Path\n'
                  f'real={str(real)!r}\nchild={str(child)!r}\nlima={str(self.config / "lima")!r}\n'
                  'assert os.environ.get("LIMA_STACK_CHILD_HOME")==child\n'
                  'assert os.environ.get("LIMA_HOME")==lima\n'
                  'for name in (child,lima):\n'
                  ' p=Path(name)\n assert p.is_dir() and not any(q.is_symlink() for q in (p,*p.parents))\n'
                  'env=os.environ.copy()\nenv["HOME"]=child\n'
                  'os.execve(real,[real,*sys.argv[1:]],env)\n')
        wrapper.write_text(script)
        wrapper.chmod(0o700)
        self.env.update(PATH=str(wrapper_dir) + os.pathsep + self.env['PATH'], LIMA_STACK_CHILD_HOME=str(child))
        self.report.update(real_limactl=str(real), real_limactl_version=version,
                           real_limactl_sha256=file_digest(real), lima_child_home=str(child), lima_download_cache=str(cache))

    def cache_evidence(self, selected):
        cache = canonical(self.report['lima_download_cache'])
        entries = []
        for p in sorted(cache.glob('download/by-url-sha256/*/data')):
            canonical(p)
            entries.append({'path': str(p), 'bytes': p.stat().st_size})
        target = canonical(cache / 'download/by-url-sha256' / hashlib.sha256(selected['location'].encode()).hexdigest() / 'data')
        require(target.is_file() and target.stat().st_size > 0, 'private Lima archive download missing')
        actual = file_digest(target)
        require('sha256:' + actual == selected['digest'], 'downloaded archive checksum differs')
        self.report['download_evidence'] = {'entries': entries, 'archive': str(target), 'sha256': actual}

    def toolchain(self):
        required, selected = module_toolchain()
        evidence = self.report['go_toolchain'] = {'minimum': required, 'selected': selected, 'host': 'unavailable',
            'ready': False, 'module_cache': self.env['GOMODCACHE'], 'build_cache': self.env['GOCACHE']}
        try:
            # Only this diagnostic uses the installed launcher. Both probes and
            # the build share owned caches; builds use the exact module release.
            self.env['GOTOOLCHAIN'] = 'local'
            try:
                code, host, stderr = self.command(['go', 'version'], allow_failure=True)
                evidence['host'] = host.strip() or 'unavailable'
                require(code == 0, 'installed Go version probe failed: ' + stderr.strip())
            finally:
                self.env['GOTOOLCHAIN'] = selected
            code, active, stderr = self.command(['go', 'env', 'GOVERSION'], allow_failure=True)
            evidence['active'] = active.strip()
            require(code == 0 and active.strip() == selected and go_release(active.strip()) >= go_release(required),
                    'selected toolchain unavailable or version mismatch (reported ' + (active.strip() or 'unavailable') + '): ' + stderr.strip())
            code, version, stderr = self.command(['go', 'version'], allow_failure=True)
            evidence['version'] = version.strip()
            require(code == 0 and version.startswith('go version ' + selected + ' '), 'version probe failed: ' + stderr.strip())
        except (Failure, OSError) as exc:
            hint = (f'Go preflight failed: host {evidence["host"]}; go.mod requires >= {required}; '
                    f'selected {selected}. Install {selected} on PATH or permit Go standard proxy/checksum '
                    'downloads into the private caches, then rerun with a fresh fixture')
            raise Failure(hint + '; ' + str(exc), exc.code if isinstance(exc, Failure) else 1) from exc
        evidence['ready'] = True

    def build(self):
        require(self.report.get('go_toolchain', {}).get('ready') is True, 'Go toolchain preflight must pass before build')
        self.command(['make', 'BIN_DIR=' + str(self.binary.parent), 'build'], 1800)
        self.candidate()
        for name in ('yoloctl', 'yoloctl-linux-amd64', 'yoloctl-linux-arm64', 'yoloctl-vm-init-linux-amd64'):
            require((self.binary.parent / name).is_file(), 'missing companion: ' + name)
        self.identity()

    def create(self):
        self.globals(absent=True)
        self.created = True  # Includes partial create, even if the reply is lost.
        self.cli('space', 'create', self.space, '--backend', 'lima', '--account', 'default',
                 '--workdir', str(self.root / 'workdir'), '--cpus', '4', '--memory', '6GiB', '--disk', '30GiB', seconds=1800)
        self.stack()

    def operator(self):
        self.globals(absent=True)
        self.command(['bash', REPO / 'scripts/smoke/controller-operator.sh'], 1800)
        self.stack()  # The smoke recreates the VM. Revalidate, do not infer.

    def confined(self, box):
        raw = self.guest('bash', '-o', 'pipefail', '-ceu',
            'nerdctl inspect "$1" | python3 -c "$2"', 'inspect', box, INSPECT)
        runtime = json.loads(raw)
        require(runtime['privileged'] is False, 'HostConfig.Privileged must be JSON false')
        require(re.fullmatch('[a-f0-9]{64}', runtime['id']), 'invalid container ID')
        view = json.loads(self.cli('box', 'inspect', box, '--space', self.space, '--json')[1])
        require(view['runtime']['runtime_mode'] == 'confined' and view['runtime']['running'] is True, 'runtime is not verified confined/running')
        self.report.setdefault('confined_evidence', []).append({'box': box, **runtime})
        return runtime['id']

    def op(self, *args):
        self.cli('box', 'exec', 'smoke-operator', '--space', self.space, '--', 'yoloctl', *args, seconds=300)

    def lifecycle(self):
        box = 'stack-confined'
        self.cli('box', 'create', box, '--space', self.space, '--agent', 'none', '--workdir-mode', 'ephemeral', '--no-attach', '--no-autoupdate', seconds=1800)
        before = self.confined(box)
        for verb in ('stop', 'start', 'restart'):
            self.op('box', verb, box, *([] if verb == 'stop' else ['--no-attach', '--no-autoupdate']))
            if verb != 'stop':
                require(self.confined(box) == before, 'unexpected replacement during ' + verb)
        # First prove Python/transport are usable. Only our errno-tagged denial counts.
        self.cli('box', 'exec', box, '--space', self.space, '--', 'python3', '-c', 'import os,errno; print("python-ready")')
        code, out, _ = self.cli('box', 'exec', box, '--space', self.space, '--', 'python3', '-c', PTY, allow_failure=True)
        require(code == 73 and out.strip() in ('LIMA_STACK_PTY_DENIED:13', 'LIMA_STACK_PTY_DENIED:1'), 'PTY failure was not an attributed permission denial')
        self.op('box', 'set', box, '--capability', 'fs.pty.allocate')
        require(self.confined(box) == before, 'policy update replaced the container')
        out = self.cli('box', 'exec', box, '--space', self.space, '--', 'python3', '-c', PTY)[1].strip()
        require(out.startswith('LIMA_STACK_PTY_ALLOWED:/dev/pts/'), 'PTY grant did not allow confined allocation')

    def images(self):
        (self.config / 'features/lima-stack-probe.yaml').write_text(FEATURE)
        box = 'stack-image'
        self.cli('box', 'create', box, '--space', self.space, '--agent', 'none', '--features', 'lima-stack-probe',
                 '--workdir-mode', 'ephemeral', '--no-attach', '--no-autoupdate', seconds=1800)
        for rebuild in (False, True):
            if rebuild:
                self.cli('box', 'reconcile', box, '--space', self.space, '--rebuild', seconds=1800)
            self.confined(box)
            marker = self.cli('box', 'exec', box, '--space', self.space, '--', 'cat', '/usr/local/share/lima-stack-marker')[1].strip()
            require(marker == 'lima-stack-marker', 'managed multi-stage image content differs')
        self.candidate()

    def remove_fixture(self):
        # Go module/toolchain cache directories are read-only. Grant only owner
        # write on the fixed owned cache, with descriptor-based no-follow walks.
        with contextlib.ExitStack() as opened:
            flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
            root_fd = os.open(self.root, flags)
            opened.callback(os.close, root_fd)
            require(os.fstat(root_fd).st_ino == self.root_identity, 'fixture ownership changed before removal')
            try:
                cache_fd = os.open('go-mod', flags, dir_fd=root_fd)
            except FileNotFoundError:
                cache_fd = None
            if cache_fd is not None:
                opened.callback(os.close, cache_fd)
                for _, _, _, directory in os.fwalk('.', dir_fd=cache_fd, follow_symlinks=False):
                    require(time.monotonic() < self.deadline, 'fixture cache cleanup deadline exhausted')
                    os.fchmod(directory, stat.S_IMODE(os.fstat(directory).st_mode) | stat.S_IWUSR)
        shutil.rmtree(self.root)

    def cleanup(self):
        self.deadline = time.monotonic() + 300  # Separate finite cleanup grace.
        require(not self.report.get('unsettled_commands'), 'owned command cancellation is unverified; retain fixture/binaries')
        require(self.root and self.root.is_dir() and not self.root.is_symlink()
                and self.root.stat().st_ino == self.root_identity, 'fixture ownership uncertain')
        if not self.created:
            self.remove_fixture()
            return
        require(self.config.resolve() == self.root / 'home', 'config redirection during cleanup')
        # Diagnostics precede destruction; failure to read them never authorizes a retry.
        try:
            self.command(['limactl', 'list', '--format', LIMA_VIEW], seconds=30, allow_failure=True)
        except Failure as e:
            self.report['diagnostic_error'] = str(e)
        self.cli('space', 'destroy', self.space, '--force', seconds=180)
        _, raw, _ = self.command(['limactl', 'list', '--json'])
        require(not raw.strip(), 'VM termination/destruction is unproved')
        disks = self.config / 'lima/_disks'
        require(not disks.exists() or not list(disks.iterdir()), 'data disk metadata remains')
        require(not (self.config / 'lima' / ('yolo-' + self.space)).exists()
                and not (self.config / 'spaces' / self.space).exists(), 'partial fixture metadata remains')
        self.remove_fixture()  # Exact, newly owned root, only after proof above.

    def check(self, args):
        self.space = args.space
        require(re.fullmatch('[a-z0-9][a-z0-9-]*', self.space or ''), 'invalid --space')
        self.config = canonical(args.config_root)
        # --check selects its target explicitly, never from inherited homes.
        self.cache = canonical(self.config / 'cache')
        self.binary = canonical(args.binary)
        output = canonical(args.output)
        require(not output.exists(), '--output must be new')
        require(output.parent.is_dir(), '--output parent must exist')
        for p in (self.config, self.config / 'lima', self.config / 'spaces' / self.space, self.binary.parent):
            canonical(p)
            require(p.is_dir(), 'UNKNOWN: required existing directory unavailable: ' + str(p))
        require(os.access(self.binary, os.X_OK), 'invalid binary')
        bootstrap = self.config / 'spaces' / self.space / 'space.yaml'
        canonical(bootstrap)
        require(bootstrap.stat().st_size <= 1024 * 1024, 'oversized bootstrap')
        text = bootstrap.read_text()
        require(scalar(text, 'schema_version') == '3' and scalar(text, 'name') == self.space, 'unsupported bootstrap identity')
        backends = re.findall(r'^ +backend: *([^\n]*)$', text, re.M)
        require(len(backends) <= 1 and all(v.strip().strip('"') in ('', 'lima') for v in backends), 'target is not Lima')
        saved = {key: scalar(text, key) for key in ('disk_id', 'disk_uuid', 'bundle')}
        # CLI startup only mkdirs the already-existing Lima home. Never start a VM.
        self.env = environment(self.config)
        self.env['TMPDIR'] = str(output.parent)
        info = self.lima()
        protected = [self.config, self.cache, self.binary.parent, REPO]
        protected += [Path(m['location']).expanduser() for m in info.get('mounts', []) if 'location' in m]
        home = Path.home()
        protected += [home / 'Library/Application Support', home / 'Library/Caches', home / '.config', home / '.cache', home / '.lima']
        separated(output, protected)
        self.output = output
        output.mkdir(mode=0o700)
        for name, out, err in self.pending:
            self.logs(name, out, err)
        self.pending.clear()
        self.report.update(mode='check', space=self.space, config_root=str(self.config), bootstrap=saved,
                           global_configs=self.globals(absent=False))
        self.identity()
        for name in ('yoloctl-linux-amd64', 'yoloctl-linux-arm64', 'yoloctl-vm-init-linux-amd64'):
            require((self.binary.parent / name).is_file(), 'UNKNOWN: missing matching companion ' + name)
        require(self.report['bundle'] == saved['bundle'], 'UNKNOWN: binary/controller bundle mismatch')
        show = self.stack(pinned=bool(args.compare))
        require(show['account_disk_id'] == saved['disk_id'] and show['account_disk_uuid'] == saved['disk_uuid'], 'disk identity differs')
        boxes = box_inventory(json.loads(self.cli('box', 'list', '--space', self.space, '--json')[1]))
        accounts = json.loads(self.cli('account', 'list', '--space', self.space, '--json')[1])
        spaces = json.loads(self.cli('space', 'list', '--json')[1])
        self.report.update(boxes=boxes,
                           accounts=sorted(a['name'] for a in accounts),
                           spaces=[{k: s[k] for k in ('name', 'status')} for s in spaces],
                           desired_start_list=sorted(b['name'] for b in boxes if b['dimensions']['runtime'] == 'running'))
        if args.compare:
            previous = json.loads(canonical(args.compare).read_text())
            require(isinstance(previous, dict) and previous.get('schema') == 1 and previous.get('mode') == 'check'
                    and previous.get('result') == 'PASS', 'comparison requires a successful check report')
            differences = []
            for key in ('space', 'accounts'):
                if previous.get(key) != self.report[key]: differences.append(key)
            for key in ('disk_id', 'disk_uuid'):
                if previous.get('bootstrap', {}).get(key) != saved[key]: differences.append(key)
            if sorted(b['name'] for b in previous.get('boxes', [])) != sorted(b['name'] for b in boxes):
                differences.append('box names')
            before_boxes = box_inventory(previous.get('boxes'))
            expected_starts = [b['name'] for b in before_boxes if b['dimensions']['runtime'] == 'running']
            running = [b['name'] for b in boxes if b['dimensions']['runtime'] == 'running']
            if any(b not in running for b in expected_starts): differences.append('desired restarts')
            old_states = {b['name']: b['state'] for b in previous.get('boxes', [])}
            self.report['status_changes'] = {b['name']: [old_states.get(b['name']), b['state']] for b in boxes if old_states.get(b['name']) != b['state']}
            for dimension in ('runtime', 'session'):
                old = {b['name']: b['dimensions'].get(dimension) for b in before_boxes}
                self.report[dimension + '_changes'] = {b['name']: [old.get(b['name']), b['dimensions'].get(dimension)]
                    for b in boxes if old.get(b['name']) != b['dimensions'].get(dimension)}
            self.report['differences'] = differences
            require(not differences, 'upgrade inventory differs: ' + ', '.join(differences))
        require(bootstrap.read_text() == text, 'bootstrap changed during observation')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    for flag in ('space', 'binary', 'config-root', 'output', 'compare'):
        parser.add_argument('--' + flag)
    args = parser.parse_args(argv)
    if args.check:
        require(all((args.space, args.binary, args.config_root, args.output)), '--check requires space, binary, config-root and output')
    else:
        require(not any((args.space, args.binary, args.config_root, args.output, args.compare)), 'target options require --check')
    host_preflight()
    runner = Runner()
    def interrupted(signum, _frame):
        raise Failure('interrupted by signal ' + str(signum), 128 + signum)
    handlers = {s: signal.signal(s, interrupted) for s in (signal.SIGINT, signal.SIGTERM)}
    status = 0
    try:
        if args.check:
            runner.phase('check', lambda: runner.check(args))
        else:
            for name in ('setup', 'toolchain', 'build', 'create', 'operator', 'lifecycle', 'images'):
                if name != 'setup':
                    runner.candidate()
                    runner.globals(absent=True)
                    runner.report.setdefault('host_lima_versions', []).append(runner.command(['limactl', '--version'])[1].strip())
                runner.phase(name, getattr(runner, name))
                runner.candidate()
                runner.report.setdefault('host_lima_versions', []).append(runner.command(['limactl', '--version'])[1].strip())
    except (Failure, OSError, ValueError, KeyError, TypeError) as e:
        status = e.code if isinstance(e, Failure) else 1
        runner.report['error'] = str(e)
    finally:
        for s in handlers: signal.signal(s, signal.SIG_IGN)
        if not args.check and runner.root:
            try:
                runner.phase('cleanup', runner.cleanup)
            except (Failure, OSError, ValueError) as e:
                runner.report['cleanup'] = 'CLEANUP_INCOMPLETE: ' + str(e)
                status = status or (e.code if isinstance(e, Failure) else 1)
        runner.report.update(result='FAIL' if status else 'PASS', exit_code=status)
        runner.save()
        for s, handler in handlers.items(): signal.signal(s, handler)
        print(f'{runner.report["result"]}: {runner.report.get("error", "Lima stack checks complete")}; report: {runner.output}', flush=True)
        if runner.report.get('cleanup'):
            print(runner.report['cleanup'] + '; retain fixture/binaries: ' + str(runner.root))
            print('Recovery after diagnosis: ' + runner.report.get('recovery', 'see report'))
    return status


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (Failure, OSError) as exc:
        print('FAIL preflight: ' + str(exc), file=sys.stderr)
        sys.exit(exc.code if isinstance(exc, Failure) else 1)
