"""Append one agent report to this launch's observation directory.

Runs with the agent's own credentials: as a Claude Code async managed hook (no
argument) or managed status line command (argument `statusline`), as a Codex
System-layer hook (argument `codex`), as the controller's refresh read of a
Codex box (`codex-query GEN THREAD`), or as the Antigravity CLI status line
from the account's own settings (argument `agy`). The controller
created /run/yoloctl/agent/gen-$YOLOCTL_AGENT_GEN for this launch; this script
never creates directories, follows links or blocks a turn: without the
variable or the directory, on lock contention past 500 ms, or on any error it
writes nothing and exits 0. Everything it writes is agent-reported evidence,
read back by internal/agentsession/observe, never authority.

Claude Code 2.1.311 (S0 capture) sent model only on an interactive startup
SessionStart, effort.level on main Stops and every status line, and model.id,
effort.level, version and context_window on every status line; resume and
clear starts carry no model. PostModelSwitch.to_model is documented, not
captured. A subagent event's session_id can name a later conversation, so
those lines are only ever marked as subagent evidence.

Main Claude hooks report working on UserPromptSubmit, idle on Stop (working
while background_tasks or session_crons is non-empty), and failed on
StopFailure. Recognized Notifications carry only a mapped state and envelope;
unknown or subagent-tagged Notifications write nothing. Prompts, messages,
errors, task details and paths are never recorded.

As the status line, it records first and then hands the same stdin bytes to
the user's own statusLine command from the user-scope settings file, with
`exec sh -c`, so that command keeps Claude's environment, cancellation and
exit status. Project and local status lines are never run: Claude runs those
only in a trusted workspace, a decision this script cannot apply. Without a
user command it prints nothing.

As agy's status line it keeps the documented fields of the same family
(model.id and model.effort, version, context_window, the conversation ID)
and maps agent_state and tool_confirmation_pending onto the reader's states.
agy has no system settings layer, so its status line is the account's; the
command it replaced, if any, was saved by `yoloctl account observe` in
yoloctl-statusline.json beside agy's settings, and runs after recording the
same way.

As a Codex hook it keeps the event, the session ID as a canonical lowercase
UUID, the start source and the model slug. On a main SessionStart or Stop it
then asks the box's Codex app-server daemon for exactly that thread: one
finite thread/read over the daemon's control socket under
/tmp/codex-daemon-<uid> (a directory owned by this user, mode 0700, not a
link; every user-owned socket in it, in name order), within 500 ms in total.
It reads only after SO_PEERCRED binds the peer to a `codex app-server` process
of this user, and appends a `query` line with model and effort only when the
peer is still the same process afterwards, the thread ID is exact, the thread
is loaded (idle or active) and both values are known. Anything else writes
nothing: a launch with `-c` runs no daemon, so it reports the hook's model only.
"""
import fcntl
import json
import os
import re
import sys
import time

ROOT = '/run/yoloctl/agent'
FILE_BYTES = 262144  # observe.FileBytes
LINE_BYTES = 4096  # observe.LineBytes
PAYLOAD_BYTES = 1 << 20
SETTINGS_BYTES = 1 << 20
COMMAND_BYTES = 65536
PIPE_BYTES = 65536
CONTEXT_TOKENS = 10 ** 9
LOCK_WAIT = 0.5
TMP = '/tmp'
SHELL = '/bin/sh'
# Set for the user's command, so a user status line naming this wrapper
# cannot recurse into itself.
WRAPPED = 'YOLOCTL_STATUSLINE_WRAPPED'
EVENTS = ('SessionStart', 'SessionEnd', 'PostModelSwitch', 'Stop', 'SubagentStop',
          'UserPromptSubmit', 'Notification', 'StopFailure')
NOTIFICATION_STATES = {'permission_prompt': 'waiting_permission',
                       'agent_needs_input': 'waiting_input',
                       'elicitation_dialog': 'waiting_dialog',
                       'elicitation_url_dialog': 'waiting_dialog',
                       'idle_prompt': 'idle'}
# The reader's enumerated efforts: any other value is left out, as unknown,
# rather than making the whole line, and the launch's reports, invalid.
EFFORTS = ('none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra', 'persistent')
# The reader's enumerated states; a mode writes only these, and an agent
# value it does not map writes no state.
STATES = ('working', 'idle', 'waiting_permission', 'waiting_input', 'waiting_dialog', 'failed')
PRINTABLE = re.compile(r'[\x20-\x7e]+')
EVENTS_FILE = re.compile(r'^events\.(0|[1-9][0-9]{0,18})\.jsonl$')
DIR_FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
FILE_FLAGS = os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK


def generation():
    gen = os.environ.get('YOLOCTL_AGENT_GEN', '')
    if re.fullmatch(r'[1-9][0-9]{0,19}', gen) and int(gen) < 1 << 64:
        return gen
    return None


def text(value, limit):
    if isinstance(value, str) and 0 < len(value) <= limit:
        return value
    return None


def printable(value, limit):
    value = text(value, limit)
    return value if value is not None and PRINTABLE.fullmatch(value) else None


def count(value, limit):
    """A reported integer in [0, limit], or None; booleans are not counts."""
    if isinstance(value, int) and not isinstance(value, bool) and 0 <= value <= limit:
        return value
    return None


def field(payload, key, inner):
    value = payload.get(key)
    return value.get(inner) if isinstance(value, dict) else value


def encode(out):
    line = (json.dumps(out, separators=(',', ':'), ensure_ascii=True) + '\n').encode()
    return line if len(line) <= LINE_BYTES else None


def record(payload, now, pid):
    """The kept fields of one hook payload, or None."""
    if not isinstance(payload, dict) or payload.get('hook_event_name') not in EVENTS:
        return None
    event = payload['hook_event_name']
    out = {'v': 1, 'event': event, 'pid': pid, 'ts': now}
    session = text(payload.get('session_id'), 128)
    if session:
        out['session_id'] = session
    subagent = event == 'SubagentStop' or 'agent_id' in payload or 'agent_type' in payload
    if event == 'Notification':
        kind = payload.get('notification_type')
        state = NOTIFICATION_STATES.get(kind) if isinstance(kind, str) else None
        # Notifications are state-only; a subagent notification cannot carry
        # state under the shared reader contract, so omit the whole line.
        if state is None or subagent:
            return None
        out['state'] = state
        return out
    if event == 'SessionStart' and text(payload.get('source'), 32):
        out['source'] = payload['source']
    model = field(payload, 'to_model' if event == 'PostModelSwitch' else 'model', 'id')
    if printable(model, 128):
        out['model'] = model
    effort = field(payload, 'effort', 'level')
    if effort in EFFORTS:
        out['effort'] = effort
    if subagent:
        out['subagent'] = True
    elif event == 'UserPromptSubmit':
        out['state'] = 'working'
    elif event == 'Stop':
        pending = any(isinstance(payload.get(key), list) and payload[key]
                      for key in ('background_tasks', 'session_crons'))
        out['state'] = 'working' if pending else 'idle'
    elif event == 'StopFailure':
        out['state'] = 'failed'
    return out


def statusline_record(payload, now, pid, wrapped, handover):
    """The kept fields of one status line payload, or None.

    Context is the window size with the used percentage, both as reported;
    used tokens are the current request's input, cache-creation and
    cache-read tokens, the documented basis of used_percentage, and are kept
    only when all three are reported.
    """
    if not isinstance(payload, dict):
        return None
    out = {'v': 1, 'event': 'statusline', 'pid': pid, 'ts': now, 'wrapped': wrapped}
    if handover:
        out['handover'] = handover
    session = text(payload.get('session_id'), 128)
    if session:
        out['session_id'] = session
    model = field(payload, 'model', 'id')
    if printable(model, 128):
        out['model'] = model
    effort = field(payload, 'effort', 'level')
    if effort in EFFORTS:
        out['effort'] = effort
    version = printable(payload.get('version'), 32)
    if version:
        out['version'] = version
    window = payload.get('context_window')
    if isinstance(window, dict):
        size = count(window.get('context_window_size'), CONTEXT_TOKENS)
        percent = count(window.get('used_percentage'), 100)
        if size and percent is not None:
            out['ctx_size'], out['ctx_pct'] = size, percent
            usage = window.get('current_usage')
            if isinstance(usage, dict):
                parts = [count(usage.get(k), CONTEXT_TOKENS) for k in
                         ('input_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens')]
                if None not in parts and sum(parts) <= CONTEXT_TOKENS:
                    out['ctx_used'] = sum(parts)
    return out


# agy's documented agent_state values; initializing and authenticating say
# nothing about a turn, so they write no state.
AGY_STATES = {'working': 'working', 'tool_use': 'working', 'reviewing': 'working',
              'idle': 'idle', 'error': 'failed'}
AGY_SETTINGS = '.gemini/antigravity-cli'
AGY_CHAINED = 'yoloctl-statusline.json'


def agy_percent(value):
    """agy's used_percentage, a float in [0, 100], rounded; or None."""
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not 0 <= value <= 100:
        return None
    return int(value + 0.5)


def agy_record(payload, now, pid, wrapped, handover):
    """The kept fields of one agy status line payload, or None.

    Used tokens are current_usage's input, cache-creation and cache-read
    tokens, as for Claude, kept only when all three are reported; a window
    size of 0 is unknown, which leaves the used tokens alone.
    """
    if not isinstance(payload, dict):
        return None
    out = {'v': 1, 'event': 'statusline', 'pid': pid, 'ts': now, 'wrapped': wrapped}
    if handover:
        out['handover'] = handover
    session = payload.get('conversation_id')
    if isinstance(session, str) and UUID.fullmatch(session.lower()):
        out['session_id'] = session.lower()
    model = payload.get('model')
    if isinstance(model, dict):
        if printable(model.get('id'), 128):
            out['model'] = model['id']
        if model.get('effort') in EFFORTS:
            out['effort'] = model['effort']
    version = printable(payload.get('version'), 32)
    if version:
        out['version'] = version
    window = payload.get('context_window')
    if isinstance(window, dict):
        size = count(window.get('context_window_size'), CONTEXT_TOKENS)
        percent = agy_percent(window.get('used_percentage'))
        used = None
        usage = window.get('current_usage')
        if isinstance(usage, dict):
            parts = [count(usage.get(k), CONTEXT_TOKENS) for k in
                     ('input_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens')]
            if None not in parts and sum(parts) <= CONTEXT_TOKENS:
                used = sum(parts)
        if size and percent is not None:
            out['ctx_size'], out['ctx_pct'] = size, percent
            if used is not None:
                out['ctx_used'] = used
        elif size == 0 and used is not None:
            out['ctx_used'] = used
    state = payload.get('agent_state')
    if payload.get('tool_confirmation_pending') is True:
        out['state'] = 'waiting_permission'
    elif isinstance(state, str) and AGY_STATES.get(state) in STATES:
        out['state'] = AGY_STATES[state]
    return out


CODEX_EVENTS = ('SessionStart', 'Stop', 'SessionEnd', 'SubagentStart', 'UserPromptSubmit', 'PostModelRequest',
                'PermissionRequest')
SOURCES = ('startup', 'resume', 'fork', 'clear', 'compact')
UUID = re.compile(r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}')
QUERY_BUDGET = 0.5
DAEMON_DIR = '/tmp/codex-daemon-%d'
DAEMON_SOCKETS = 16
WS_GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11'
WS_MESSAGE = 1 << 20


def codex_record(payload, now, pid):
    """The kept fields of one Codex hook payload, or None.

    A prompt submit means working and a Stop idle; a permission request is a
    state-only line; a model request gives the tokens used, the failed state
    when it failed (never its error text) and the working state when it
    completed, which the controller uses only to clear a failure. A child
    agent's turn and model requests are not the box's, so they write
    nothing."""
    if not isinstance(payload, dict) or payload.get('hook_event_name') not in CODEX_EVENTS:
        return None
    event = payload['hook_event_name']
    subagent = event == 'SubagentStart' or 'agent_id' in payload or 'agent_type' in payload
    if subagent and event in ('UserPromptSubmit', 'PostModelRequest'):
        return None
    out = {'v': 1, 'event': event, 'pid': pid, 'ts': now}
    session = payload.get('session_id')
    if isinstance(session, str) and UUID.fullmatch(session.lower()) and not (subagent and event == 'PermissionRequest'):
        out['session_id'] = session.lower()
    if event == 'PermissionRequest':
        # The main turn waits whichever agent asked.
        out['state'] = 'waiting_permission'
        return out
    if event == 'PostModelRequest':
        usage = payload.get('token_usage')
        used = count(usage.get('total_tokens'), CONTEXT_TOKENS) if isinstance(usage, dict) else None
        if used is not None:
            out['ctx_used'] = used
        status = payload.get('status')
        if status == 'failed':
            out['state'] = 'failed'
        elif status == 'completed':
            out['state'] = 'working'
        return out
    if event == 'SessionStart' and payload.get('source') in SOURCES:
        out['source'] = payload['source']
    if printable(payload.get('model'), 128):
        out['model'] = payload['model']
    if event == 'UserPromptSubmit':
        out['state'] = 'working'
    elif event == 'Stop' and not subagent:
        out['state'] = 'idle'
    if subagent:
        out['subagent'] = True
    return out


class QueryStop(Exception):
    pass


def peer_identity(pid):
    """(start ticks, argv) of pid, or None when unreadable."""
    try:
        with open('/proc/%d/stat' % pid, 'rb') as f:
            fields = f.read(4096).rsplit(b')', 1)[1].split()
        with open('/proc/%d/cmdline' % pid, 'rb') as f:
            argv = f.read(4096).split(b'\0')
        return int(fields[19]), tuple(argv[:3])
    except (OSError, ValueError, IndexError):
        return None


class CodexRead:
    """One finite thread/read over one socket, bounded by deadline."""

    def __init__(self, thread, deadline):
        import socket
        self.socket, self.thread, self.deadline = socket, thread, deadline
        self.sock, self.buf = None, b''

    def remaining(self):
        left = self.deadline - time.monotonic()
        if left <= 0:
            raise QueryStop('deadline')
        return left

    def recv(self):
        self.sock.settimeout(self.remaining())
        try:
            chunk = self.sock.recv(65536)
        except self.socket.timeout:
            raise QueryStop('deadline')
        if not chunk:
            raise QueryStop('closed')
        self.buf += chunk
        if len(self.buf) > WS_MESSAGE + 16:
            raise QueryStop('over bound')

    def exact(self, n):
        while len(self.buf) < n:
            self.recv()
        data, self.buf = self.buf[:n], self.buf[n:]
        return data

    def send(self, opcode, payload):
        import struct
        n = len(payload)
        header = bytes([0x80 | opcode])
        if n < 126:
            header += bytes([0x80 | n])
        elif n < 1 << 16:
            header += bytes([0x80 | 126]) + struct.pack('>H', n)
        else:
            header += bytes([0x80 | 127]) + struct.pack('>Q', n)
        mask = os.urandom(4)
        self.sock.settimeout(self.remaining())
        self.sock.sendall(header + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))

    def message(self):
        """One text message, answering pings; None on close."""
        import struct
        parts = None
        while True:
            b0, b1 = self.exact(2)
            n = b1 & 0x7f
            if b1 & 0x80:
                raise QueryStop('masked server frame')
            if n == 126:
                n = struct.unpack('>H', self.exact(2))[0]
            elif n == 127:
                n = struct.unpack('>Q', self.exact(8))[0]
            if n > WS_MESSAGE:
                raise QueryStop('over bound')
            payload, opcode = self.exact(n), b0 & 0x0f
            if opcode == 0x8:
                return None
            if opcode == 0x9:
                self.send(0xa, payload)
                continue
            if opcode == 0xa:
                continue
            if opcode == 0x1:
                parts = [payload]
            elif opcode == 0x0 and parts is not None:
                parts.append(payload)
            else:
                raise QueryStop('unexpected frame')
            if sum(map(len, parts)) > WS_MESSAGE:
                raise QueryStop('over bound')
            if b0 & 0x80:
                return b''.join(parts).decode('utf-8')

    def call(self, request_id, method, params):
        self.send(0x1, json.dumps({'id': request_id, 'method': method, 'params': params},
                                  separators=(',', ':')).encode())
        while True:
            text = self.message()
            if text is None:
                raise QueryStop('closed')
            msg = json.loads(text)
            # Notifications and server requests are never answered.
            if isinstance(msg, dict) and 'method' not in msg and msg.get('id') == request_id:
                if 'error' in msg:
                    raise QueryStop('rejected')
                return msg.get('result')

    def upgrade(self):
        import base64
        import hashlib
        key = base64.b64encode(os.urandom(16)).decode()
        self.sock.settimeout(self.remaining())
        self.sock.sendall(('GET /rpc HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n'
                           'Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n'
                           % key).encode())
        while b'\r\n\r\n' not in self.buf:
            self.recv()
            if len(self.buf) > 16384:
                raise QueryStop('over bound')
        head, self.buf = self.buf.split(b'\r\n\r\n', 1)
        lines = head.decode('latin-1').split('\r\n')
        accept = base64.b64encode(hashlib.sha1((key + WS_GUID).encode()).digest()).decode()
        headers = {k.strip().lower(): v.strip() for k, _, v in (line.partition(':') for line in lines[1:])}
        if lines[0].split(' ')[1:2] != ['101'] or headers.get('sec-websocket-accept') != accept:
            raise QueryStop('upgrade refused')

    def peer(self):
        import struct
        raw = self.sock.getsockopt(self.socket.SOL_SOCKET, self.socket.SO_PEERCRED, struct.calcsize('3i'))
        pid, uid, _ = struct.unpack('3i', raw)
        ident = peer_identity(pid) if pid > 0 and uid == os.getuid() else None
        argv = ident[1] if ident else ()
        if len(argv) < 2 or os.path.basename(argv[0]) != b'codex' or argv[1] != b'app-server':
            raise QueryStop('peer is not the codex app-server')
        return pid, ident

    def run(self, path):
        """(model, effort, state) when model and effort are current for the
        thread, else None. state is the thread's working, idle or waiting
        state from its status and active flags."""
        self.sock = self.socket.socket(self.socket.AF_UNIX, self.socket.SOCK_STREAM)
        try:
            self.sock.settimeout(self.remaining())
            self.sock.connect(path)
            pid, ident = self.peer()
            self.upgrade()
            self.call('initialize', 'initialize', {
                'clientInfo': {'name': 'yoloctl-agent-observe', 'title': None, 'version': '1'},
                'capabilities': {'experimentalApi': False}})
            self.send(0x1, b'{"method":"initialized"}')
            result = self.call('read-1', 'thread/read', {'threadId': self.thread, 'includeTurns': False})
            if peer_identity(pid) != ident:
                raise QueryStop('peer changed')
        finally:
            self.sock.close()
        thread = result.get('thread') if isinstance(result, dict) else None
        if not isinstance(thread, dict) or thread.get('id') != self.thread:
            return None
        status = thread.get('status')
        model, effort = thread.get('model'), thread.get('reasoningEffort')
        if not isinstance(status, dict) or status.get('type') not in ('idle', 'active'):
            return None
        if not printable(model, 128) or effort not in EFFORTS:
            return None
        flags = status.get('activeFlags') if status.get('type') == 'active' else None
        flags = flags if isinstance(flags, list) else []
        if status.get('type') == 'idle':
            state = 'idle'
        elif 'waitingOnApproval' in flags:
            state = 'waiting_permission'
        elif 'waitingOnUserInput' in flags:
            state = 'waiting_input'
        else:
            state = 'working'
        return model, effort, state


def daemon_sockets(uid):
    """User-owned sockets of the user's own daemon directory, in name order."""
    import stat
    path = DAEMON_DIR % uid
    fd = os.open(path, DIR_FLAGS)
    try:
        st = os.fstat(fd)
        if st.st_uid != uid or stat.S_IMODE(st.st_mode) != 0o700:
            return []
        names = sorted(os.listdir(fd))[:DAEMON_SOCKETS]
        found = []
        for name in names:
            try:
                entry = os.lstat(name, dir_fd=fd)
            except OSError:
                continue
            if stat.S_ISSOCK(entry.st_mode) and entry.st_uid == uid:
                found.append(os.path.join(path, name))
        return found
    finally:
        os.close(fd)


def codex_query(thread):
    """The daemon's current (model, effort, state) for thread, or None."""
    deadline = time.monotonic() + QUERY_BUDGET
    try:
        sockets = daemon_sockets(os.getuid())
    except (OSError, ImportError):
        return None
    for path in sockets:
        try:
            found = CodexRead(thread, deadline).run(path)
        except (QueryStop, OSError, ValueError, UnicodeDecodeError, ImportError):
            found = None
        if found is not None:
            return found
        if time.monotonic() >= deadline:
            return None
    return None


def lock(directory):
    fd = os.open('events.lock', FILE_FLAGS, 0o600, dir_fd=directory)
    deadline = time.monotonic() + LOCK_WAIT
    while True:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fd
        except BlockingIOError:
            if time.monotonic() >= deadline:
                os.close(fd)
                return None
            time.sleep(0.02)


def current(directory):
    indices = [int(m.group(1)) for m in map(EVENTS_FILE.match, os.listdir(directory)) if m]
    return max(indices, default=0)


def append(directory, line):
    """Append line under the lock; at FILE_BYTES start the next file and keep two."""
    k = current(directory)
    fd = os.open(f'events.{k}.jsonl', FILE_FLAGS, 0o600, dir_fd=directory)
    try:
        if os.fstat(fd).st_size + len(line) > FILE_BYTES:
            os.close(fd)
            k += 1
            fd = os.open(f'events.{k}.jsonl', FILE_FLAGS, 0o600, dir_fd=directory)
            if k >= 2:
                try:
                    os.unlink(f'events.{k - 2}.jsonl', dir_fd=directory)
                except FileNotFoundError:
                    pass
        size = os.fstat(fd).st_size
        try:
            written = os.write(fd, line)
        except OSError:
            written = -1
        if written != len(line):
            # A short or failed write (a full tmpfs) must not leave half a line.
            os.ftruncate(fd, size)
    finally:
        os.close(fd)


def write(gen, out, started):
    """Append out, stamped with dur_ms (this run's time up to the write), to
    generation gen's events; every failure drops it silently."""
    try:
        root = os.open(ROOT, DIR_FLAGS)
        try:
            directory = os.open(f'gen-{gen}', DIR_FLAGS, dir_fd=root)
        finally:
            os.close(root)
        try:
            held = lock(directory)
            if held is not None:
                try:
                    out['dur_ms'] = round((time.monotonic() - started) * 1000, 3)
                    line = encode(out)
                    if line is not None:
                        append(directory, line)
                finally:
                    os.close(held)
        finally:
            os.close(directory)
    except OSError:
        pass


def user_command():
    """statusLine.command of type command in the user-scope settings file,
    or None for anything else."""
    base = os.environ.get('CLAUDE_CONFIG_DIR', '')
    if not base.startswith('/'):
        home = os.environ.get('HOME', '')
        if not home.startswith('/'):
            return None
        base = os.path.join(home, '.claude')
    settings = read_settings(os.path.join(base, 'settings.json'))
    line = settings.get('statusLine') if settings is not None else None
    if not isinstance(line, dict) or line.get('type') != 'command':
        return None
    command = line.get('command')
    if not isinstance(command, str) or not command.strip() or len(command) > COMMAND_BYTES or '\0' in command:
        return None
    return command


def read_settings(path):
    """A JSON settings file as a dict, or None; a FIFO, a non-regular file or
    an oversized one is no settings file."""
    try:
        # O_NONBLOCK: a FIFO in place of the file cannot hang the status line.
        fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_CLOEXEC)
        try:
            if os.fstat(fd).st_mode & 0o170000 != 0o100000:
                return None
            raw = os.read(fd, SETTINGS_BYTES + 1)
        finally:
            os.close(fd)
        settings = json.loads(raw) if len(raw) <= SETTINGS_BYTES else None
    except (OSError, ValueError):
        return None
    return settings if isinstance(settings, dict) else None


def chained_command(line):
    """The command of a statusLine object that runs one, or None."""
    if not isinstance(line, dict) or line.get('type', 'command') != 'command' or line.get('enabled', True) is not True:
        return None
    command = line.get('command')
    if not isinstance(command, str) or not command.strip() or len(command) > COMMAND_BYTES or '\0' in command:
        return None
    return command


def agy_command():
    """The status line command `yoloctl account observe` replaced, or None."""
    home = os.environ.get('HOME', '')
    if not home.startswith('/'):
        return None
    saved = read_settings(os.path.join(home, AGY_SETTINGS, AGY_CHAINED))
    return chained_command(saved.get('previous')) if saved is not None and saved.get('v') == 1 else None


def write_all(fd, data):
    view = memoryview(data)
    while view:
        n = os.write(fd, view)
        if n <= 0:
            raise OSError('short write')
        view = view[n:]


def stage(raw):
    """A readable fd at the start of raw for the user's command, and None; or
    None and the reason it could not be staged. An unnamed O_TMPFILE in /tmp
    first (memfd_create and /dev/shm are refused in the confined box), then a
    pipe when raw fits its buffer."""
    try:
        fd = os.open(TMP, os.O_TMPFILE | os.O_RDWR | os.O_CLOEXEC, 0o600)
    except (OSError, AttributeError):
        fd = None
    if fd is not None:
        try:
            write_all(fd, raw)
            os.lseek(fd, 0, os.SEEK_SET)
            return fd, None
        except OSError:
            os.close(fd)
    if len(raw) >= PIPE_BYTES:
        return None, 'too-large'
    try:
        r, w = os.pipe()
    except OSError:
        return None, 'unavailable'
    try:
        # Non-blocking: a pipe shrunk by the user's pipe quota fails instead
        # of hanging the status line.
        os.set_blocking(w, False)
        write_all(w, raw)
    except OSError:
        os.close(r)
        return None, 'unavailable'
    finally:
        os.close(w)
    return r, None


def hook(started):
    gen = generation()
    if gen is None:
        return 0
    try:
        payload = json.loads(sys.stdin.buffer.read(PAYLOAD_BYTES + 1)[:PAYLOAD_BYTES])
    except (ValueError, OSError):
        return 0
    out = record(payload, time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime()) + 'Z', os.getppid())
    if out is not None:
        write(gen, out, started)
    return 0


def codex_hook(started):
    gen = generation()
    if gen is None:
        return 0
    try:
        payload = json.loads(sys.stdin.buffer.read(PAYLOAD_BYTES + 1)[:PAYLOAD_BYTES])
    except (ValueError, OSError):
        return 0
    now = time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime()) + 'Z'
    out = codex_record(payload, now, os.getppid())
    if out is None:
        return 0
    write(gen, dict(out), started)
    if out['event'] in ('SessionStart', 'Stop') and not out.get('subagent') and 'session_id' in out:
        found = codex_query(out['session_id'])
        if found is not None:
            # Labelled with the thread it was read for: the reader applies it
            # only while that thread is the box's current conversation.
            line = {'v': 1, 'event': 'query', 'pid': out['pid'], 'ts': now, 'session_id': out['session_id'],
                    'model': found[0], 'effort': found[1]}
            # The daemon may still report a finishing turn as active: the Stop
            # hook's idle stays authoritative.
            if out['event'] == 'SessionStart':
                line['state'] = found[2]
            write(gen, line, started)
    return 0


GEN_ARG = re.compile(r'[1-9][0-9]{0,19}')


def codex_query_mode(args, started):
    """The controller's refresh read: `codex-query GEN THREAD` asks the box's
    daemon for exactly that thread and appends a query line to generation GEN,
    an existing controller-created directory. Invalid arguments are refused
    (exit 2); everything else, including no daemon, writes nothing and exits 0."""
    if len(args) != 2 or not GEN_ARG.fullmatch(args[0]) or int(args[0]) >= 1 << 64 or not UUID.fullmatch(args[1]):
        return 2
    gen, thread = args
    found = codex_query(thread)
    if found is not None:
        write(gen, {'v': 1, 'event': 'query', 'pid': os.getpid(),
                    'ts': time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime()) + 'Z', 'session_id': thread,
                    'model': found[0], 'effort': found[1], 'state': found[2]}, started)
    return 0


def statusline(started, record=statusline_record, find_command=user_command):
    if WRAPPED in os.environ:
        return 0
    try:
        raw = sys.stdin.buffer.read(PAYLOAD_BYTES + 1)
    except OSError:
        return 0
    if len(raw) > PAYLOAD_BYTES:
        return 0
    command = find_command()
    fd, handover = stage(raw) if command is not None else (None, None)
    gen = generation()
    if gen is not None:
        try:
            payload = json.loads(raw)
        except ValueError:
            payload = None
        out = record(payload, time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime()) + 'Z',
                                os.getppid(), fd is not None, handover)
        if out is not None:
            write(gen, out, started)
    if fd is None:
        return 0
    sys.stdout.flush()
    os.dup2(fd, 0)
    os.close(fd)
    env = dict(os.environ)
    env[WRAPPED] = '1'
    try:
        os.execve(SHELL, ['sh', '-c', command], env)
    except OSError:
        pass
    return 0


def main(argv=None):
    started = time.monotonic()
    args = sys.argv[1:] if argv is None else argv
    if args == ['statusline']:
        return statusline(started)
    if args == ['codex']:
        return codex_hook(started)
    if args == ['agy']:
        return statusline(started, agy_record, agy_command)
    if args[:1] == ['codex-query']:
        return codex_query_mode(args[1:], started)
    return hook(started)


if __name__ == '__main__':
    sys.exit(main())
