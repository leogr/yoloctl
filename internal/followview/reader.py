# Fixed finite VM reader. Request data is JSON on stdin, never shell source.
import base64
import hashlib
import json
import os
import signal
import stat
import sys
import time

WHOLE = 2 << 20
WINDOW = 1 << 20


def open_directory(path):
    # Search-only ancestor handles need no directory-listing permission.
    directory_flags = getattr(os, 'O_PATH', os.O_RDONLY) | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
    fd = os.open('/', directory_flags)
    try:
        for component in path.split('/'):
            if component:
                next_fd = os.open(component, directory_flags, dir_fd=fd)
                os.close(fd)
                fd = next_fd
        return fd
    except BaseException:
        os.close(fd)
        raise


def open_selected(request, directory=False):
    path = request['path']
    if not os.path.isabs(path) or os.path.normpath(path) != path:
        raise ValueError('an absolute, clean path is required')
    roots = [root for root in request['roots']
             if os.path.isabs(root) and os.path.normpath(root) == root
             and (path == root or path.startswith(root.rstrip('/') + '/'))]
    if not roots:
        raise ValueError('path is outside the current space grants')
    root = max(roots, key=len)
    fd = open_directory(root)
    try:
        parts = os.path.relpath(path, root).split('/')
        if parts == ['.']:
            if directory:
                return os.open('.', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            raise ValueError('select a regular file, not the grant directory')
        for component in parts[:-1]:
            next_fd = os.open(component, getattr(os, 'O_PATH', os.O_RDONLY) | os.O_DIRECTORY |
                              os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC
        if directory:
            flags |= os.O_DIRECTORY
        return os.open(parts[-1], flags, dir_fd=fd)
    finally:
        os.close(fd)


def trim_window(data, start, total):
    if start:
        skipped = 0
        while skipped < min(3, len(data)) and data[skipped] & 0xc0 == 0x80:
            skipped += 1
        data, start = data[skipped:], start + skipped
    if start + len(data) < total:
        # At most one partial UTF-8 sequence, never a scan for a newline.
        last = len(data) - 1
        while last >= max(0, len(data) - 4) and data[last] & 0xc0 == 0x80:
            last -= 1
        if last >= 0:
            first = data[last]
            needed = 2 if 0xc2 <= first <= 0xdf else 3 if 0xe0 <= first <= 0xef else 4 if 0xf0 <= first <= 0xf4 else 1
            if len(data) - last < needed:
                data = data[:last]
    return data, start


def read(request):
    started = time.monotonic_ns()
    fd = open_selected(request)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise ValueError('select a readable regular file')
        large = info.st_size > WHOLE
        start = 0
        if large:
            start = max(0, info.st_size - WINDOW) if request.get('tail') else min(max(0, request.get('offset', 0)), max(0, info.st_size - WINDOW))
        data = os.pread(fd, min(WINDOW if large else WHOLE, max(0, info.st_size - start)), start)
        if large:
            data, start = trim_window(data, start, info.st_size)
        digest = hashlib.sha256(data).hexdigest()
        end = start + len(data)
        key = f'{start}:{end}:{int(large)}:{digest}'
        result = dict(size=info.st_size, start=start, end=end, large=large,
                      device=info.st_dev, inode=info.st_ino, mtime_ns=info.st_mtime_ns,
                      hash=digest, key=key, read_ns=time.monotonic_ns()-started, changed=key != request.get('previous'))
        if result['changed']:
            result['data'] = base64.b64encode(data).decode('ascii')
        return result
    finally:
        os.close(fd)


def listing(request):
    fd = open_selected(request, directory=True)
    try:
        entries = []
        with os.scandir(fd) as scan:
            for entry in scan:
                if not entry.name.startswith(request.get('prefix', '')):
                    continue
                if any(c in entry.name for c in '\t\r\n'):
                    continue
                if entry.name.startswith('.') and not request.get('prefix', '').startswith('.'):
                    continue
                if entry.is_symlink():
                    continue
                if entry.is_dir(follow_symlinks=False):
                    entries.append(entry.name + '/')
                elif entry.is_file(follow_symlinks=False):
                    entries.append(entry.name)
                if len(entries) >= 512:
                    break
        return dict(entries=sorted(entries))
    finally:
        os.close(fd)


try:
    signal.alarm(5)
    request = json.loads(sys.stdin.buffer.read(1 << 20))
    if request['op'] == 'read':
        result = read(request)
    elif request['op'] == 'list':
        result = listing(request)
    else:
        raise ValueError('unknown follow operation')
except Exception as error:
    result = dict(error=f'{error}; use the direct path within a current grant (symlink components are refused)')
json.dump(result, sys.stdout, separators=(',', ':'))
