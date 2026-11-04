#!/usr/bin/env python3
"""Check a disposable privileged box's holder through its canonical screen.

The shared box-use client owns transport/correlation/cleanup. Capture its raw
stdout and stderr, and require observed output before detaching; command echo
and a frontend that exits before observation cannot satisfy this gate.
"""
import argparse
import importlib.util
import json
from pathlib import Path
import time
import uuid

spec = importlib.util.spec_from_file_location(
    "privileged_box_use", Path(__file__).with_name("box-use.py")
)
box_use = importlib.util.module_from_spec(spec)
spec.loader.exec_module(box_use)


class RecordedClient(box_use.Client):
    def __init__(self, command, output, timeout):
        self.output = output
        self.deadline = time.monotonic() + timeout
        super().__init__(command, output / "holder.stderr", self.remaining)

    def remaining(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("managed-holder observation deadline expired")
        return remaining

    def _read(self):
        # Same framing as the shared client, with raw capture before decoding
        # so even a malformed reply survives a failed host run.
        try:
            with (self.output / "holder.stdout.jsonl").open("w") as capture:
                for line in self.process.stdout:
                    capture.write(line)
                    capture.flush()
                    value = json.loads(line)
                    if not isinstance(value, dict):
                        raise ValueError("box-use response is not an object")
                    self.messages.put(value)
        except (ValueError, OSError) as error:
            self.messages.put(error)
        finally:
            self.messages.put(EOFError("box-use stdout closed"))

    def receive(self, timeout=60):
        return super().receive(min(timeout, self.remaining()))

    def request(self, op, **fields):
        self.remaining()
        with (self.output / "holder.requests.jsonl").open("a") as capture:
            capture.write(json.dumps(dict(id=str(self.counter + 1), op=op, **fields)) + "\n")
        return super().request(op, **fields)


def expect(reply, kind):
    if reply.get("type") != kind:
        raise AssertionError(f"expected {kind}: {reply}")
    return reply


def run_session(command, output, timeout=60):
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    (output / "holder.requests.jsonl").write_text("")
    # Separate invocations must not accidentally inspect old output or match
    # their own typed command. The complete marker is constructed by the shell.
    token = uuid.uuid4().hex
    marker = "privileged-" + token
    shell = "printf 'privileged-%s\\n' " + token
    assert marker not in shell
    report = {"status": "FAIL", "marker": marker}
    client = None
    try:
        client = RecordedClient(command, output, timeout)
        ready = expect(client.receive(), "ready")
        if ready.get("version") != 1 or not isinstance(ready.get("session"), str) or not ready["session"]:
            raise AssertionError(f"invalid ready: {ready}")
        report["session"] = ready["session"]
        expect(client.request("text", text=shell), "sent")
        expect(client.request("key", key="enter"), "sent")
        while True:
            screen = expect(client.request("screen"), "screen").get("screen")
            if (not isinstance(screen, dict) or screen.get("full") is not True
                    or type(screen.get("revision")) is not int or screen["revision"] < 1
                    or not isinstance(screen.get("lines"), list)
                    or not all(isinstance(line, str) for line in screen["lines"])):
                raise AssertionError(f"invalid full screen: {screen}")
            if marker in screen["lines"]:
                report["observed_revision"] = screen["revision"]
                break
            wait_ms = max(1, min(500, int(client.remaining() * 1000)))
            expect(client.request("wait", since=screen["revision"], timeout_ms=wait_ms, settle_ms=50), "wait")
        expect(client.request("detach"), "detached")
        status = client.process.wait(timeout=client.remaining())
        report["frontend_status"] = status
        if status != 0:
            raise AssertionError(f"detach frontend exited {status}")
        report["status"] = "PASS"
        return report
    except Exception as error:
        report["error"] = f"{type(error).__name__}: {error}"
        raise
    finally:
        try:
            if client is not None:
                client.close()
        finally:
            (output / "holder-result.json").write_text(json.dumps(report, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ("yoloctl", "space", "box", "output"):
        parser.add_argument("--" + field, required=True)
    args = parser.parse_args()
    run_session([args.yoloctl, "box", "attach", args.box, "--space", args.space,
                 "--box-use", "--no-autoupdate"], args.output)
    print("PASS: managed holder input/output observed before clean detach")


if __name__ == "__main__":
    main()
