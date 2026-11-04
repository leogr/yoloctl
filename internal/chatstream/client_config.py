# Confined launcher for one human chat view's AIRC client.
# argv: AIRC binary, published client config, this connection's nickname and
# its mode: "join", or "take-over", which alone enables AIRC's same-account
# regain (closing this account's other session holding the nickname).
# The published configuration is copied with only the nickname replaced and
# that opt-in added into
# an unlinked private temporary descriptor that the client inherits, so nothing
# is left after it exits. Relative paths resolve against the published file's
# directory, as AIRC itself resolves them. The token file is never opened and
# no configuration byte is printed.
import json
import os
import stat
import sys
import tempfile
import tomllib

KEYS = ('unix', 'artifact_unix', 'account', 'nick', 'token_file', 'channels')
REQUIRED = ('account', 'nick', 'token_file')
PATHS = ('unix', 'artifact_unix', 'token_file')
LIMIT = 64 << 10


def fail(reason):
    sys.stderr.write('managed chat client configuration: ' + reason + '\n')
    sys.exit(2)


def printable(value):
    return isinstance(value, str) and value != '' and all(' ' <= c <= '~' for c in value)


def valid_nick(nick):
    return 0 < len(nick) <= 24 and all(
        c.isascii() and (c.isalpha() or c == '_' or i > 0 and (c.isdigit() or c == '-'))
        for i, c in enumerate(nick))


def published(source):
    try:
        fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    except OSError:
        fail('the published file is unavailable')
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            fail('the published file is not a regular file')
        raw = b''
        while len(raw) <= LIMIT:
            chunk = os.read(fd, LIMIT + 1 - len(raw))
            if not chunk:
                break
            raw += chunk
    finally:
        os.close(fd)
    if len(raw) > LIMIT:
        fail('the published file exceeds its bound')
    try:
        return tomllib.loads(raw.decode('utf-8'))
    except (UnicodeDecodeError, tomllib.TOMLDecodeError):
        fail('the published file is not valid TOML')


def checked(config, what):
    for key in KEYS:
        if key in config:
            items = config[key] if key == 'channels' else [config[key]]
            if not isinstance(items, list) or not all(printable(item) for item in items):
                fail(what + ' has an unexpected ' + key)


def derive(source, nick, regain):
    config = published(source)
    if not set(config) <= set(KEYS) or not set(REQUIRED) <= set(config):
        fail('the published file has unexpected fields')
    checked(config, 'the published file')
    base = os.path.dirname(os.path.abspath(source))
    for key in PATHS:
        if key in config:
            config[key] = os.path.join(base, config[key])
    config['nick'] = nick
    checked(config, 'the private copy')
    # Printable ASCII JSON strings and string arrays are valid TOML.
    lines = ''.join(key + ' = ' + json.dumps(config[key]) + '\n' for key in KEYS if key in config)
    return (lines + ('regain = true\n' if regain else '')).encode()


def main():
    if len(sys.argv) != 5:
        fail('expected the client, published file, nickname and mode')
    client, source, nick, mode = sys.argv[1:]
    if not valid_nick(nick):
        fail('invalid connection nickname')
    if mode not in ('join', 'take-over'):
        fail('invalid connection mode')
    data = memoryview(derive(source, nick, mode == 'take-over'))
    try:
        # Some container overlay filesystems do not support O_TMPFILE.
        # TemporaryFile falls back to exclusive creation and unlinks the file
        # before returning, so no configuration bytes ever have a pathname.
        config = tempfile.TemporaryFile(dir='/tmp')
        fd = config.fileno()
        while data:
            data = data[os.write(fd, data):]
    except OSError:
        fail('an anonymous private configuration is unavailable')
    os.set_inheritable(fd, True)
    try:
        os.execv(client, [client, '--config', '/proc/self/fd/%d' % fd, '--jsonl', '2'])
    except OSError:
        fail('the AIRC client could not start')


main()
