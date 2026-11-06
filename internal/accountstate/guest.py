# Privileged VM-side filesystem administration. Requests arrive only on stdin.
import base64
import json
import os
import re
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


# Authentication reads remain inside the verified native leaf and never return
# credential bytes. Unknown provider formats are not evidence of valid login.
def auth_read(fd, relative, limit=SETTINGS_BYTES):
    parts = relative.split('/')
    if not parts or any(p in ('', '.', '..') or '\\' in p or '\0' in p for p in parts):
        raise ValueError('invalid native auth path')
    current = os.dup(fd)
    try:
        for part in parts[:-1]:
            child = directory(current, part)
            os.close(current)
            current = child
        f = regular(current, parts[-1])
        with os.fdopen(f, 'rb') as inp:
            return b'' if limit is None else inp.read(limit + 1)
    except FileNotFoundError:
        return None
    except (OSError, ValueError):
        # A selector/filename must never leak as part of a filesystem error.
        raise OSError('native credential storage unavailable') from None
    finally:
        os.close(current)


def auth_object(raw):
    if raw is None:
        return None, 'missing'
    if len(raw) > SETTINGS_BYTES:
        return None, 'invalid'
    try:
        def constant(name):
            raise ValueError('invalid JSON')
        value = json.loads(raw, object_pairs_hook=strict_object, parse_constant=constant)
    except (ValueError, UnicodeError, RecursionError):
        return None, 'invalid'
    if not isinstance(value, dict) or not value:
        return None, 'invalid'
    return value, None


def material(value):
    return isinstance(value, str) and bool(value.strip())


def auth_spec(req):
    spec = json.loads(base64.b64decode(req.get('data', ''), validate=True))
    if spec.get('format', '') not in ('', 'claude', 'codex'):
        raise ValueError('unsupported native auth format')
    return spec


def auth_status(fd, relative, spec):
    def finding(source, outcome):
        return dict(source=source, outcome=outcome)
    def read(name, limit=SETTINGS_BYTES):
        return None if fd is None else auth_read(fd, name, limit)
    fmt = spec.get('format', '')
    raw = read(relative, SETTINGS_BYTES if fmt else None)
    if not fmt:
        results = [finding(relative, 'missing' if raw is None else 'file-present')]
    else:
        doc, outcome = auth_object(raw)
        if doc is not None:
            if fmt == 'claude':
                oauth = doc.get('claudeAiOauth')
                present = isinstance(oauth, dict) and material(oauth.get('accessToken'))
            else:
                tokens = doc.get('tokens')
                present = material(doc.get('OPENAI_API_KEY')) or (isinstance(tokens, dict) and material(tokens.get('access_token')))
            outcome = 'credentials-present' if present else 'unverified'
        results = [finding(relative, outcome)]
    profiles = spec.get('profiles_dir', '')
    if not profiles:
        return {'auth': results}
    # Do not expose selector contents as diagnostics, including malformed input.
    source = profiles
    active = read(profiles + '/active_config', 256)
    if active is None:
        name = 'default'
    else:
        try:
            name = active.decode('utf-8').strip()
        except UnicodeError:
            name = ''
        if (len(active) > 256 or not name or name in ('.', '..') or len(name.encode('utf-8')) > 250
                or any(c in name for c in '/\\\0\r\n\t')):
            results.append(finding(source, 'invalid'))
            return {'auth': results}
    cfg, cfg_outcome = auth_object(read(profiles + '/configs/' + name + '.json'))
    creds, creds_outcome = auth_object(read(profiles + '/credentials/' + name + '.json'))
    if cfg is None:
        outcome = 'invalid' if 'invalid' in (cfg_outcome, creds_outcome) else 'missing'
    elif not isinstance(cfg.get('authentication'), dict) or not material(cfg['authentication'].get('type')):
        outcome = 'unverified'
    elif cfg['authentication']['type'] != 'user_oauth':
        outcome = 'external'
    elif creds is None:
        outcome = creds_outcome
    elif creds.get('type') != 'oauth_token':
        outcome = 'unverified'
    else:
        outcome = 'credentials-present' if material(creds.get('access_token')) else 'invalid'
    results.append(finding(source, outcome))
    return {'auth': results}


RESUME_SCAN_ENTRIES = 10000


def resume_file_present(leaf, pattern, raw_id):
    """Inspect metadata only; a file is not proof that Claude can resume it."""
    parts = pattern.split('/')
    literal = r'[A-Za-z0-9_.-]+'
    if (len(parts) < 3 or parts[-2] != '*' or pattern.count('*') != 1
            or pattern.count('{{session_id}}') != 1
            or any(p in ('.', '..') or not re.fullmatch(literal, p) for p in parts[:-2])):
        raise ValueError('invalid native resume file pattern')
    filename = parts[-1]
    if ('{{session_id}}' not in filename or not filename.split('{{session_id}}', 1)[1]
            or not re.fullmatch(literal, filename.replace('{{session_id}}', '', 1))):
        raise ValueError('invalid native resume filename')
    session_id = raw_id.decode('ascii')
    if str(uuid.UUID(session_id)) != session_id:
        raise ValueError('invalid native conversation ID')
    filename = filename.replace('{{session_id}}', session_id)
    current = os.dup(leaf)
    try:
        for part in parts[:-2]:
            try:
                child = directory(current, part)
            except FileNotFoundError:
                return False
            os.close(current)
            current = child
        with os.scandir(current) as entries:
            for count, entry in enumerate(entries, 1):
                if count > RESUME_SCAN_ENTRIES:
                    raise ValueError('native conversation search limit exceeded')
                mode = entry.stat(follow_symlinks=False).st_mode
                if stat.S_ISLNK(mode):
                    raise ValueError('native conversation search contains a linked entry')
                if not stat.S_ISDIR(mode):
                    continue
                project = directory(current, entry.name)
                try:
                    try:
                        f = regular(project, filename)
                    except FileNotFoundError:
                        continue
                    os.close(f)
                    return True
                finally:
                    os.close(project)
        return False
    except OSError:
        # Project names and file contents never leave the helper.
        raise OSError('native conversation storage unavailable') from None
    finally:
        os.close(current)


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
        if action == 'auth-status':
            absent = auth_status(None, req['path'], auth_spec(req))
        try:
            acct = directory(root, name, create=create)
        except FileNotFoundError:
            if action == 'resume-file':
                raise ValueError('native conversation account leaf unavailable') from None
            return {'present': False, 'leaves': [], **absent}
        try:
            if action == 'list':
                leaves = [n for n in os.listdir(acct) if stat.S_ISDIR(os.stat(n, dir_fd=acct, follow_symlinks=False).st_mode)]
                return {'leaves': sorted(leaves)}
            agent = req.get('agent')
            if not agent:
                if action == 'resume-file':
                    raise ValueError('native conversation agent leaf unavailable')
                return {}
            try:
                leaf = directory(acct, agent, create=create, uid=req['uid'], gid=req['gid'])
            except FileNotFoundError:
                if action == 'resume-file':
                    raise ValueError('native conversation agent leaf unavailable') from None
                return {'present': False, **absent}
        finally:
            os.close(acct)
        try:
            if action == 'resume-file':
                present = resume_file_present(leaf, req['path'], base64.b64decode(req['data'], validate=True))
                return {'outcome': 'present' if present else 'absent'}
            if action == 'auth-status':
                return auth_status(leaf, req['path'], auth_spec(req))
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
