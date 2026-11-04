#!/usr/bin/env python3
"""Drive real TUI/managed/passthrough PTYs in an already isolated smoke space.

Raw output goes only to capture files, never to the caller's terminal. The guest
audit, CLI statuses and receive progress establish transport behavior; this is
not a graphical terminal parity test or a terminal emulator implemented in Python.
"""

import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import shlex
import signal
import struct
import subprocess
import sys
import termios
import time
import uuid


class Failure(RuntimeError):
    pass


def read_pty(fd):
    try:
        return os.read(fd, 65536)
    except OSError as error:
        if error.errno == errno.EIO:  # Linux closed slave; Darwin returns EOF.
            return b""
        raise


class View:
    def __init__(self, command, output, rows=30, cols=100):
        self.capture = open(output, "wb", buffering=0)
        self.tail = b""
        self.received = 0
        self.reading = True
        self.eof = False
        self.status = None
        try:
            self.pid, self.master = pty.fork()
        except BaseException:
            self.capture.close()
            raise
        if self.pid == 0:
            try:
                env = dict(os.environ, TERM="xterm-256color")
                env.pop("YOLOCTL_TUI", None)
                fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
                os.execvpe(command[0], command, env)
            except BaseException as error:
                os.write(2, f"exec failed: {error!r}\n".encode())
                os._exit(127)
        try:
            os.set_blocking(self.master, False)
            self.resize(rows, cols)
        except BaseException:
            self.close()
            raise

    def resize(self, rows, cols):
        fcntl.ioctl(self.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def read(self):
        try:
            data = read_pty(self.master)
        except BlockingIOError:
            return
        if not data:
            self.eof = True
            return
        self.capture.write(data)
        self.tail = (self.tail + data)[-65536:]
        self.received += len(data)
        if self.received > 192 * 1024 * 1024:
            raise Failure("view exceeded capture budget")

    def poll(self):
        if self.status is None:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.status = os.waitstatus_to_exitcode(status)
        return self.status

    def terminate(self):
        if self.poll() is None:
            try:
                os.killpg(self.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass

    def close(self):
        try:
            if self.poll() is None:
                try:
                    os.killpg(self.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                _, status = os.waitpid(self.pid, 0)
                self.status = os.waitstatus_to_exitcode(status)
        finally:
            os.close(self.master)
            self.capture.close()


def audit_events(data):
    # A reader can see an unfinished final write. Only complete records count.
    events = [json.loads(line) for line in data.split(b"\n")[:-1]]
    errors = [e for e in events if e["event"] == "error"]
    if errors:
        raise Failure(f"guest probe failed: {errors}")
    return events


def validate_audit(events, expected, final=False, queries_sent=None):
    actual = "".join(e["key"] for e in events if e["event"] == "input")
    if any(c not in "".join(expected) for c in actual):
        raise Failure(f"unexpected input in audit: {actual!r}")
    for alphabet, sent in expected.items():
        received = "".join(c for c in actual if c in alphabet)
        if not sent.startswith(received) or final and received != sent:
            raise Failure(f"input changed for {alphabet}: received={received!r}, sent={sent!r}")
    queries = sum(e["event"] == "query" for e in events)
    if queries_sent is not None and (queries > queries_sent or final and queries != queries_sent):
        raise Failure(f"query commands={queries}, sent={queries_sent}")
    replies = [e for e in events if e["event"] == "reply"]
    if len(replies) > queries or final and len(replies) != queries:
        raise Failure(f"cursor replies={len(replies)}, queries={queries}")
    if any((e["row"], e["col"]) != (2, 3) for e in replies):
        raise Failure(f"cursor reply changed: {replies}")
    if final and not any(e["event"] == "done" for e in events):
        raise Failure("guest did not finish")


def prepare_python_probe(exec_prefix, source, *arguments, env=None, directory="/tmp"):
    # Transfer source/argv/env over pipes, through confined box exec in a live
    # smoke. Only a short launch command goes through the terminal. Splitting
    # a large paste into lines still leaves a large typeahead burst for Bash
    # and the line discipline to handle across Readline mode transitions.
    payload = json.dumps([source.decode("utf-8"), arguments, env or {}])
    program = ("import json,os,sys\nsource,args,env=json.loads(" + repr(payload) +
               ")\nsys.argv[1:]=args\nos.environ.update(env)\nexec(source)\n")
    installer = '''import os,sys,tempfile
assert not os.isatty(0) and not os.isatty(1), "probe installation requires pipes"
fd,path=tempfile.mkstemp(prefix="yoloctl-smoke-probe-", suffix=".py", dir=sys.argv[1])
with os.fdopen(fd, "wb") as output:
    output.write(sys.stdin.buffer.read())
print(path)
'''
    try:
        result = subprocess.run([*exec_prefix, "python3", "-c", installer, directory],
                                input=program.encode("utf-8"), capture_output=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise Failure(f"prepare probe: {error}") from error
    if result.returncode:
        raise Failure(f"prepare probe exited {result.returncode}: {result.stderr[-2000:]!r}")
    path = result.stdout.decode("utf-8").rstrip("\n")
    if not os.path.isabs(path) or any(ord(c) < 32 for c in path):
        raise Failure(f"prepare probe returned an invalid path: {result.stdout!r}")
    command = shlex.join(["exec", "python3", "-u", path]).encode("utf-8") + b"\r"
    if len(command) >= 255:
        raise Failure("probe launch exceeds the terminal input budget")
    return command


class Driver:
    def __init__(self, yoloctl, space, box, output):
        self.yoloctl, self.space, self.box = yoloctl, space, box
        self.output = Path(output)
        self.output.mkdir(parents=True, exist_ok=True)
        self.views = []
        self.expected = {"abc": "", "DEF": "", "123": ""}
        self.queries = 0
        self.token = uuid.uuid4().hex
        self.audit_path = f"/workdir/attach-audit-{self.token}.jsonl"
        self.attach = [yoloctl, "box", "attach", box, "--space", space, "--no-autoupdate"]

    def start(self, name, command):
        view = View(command, self.output / f"{name}.raw")
        self.views.append(view)
        return view

    def prepare_probe(self, source, *arguments, env=None):
        return prepare_python_probe(
            [self.yoloctl, "box", "exec", self.box, "--space", self.space, "--"],
            source, *arguments, env=env)

    def pump(self, delay=0.05):
        readers = [v.master for v in self.views if v.reading and not v.eof]
        ready, _, _ = select.select(readers, [], [], delay)
        for view in self.views:
            if view.master in ready:
                view.read()
            view.poll()

    def wait(self, description, predicate, timeout=30):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.pump()
            if predicate():
                return
        state = [(v.pid, v.status, v.tail[-400:]) for v in self.views]
        raise Failure(f"timeout waiting for {description}: {state!r}")

    def send(self, view, data):
        remaining = memoryview(data)
        deadline = time.monotonic() + 10
        while remaining:
            if view.poll() is not None:
                raise Failure(f"input view exited {view.status}")
            if time.monotonic() >= deadline:
                raise Failure("host PTY input stalled")
            try:
                remaining = remaining[os.write(view.master, remaining):]
            except BlockingIOError:
                self.pump()

    def input(self, view, alphabet):
        payload = alphabet * 8
        self.expected[alphabet] += payload
        self.send(view, payload.encode("ascii"))

    def query(self, view):
        self.queries += 1
        self.send(view, b"?")

    def start_flood(self, view, timeout=45):
        self.send(view, b"+")
        # Stdin and resize use independent transport paths. Writing '+' on
        # the host does not mean the guest processed it: wait for its audit
        # before sending a resize that must occur while flooding is enabled.
        self.wait_audit("guest flood acknowledgment", lambda es: any(
            e["event"] == "flood-start" for e in es), timeout=timeout)

    def check_live_rejoin(self, view, alphabet, timeout=30):
        # A managed renderer emits screen differences: even the unchanged
        # digits in a flood row can be split by cursor/style controls. A raw
        # substring is not evidence that the restored view is ready. Fence on
        # guest-confirmed input/query delivery, then require receive progress
        # after that fence instead of accepting already captured setup bytes.
        self.input(view, alphabet)
        self.query(view)
        self.wait_audit("rejoined viewer input and query reply", lambda es:
            sum(e["event"] == "input" for e in es) == sum(map(len, self.expected.values())) and
            sum(e["event"] == "reply" for e in es) == self.queries, timeout=timeout)
        before = view.received
        self.wait("rejoined viewer live output", lambda:
            view.status is not None or view.received > before, timeout=timeout)
        if view.status is not None:
            raise Failure(f"rejoined viewer exited {view.status}")

    def audit(self):
        command = [self.yoloctl, "box", "exec", self.box, "--space", self.space,
                   "--", "cat", self.audit_path]
        child = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                 stderr=subprocess.PIPE, start_new_session=True)
        try:
            # Drain PTY views while RPC completes; blocking here would simulate
            # three stalled terminals instead of the one deliberately paused.
            streams = {child.stdout.fileno(): bytearray(), child.stderr.fileno(): bytearray()}
            remaining = set(streams)
            deadline = time.monotonic() + 30
            while remaining or child.poll() is None:
                if time.monotonic() >= deadline:
                    raise Failure("audit RPC timed out")
                self.pump(0.01)
                ready, _, _ = select.select(list(remaining), [], [], 0)
                for fd in ready:
                    data = os.read(fd, 65536)
                    if not data:
                        remaining.remove(fd)
                    streams[fd].extend(data)
                    if len(streams[fd]) > 1024 * 1024:
                        raise Failure("guest audit exceeded its budget")
            out, err = (bytes(streams[fd]) for fd in (child.stdout.fileno(), child.stderr.fileno()))
            if child.returncode:
                raise Failure(f"read guest audit: {err!r}")
            (self.output / "audit.jsonl").write_bytes(out)
            events = audit_events(out)
            validate_audit(events, self.expected, queries_sent=self.queries)
            return events
        finally:
            try:
                if child.poll() is None:
                    try:
                        os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                child.wait(timeout=5)
            finally:
                child.stdout.close()
                child.stderr.close()

    def wait_audit(self, description, predicate, timeout=45):
        deadline = time.monotonic() + timeout
        events = []
        while time.monotonic() < deadline:
            events = self.audit()
            if predicate(events):
                return events
            until = time.monotonic() + 0.25
            while time.monotonic() < until:
                self.pump()
        controls = [e for e in events if e["event"] not in ("input", "progress")]
        counts = {alphabet: sum(e["event"] == "input" and e["key"] in alphabet for e in events)
                  for alphabet in self.expected}
        raise Failure(f"timeout waiting for {description}; recent control audit={controls[-10:]!r}; "
                      f"input counts={counts}; inspect {self.output}/audit.jsonl")

    def detach(self, view, clean=False):
        if clean:
            self.send(view, b"\x19\x17")  # default standalone prefix, Ctrl+W
        else:
            view.terminate()
        view.reading = True
        self.wait("viewer detach", lambda: view.status is not None, timeout=15)
        if clean and view.status != 0:
            raise Failure(f"clean detach returned {view.status}")

    def run(self):
        command = self.prepare_probe(Path(__file__).with_name("attach-probe.py").read_bytes(),
                                     self.audit_path, self.token)
        managed = self.start("managed", self.attach)
        self.wait("shell prompt", lambda: b"sandbox@" in managed.tail, timeout=60)
        self.send(managed, command)
        ready = b"MIXED_READY_" + self.token.encode("ascii")
        self.wait("guest probe", lambda: ready in managed.tail)

        tui = self.start("tui", [self.yoloctl])
        self.wait("palette", lambda: b"yoloctl" in tui.tail)
        palette = f"box attach {self.box} --space {self.space} --no-autoupdate\r"
        self.send(tui, palette.encode("ascii"))
        self.wait("TUI snapshot", lambda: ready in tui.tail)
        raw = self.start("passthrough", self.attach + ["--passthrough"])
        self.wait("passthrough replay", lambda: ready in raw.tail)

        print(">>> mixed attach: all three frontends interactive; output and resize stress", flush=True)
        self.start_flood(managed)
        # Admit input from all views without a fence between senders. Audit
        # each stream's order independently of interleaving in the guest.
        for view, alphabet in [(managed, "abc"), (tui, "DEF"), (raw, "123")]:
            self.input(view, alphabet)
        self.wait_audit("initial input from all three viewers", lambda es:
            sum(e["event"] == "input" for e in es) == sum(map(len, self.expected.values())))
        for view, alphabet, rows, cols in [(managed, "abc", 31, 92),
                                         (tui, "DEF", 27, 84), (raw, "123", 33, 108)]:
            self.input(view, alphabet)
            view.resize(rows, cols)
            # The full TUI reserves two chrome rows; standalone views use all.
            guest_rows = rows - 2 if view is tui else rows
            self.wait_audit(f"{Path(view.capture.name).stem} resize to {cols}x{guest_rows} during output", lambda es: any(
                e["event"] == "resize" and e["rows"] == guest_rows and
                e["cols"] == cols and e["flooding"] for e in es))
            self.query(view)

        print(">>> mixed attach: detach/rejoin managed view during output", flush=True)
        self.detach(managed, clean=True)
        managed = self.start("managed-rejoined", self.attach)
        self.check_live_rejoin(managed, "abc")

        print(">>> mixed attach: pause passthrough reader; healthy viewers must progress", flush=True)
        raw.reading = False
        events = self.audit()
        baseline = max((e["bytes"] for e in events if e["event"] == "progress"), default=0)
        progress = {v: v.received for v in (managed, tui)}
        # Account for progress-record granularity and the pending chunk. More
        # than 8 MiB is produced after pausing. We assert healthy progress with
        # an unread viewer; exact slow-client disconnection is tested separately
        # at the holder, without depending on OS/SSH buffering here.
        self.wait_audit("backpressure output budget", lambda es: any(
            e["event"] == "progress" and e["bytes"] >= baseline + 10 * 1024 * 1024 for e in es))
        self.input(managed, "abc")
        self.input(tui, "DEF")
        self.query(tui)
        self.wait_audit("healthy input and query replies with unread viewer", lambda es:
            sum(e["event"] == "input" for e in es) == sum(map(len, self.expected.values())) and
            sum(e["event"] == "reply" for e in es) == 5)
        # Keep the reader paused across more than the holder's output budget.
        self.send(managed, b"-")
        events = self.wait_audit("output fence", lambda es: any(e["event"] == "flood-stop" for e in es))
        for view, before in progress.items():
            if view.status is not None or view.received <= before:
                raise Failure("healthy viewer stalled or exited with unread peer")
        self.detach(raw)
        raw = self.start("passthrough-rejoined", self.attach + ["--passthrough"])
        self.wait("passthrough rejoin output", lambda: b"0123456789" in raw.tail)
        self.input(raw, "123")
        self.query(raw)
        self.wait_audit("rejoined input and exactly one reply per query", lambda es:
            sum(e["event"] == "input" for e in es) == sum(map(len, self.expected.values())) and
            sum(e["event"] == "reply" for e in es) == 6)

        self.send(managed, b"!")
        self.wait("agent exit in standalone viewers", lambda:
                  managed.status is not None and raw.status is not None)
        if (managed.status, raw.status) != (7, 7):
            raise Failure(f"exit statuses changed: managed={managed.status}, raw={raw.status}")
        # Refresh before checking presentation, so a display failure retains
        # the completed guest audit instead of the last pre-exit sample.
        events = self.audit()
        validate_audit(events, self.expected, final=True, queries_sent=self.queries)
        self.wait("TUI exit status", lambda: b"exit 7" in tui.tail or b"exited with status 7" in tui.tail)
        if tui.status is not None:
            raise Failure(f"TUI exited with its tab: {tui.status}")
        if not any(e["event"] == "input" and e["flooding"] for e in events):
            raise Failure("input did not overlap output")
        for path in self.output.glob("passthrough*.raw"):
            if b"\x1b[6n" in path.read_bytes():
                raise Failure("passthrough leaked a query to the host terminal")
        self.detach(tui)
        print(">>> PASS: simultaneous TUI, managed and passthrough input, resize, rejoin and exit", flush=True)

    def close(self):
        for view in self.views:
            view.reading = True
            view.terminate()
        deadline = time.monotonic() + 5
        try:
            while time.monotonic() < deadline and any(v.poll() is None for v in self.views):
                self.pump()
        finally:
            for view in self.views:
                view.close()
            self.views.clear()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--yoloctl", required=True)
    parser.add_argument("--space", required=True)
    parser.add_argument("--box", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    driver = Driver(str(Path(args.yoloctl).resolve()), args.space, args.box, args.output)

    def stop(signum, _frame):
        raise Failure(f"interrupted by signal {signum}")

    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, stop)
    try:
        driver.run()
        return 0
    except (Failure, OSError) as error:
        print(f"FAIL: {error}; captures: {driver.output}", file=sys.stderr)
        # The enclosing smoke may remove its fixture. Leave bounded, escaped
        # evidence in the log even when the user did not request preservation.
        for view in driver.views:
            print(f"  {Path(view.capture.name).name}: status={view.status}, "
                  f"bytes={view.received}, tail={view.tail[-300:]!r}", file=sys.stderr)
        audit = driver.output / "audit.jsonl"
        if audit.exists():
            print(f"  guest audit tail: {audit.read_bytes()[-2400:]!r}", file=sys.stderr)
        return 1
    finally:
        driver.close()


if __name__ == "__main__":
    sys.exit(main())
