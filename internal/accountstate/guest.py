# Privileged VM-side filesystem administration. Requests arrive only on stdin.
import base64
import json
import os
import shutil
import stat
import subprocess
import sys
import uuid

FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC

def directory(fd, name, create=False, uid=0, gid=0):
    if name in ('', '.', '..') or '/' in name:
        raise ValueError('invalid path component')
    if create:
        try:
            os.mkdir(name, 0o700, dir_fd=fd)
            os.chown(name, uid, gid, dir_fd=fd, follow_symlinks=False)
        except FileExistsError:
            pass
    return os.open(name, FLAGS, dir_fd=fd)

def regular(fd, name):
    f = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
    if not stat.S_ISREG(os.fstat(f).st_mode):
        os.close(f)
        raise ValueError('expected regular file')
    return f

def write(fd, name, data, uid, gid):
    tmp = '.yoloctl-' + uuid.uuid4().hex
    f = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=fd)
    try:
        with os.fdopen(f, 'wb') as out:
            out.write(data)
            out.flush()
            os.fchown(out.fileno(), uid, gid)
            os.fsync(out.fileno())
        os.rename(tmp, name, src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)
    finally:
        try:
            os.unlink(tmp, dir_fd=fd)
        except FileNotFoundError:
            pass

SETTINGS_BYTES = 1 << 20
# Beside the settings: the statusLine `account observe --enable` replaced.
STASH = 'yoloctl-statusline.json'


def strict_object(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError('settings file has a duplicate key')
        out[key] = value
    return out


def load_json(fd, name):
    """A JSON object file in fd, or None when absent; anything else fails."""
    try:
        f = regular(fd, name)
    except FileNotFoundError:
        return None
    with os.fdopen(f, 'rb') as inp:
        raw = inp.read(SETTINGS_BYTES + 1)
    if len(raw) > SETTINGS_BYTES:
        raise ValueError('settings file is too large')
    def constant(name):
        raise ValueError('settings file is not strict JSON')
    value = json.loads(raw, object_pairs_hook=strict_object, parse_constant=constant)
    if not isinstance(value, dict):
        raise ValueError('settings file is not a JSON object')
    return value


def runs_command(line):
    """Whether a statusLine object runs a command; the writer chains only these."""
    return (isinstance(line, dict) and line.get('type', 'command') == 'command' and line.get('enabled', True) is True
            and isinstance(line.get('command'), str) and line['command'].strip() != '')


def status_line(fd, name, spec, uid, gid):
    """Set or restore the reporter's statusLine in settings file name, keeping
    every other key; the replaced statusLine is saved verbatim in STASH. A
    missing file is never created: the agent's launcher seeds a new account."""
    command = spec['command']
    settings = load_json(fd, name)
    current = settings.get('statusLine') if settings is not None else None
    ours = isinstance(current, dict) and current.get('command') == command
    if spec['enable']:
        if settings is None:
            return {'outcome': 'absent'}
        if ours:
            return {'outcome': 'unchanged'}
        previous = settings.get('statusLine')
        line = {'type': 'command', 'command': command}
        if runs_command(previous):
            line.update({k: previous[k] for k in ('padding', 'stack_with_default') if k in previous})
        else:
            # The reporter prints nothing of its own: keep agy's line.
            line['stack_with_default'] = True
        write(fd, STASH, json.dumps({'v': 1, 'previous': previous}).encode(), uid, gid)
        settings['statusLine'] = line
        write(fd, name, (json.dumps(settings, indent=2) + '\n').encode(), uid, gid)
        return {'outcome': 'enabled'}
    if not ours:
        return {'outcome': 'unchanged'}
    saved = load_json(fd, STASH)
    if saved is not None and (saved.get('v') != 1 or 'previous' not in saved):
        raise ValueError('the saved status line is unreadable; restore statusLine by hand')
    restored = saved is not None and saved['previous'] is not None
    if restored:
        settings['statusLine'] = saved['previous']
    else:
        del settings['statusLine']
    write(fd, name, (json.dumps(settings, indent=2) + '\n').encode(), uid, gid)
    try:
        os.unlink(STASH, dir_fd=fd)
    except FileNotFoundError:
        pass
    os.fsync(fd)
    return {'outcome': 'restored' if restored else 'removed'}


def run(req):
    mount = req['mount']
    info = json.loads(subprocess.check_output(['findmnt', '-J', '--mountpoint', mount, '-o', 'FSTYPE,UUID']))['filesystems'][0]
    fsid = info.get('uuid')
    if info.get('fstype') != 'ext4' or not fsid:
        raise ValueError('account disk is not a mounted ext4 filesystem')
    if req.get('uuid') and req['uuid'] != fsid:
        raise ValueError('account disk filesystem identity does not match declaration')
    disk = os.open(mount, FLAGS)
    action = req['action']
    try:
        try:
            f = regular(disk, '.yoloctl-owner')
        except FileNotFoundError:
            if action != 'initialize' or req.get('uuid'):
                raise ValueError('account disk ownership marker is missing')
            write(disk, '.yoloctl-owner', req['id'].encode(), 0, 0)
        else:
            with os.fdopen(f, 'rb') as inp:
                if inp.read(100) != req['id'].encode():
                    raise ValueError('account disk belongs to another space')
        root = directory(disk, 'account-state', create=action == 'initialize')
    finally:
        os.close(disk)
    try:
        if action in ('initialize', 'prepare'):
            target = req['root']
            os.makedirs(target, mode=0o700, exist_ok=True)
            targetfd = os.open(target, FLAGS)
            os.close(targetfd)
            # A bind to the exact disk directory preserves one superblock/cache.
            if not os.path.samestat(os.stat(target), os.fstat(root)):
                if subprocess.run(['mountpoint', '-q', target]).returncode == 0:
                    raise ValueError('account root already mounts different storage')
                subprocess.run(['mount', '--bind', mount + '/account-state', target], check=True)
            return {'uuid': fsid}
        # All operations require the managed bind to be live, never a fallback dir.
        if not os.path.samestat(os.stat(req['root'], follow_symlinks=False), os.fstat(root)):
            raise ValueError('account state mount is unavailable')
        if action == 'verify':
            return {'uuid': fsid}
        name = req['account']
        if action == 'remove':
            if not shutil.rmtree.avoids_symlink_attacks:
                raise ValueError('safe directory deletion is unavailable')
            try:
                child = directory(root, name)
            except FileNotFoundError:
                return {}
            os.close(child)
            shutil.rmtree(name, dir_fd=root)
            os.fsync(root)
            return {}
        create = action in ('mkdir', 'write')
        # A missing account, leaf or directory is a missing settings file.
        absent = {'outcome': 'absent'} if action == 'statusline' else {}
        try:
            acct = directory(root, name, create=create)
        except FileNotFoundError:
            return {'present': False, 'leaves': [], **absent}
        try:
            if action == 'list':
                leaves = [n for n in os.listdir(acct) if stat.S_ISDIR(os.stat(n, dir_fd=acct, follow_symlinks=False).st_mode)]
                return {'leaves': sorted(leaves)}
            agent = req.get('agent')
            if not agent:
                return {}
            try:
                leaf = directory(acct, agent, create=create, uid=req['uid'], gid=req['gid'])
            except FileNotFoundError:
                return {'present': False, **absent}
        finally:
            os.close(acct)
        try:
            if action == 'mkdir':
                return {}
            parts = req['path'].split('/')
            if not parts or any(p in ('', '.', '..') for p in parts):
                raise ValueError('invalid native file path')
            for part in parts[:-1]:
                try:
                    nextfd = directory(leaf, part, create=create, uid=req['uid'], gid=req['gid'])
                except FileNotFoundError:
                    return {'present': False, **absent}
                os.close(leaf)
                leaf = nextfd
            if action == 'write':
                write(leaf, parts[-1], base64.b64decode(req['data'], validate=True), req['uid'], req['gid'])
                return {}
            if action == 'statusline':
                spec = json.loads(base64.b64decode(req['data'], validate=True))
                return status_line(leaf, parts[-1], spec, req['uid'], req['gid'])
            if action == 'exists':
                try:
                    f = regular(leaf, parts[-1])
                except FileNotFoundError:
                    return {'present': False}
                os.close(f)
                return {'present': True}
            raise ValueError('unknown native-state operation')
        finally:
            os.close(leaf)
    finally:
        os.close(root)

if __name__ == '__main__':
    try:
        print(json.dumps(run(json.load(sys.stdin))))
    except Exception as exc:
        # Do not print requests, data, tracebacks, or native file contents.
        print('account storage operation failed: ' + str(exc), file=sys.stderr)
        sys.exit(1)
