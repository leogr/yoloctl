#!/usr/bin/env python3
"""Exercise collaborative box-use in the attach smoke's disposable space.

Uses a real passthrough PTY, two JSON frontends and a finite CLI batch. Captures stay in files;
assertions use runtime-built shell markers, never command echo as success.
"""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import queue
import select
import subprocess
import threading
import time

spec = importlib.util.spec_from_file_location(
    "attach_mixed", Path(__file__).with_name("attach-mixed.py")
)
mixed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mixed)


class Client:
    def __init__(self, command, output, pump=lambda: None):
        self.pump = pump
        self.counter = 0
        self.messages = queue.Queue()
        self.log = open(output, "wb")
        self.process = subprocess.Popen(
            command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=self.log, text=True, bufsize=1,
        )
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()

    def _read(self):
        try:
            for line in self.process.stdout:
                self.messages.put(json.loads(line))
        except (ValueError, OSError) as error:
            self.messages.put(error)
        finally:
            self.messages.put(EOFError("box-use stdout closed"))

    def receive(self, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.pump()
            try:
                result = self.messages.get(timeout=min(.05, max(0, deadline-time.monotonic())))
            except queue.Empty:
                continue
            if isinstance(result, BaseException):
                raise result
            if result.get("type") in ("error", "input_uncertain", "disconnected"):
                raise RuntimeError(result)
            return result
        raise TimeoutError("box-use response timeout")

    def request(self, op, **fields):
        self.counter += 1
        identity = str(self.counter)
        self.process.stdin.write(json.dumps(dict(id=identity, op=op, **fields))+"\n")
        self.process.stdin.flush()
        response = self.receive()
        if response.get("id") != identity:
            raise RuntimeError(f"unexpected response: {response}")
        return response

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.reader.join(timeout=5)
        self.process.stdin.close()
        self.process.stdout.close()
        self.log.close()


def run(args):
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    command = [args.yoloctl, "box", "attach", args.box, "--space", args.space, "--no-autoupdate"]
    peer = mixed.View(command+["--passthrough"], output / "peer.raw", rows=30, cols=100)
    clients = []

    def pump():
        if select.select([peer.master], [], [], 0)[0]:
            peer.read()

    def peer_marker(marker):
        deadline = time.monotonic()+60
        while marker not in peer.tail:
            pump()
            if peer.eof or time.monotonic() > deadline:
                raise RuntimeError(f"peer did not display {marker!r}")
            time.sleep(.01)

    def attach(name):
        client = Client(command+["--box-use"], output / f"{name}.stderr", pump)
        clients.append(client)
        ready = client.receive()
        if ready.get("type") != "ready" or ready.get("version") != 1:
            raise RuntimeError(f"invalid ready: {ready}")
        return client, ready

    def marker(client, text):
        deadline = time.monotonic()+30
        while time.monotonic() < deadline:
            screen = client.request("screen")["screen"]
            if text in screen.get("lines", []):
                return screen
            client.request("wait", since=screen["revision"], timeout_ms=1000, settle_ms=100)
        raise RuntimeError(f"structured view did not display {text}")

    def shell(client, text):
        client.request("text", text=text)
        client.request("key", key="enter")

    try:
        peer_marker(b"sandbox@")
        first, ready = attach("first")
        screen = first.request("screen")["screen"]
        if (screen["rows"], screen["cols"]) != (30, 100):
            raise RuntimeError("box-use changed shared geometry")
        shell(first, "printf 'BOXUSE_%s\\n' SHARED")
        marker(first, "BOXUSE_SHARED")
        peer_marker(b"BOXUSE_SHARED")

        os.write(peer.master, b"printf 'PEER_%s\\n' STEER\r")
        steered = marker(first, "PEER_STEER")
        if not steered["input"]["peer_revision"] or steered["input"]["last_peer"] == ready["client"]:
            raise RuntimeError("peer steering did not report activity")

        second, second_ready = attach("second")
        if second_ready["session"] != ready["session"]:
            raise RuntimeError("second frontend joined a different session")
        shell(second, "printf 'AGENT_%s\\n' PEER")
        collaborated = marker(first, "AGENT_PEER")
        if collaborated["input"]["last_peer"] != second_ready["client"]:
            raise RuntimeError("agent peer activity missing")
        peer_marker(b"AGENT_PEER")

        # The shell/session belongs to this disposable attach-smoke fixture.
        # Drive the finite convenience while all existing viewers stay attached.
        actions = [
            dict(id="before", op="screen"),
            dict(id="prompt", op="text", text="printf 'FINITE_%s\\n' BATCH"),
            dict(id="submit", op="key", key="enter"),
            dict(id="settle", op="wait", timeout_ms=3000, settle_ms=100),
            dict(id="after", op="screen"),
        ]
        with (open(output / "finite.stderr", "w") as diagnostics,
              open(output / "finite.jsonl", "w") as transcript):
            subprocess.run(
                [args.yoloctl, "box", "use", args.box, "batch", "--stdin",
                 "--space", args.space, "--timeout", "20s"],
                input="".join(json.dumps(action)+"\n" for action in actions),
                stdout=transcript, stderr=diagnostics, text=True,
                timeout=40, check=True,
            )
        finite = validate_finite_batch((output / "finite.jsonl").read_text(), ready, second_ready)
        observed = marker(first, "FINITE_BATCH")
        if observed["input"]["last_peer"] != finite["client"]:
            raise RuntimeError("finite batch did not share session input activity")
        peer_marker(b"FINITE_BATCH")
        for client in (second, first):
            client.request("detach")
            if client.process.wait(timeout=10) != 0:
                raise RuntimeError("detach exited unsuccessfully")

        os.write(peer.master, b"printf 'AFTER_%s\\n' DETACH\r")
        peer_marker(b"AFTER_DETACH")
        os.write(peer.master, b"exit 7\r")
        deadline = time.monotonic()+10
        while peer.poll() is None and time.monotonic() < deadline:
            pump()
            time.sleep(.01)
        if peer.poll() != 7:
            raise RuntimeError("remaining peer lost session exit status")
        print("PASS: shared box-use input, finite CLI batch, peer steering, geometry and detach")
    finally:
        for client in clients:
            client.close()
        peer.close()


def validate_finite_batch(output, first, second):
    records = [json.loads(line) for line in output.splitlines()]
    if len(records) != 7:
        raise RuntimeError(f"finite batch returned {len(records)} records, expected 7")
    ready = records[0]
    if (ready.get("type") != "ready" or ready.get("version") != 1
            or ready.get("session") != first["session"]
            or not ready.get("client")
            or ready["client"] in (first["client"], second["client"])):
        raise RuntimeError("finite batch lost shared session or distinct attachment identity")
    expected = [("before", "screen"), ("prompt", "sent"), ("submit", "sent"),
                ("settle", "wait"), ("after", "screen")]
    if [(r.get("id"), r.get("type")) for r in records[1:-1]] != expected:
        raise RuntimeError("finite batch lost ordered/correlated results")
    for record in (records[1], records[4], records[5]):
        screen = record.get("screen", {})
        if ((screen.get("rows"), screen.get("cols")) != (30, 100)
                or not screen.get("revision") or "input" not in screen):
            raise RuntimeError("finite batch lost screen geometry/revision/peer metadata")
    if records[-1].get("type") != "detached":
        raise RuntimeError("finite batch did not confirm detach")
    return ready


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ("yoloctl", "space", "box", "output"):
        parser.add_argument("--"+flag, required=True)
    run(parser.parse_args())
