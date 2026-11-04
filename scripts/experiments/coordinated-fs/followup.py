#!/usr/bin/env python3
"""Four bounded host controls using the already-built synthetic two-guest stack."""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid

HERE = Path(__file__).resolve().parent
MODES = ("mapped-resize", "coordinator-loss", "stopped-peer", "large-append")


def wait_for(predicate, seconds, label):
    deadline = time.monotonic() + seconds
    while not predicate():
        if time.monotonic() >= deadline:
            raise TimeoutError(label)
        time.sleep(0.02)


def exclusive_available(path):
    with path.open("rb") as stream:
        try:
            fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return False
        return True


def append_order(actual, first, second):
    if len(actual) != len(first) + len(second):
        raise AssertionError("large append lost or added bytes")
    if actual == first + second:
        return "a-then-b"
    if actual == second + first:
        return "b-then-a"
    # Distinguish interleaving from missing, duplicated or changed payload.
    # This control uses one distinct byte per record and one syscall per write.
    if actual.count(first[:1]) != len(first) or actual.count(second[:1]) != len(second):
        raise AssertionError("large append changed payload bytes")
    return "interleaved"


def parallel_writes(peers, payloads):
    results = queue.Queue()
    start = threading.Barrier(2)

    def write(peer, data):
        try:
            start.wait(timeout=5)
            count = peer.call("write", handle="large-append", hex=data.hex())
            results.put((peer.name, count, None))
        except Exception as error:
            results.put((peer.name, None, repr(error)))

    for peer, data in zip(peers, payloads):
        threading.Thread(target=write, args=(peer, data), daemon=True).start()
    counts = {}
    for _ in peers:
        name, count, error = results.get(timeout=30)
        if error:
            raise RuntimeError(name + ": " + error)
        counts[name] = count
    return counts


def run(contract, configs, trial, coordinator, mode):
    run_id = "storage-contract-" + uuid.uuid4().hex
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "run_id": run_id, "mode": mode, "status": "failed", "endpoints": {}, "passed": [],
              "limits": "Bounded synthetic control; no durability, production recovery, hostile-client, workdir, agent or macOS qualification."}
    peers = []
    try:
        for config in configs:
            peer = contract.Endpoint(config, Path(contract.__file__).read_text(), run_id)
            peers.append(peer)
            report["endpoints"][peer.name] = peer.call("info")
        a, b = peers
        a.call("prepare", create=True)
        b.call("prepare", create=False)
        a.call("open", name="mapping", handle="mapping", create=True)
        a.call("size", handle="mapping", size=8192)
        b.call("open", name="mapping", handle="mapping")
        for peer in peers:
            contract.expect(peer.call("map"), bytes(8).hex(), "initial map")
            contract.expect(peer.call("map_read", offset=4096), bytes(8).hex(), "fault both pages")
        path = trial / "account" / run_id / "mapping"
        lease = trial / "control/lifetime.lock"
        if mode == "mapped-resize":
            for step in range(32):
                writer, reader = (a, b) if step % 2 == 0 else (b, a)
                value = (step + 1).to_bytes(8, "little")
                writer.call("map_write", offset=0, hex=value.hex())
                writer.call("map_write", offset=4096, hex=value.hex())
                size = (17, 4096, 0, 1)[step % 4]
                writer.call("size", handle="mapping", size=size)
                if size >= 8:
                    contract.expect(reader.call("map_read", offset=0), value.hex(), "valid prefix after shrink")
                writer.call("size", handle="mapping", size=8192)
                for peer in peers:
                    contract.expect(peer.call("map_read", offset=4096), bytes(8).hex(), "regrown page is zero")
                new = (step + 100).to_bytes(8, "little").hex()
                writer.call("map_write", offset=4096, hex=new)
                contract.expect(reader.call("map_read", offset=4096), new, "regrown page is shared")
            report["cycles"] = 32
            report["passed"].append("persistent guest mappings retain valid prefixes and shared zeroed regrown pages across 32 peer shrink/growth cycles")
        elif mode in {"coordinator-loss", "stopped-peer"}:
            # Only process groups created by this run are signalled. Keep B
            # stopped until the old authority has gone and exclusion is checked.
            b.signal(signal.SIGSTOP)
            if mode == "coordinator-loss":
                coordinator.kill()
                coordinator.wait(timeout=5)
            else:
                a.send("size", handle="mapping", size=0)
                deadline = time.monotonic() + 15
                observations = 0
                while coordinator.poll() is None:
                    contract.expect(path.stat().st_size, 8192, "no shrink without stopped peer's acknowledgement")
                    observations += 1
                    if time.monotonic() >= deadline:
                        raise TimeoutError("coordinator did not reject nonresponding peer")
                    time.sleep(0.02)
                contract.expect(coordinator.returncode, 78, "coordinator poisons failed transition")
                contract.expect(path.stat().st_size, 8192, "failed shrink leaves original host file length")
                report["unacknowledged_shrink_observations"] = observations
                report["passed"].append("stopped mapping holder prevents shrink; timeout aborts the authority with host length unchanged")
            wait_for(lambda: a.process.poll() is not None, 5, "live VMM did not exit after authority loss")
            report["live_participant_exit_code"] = a.process.returncode
            contract.expect(exclusive_available(lease), False, "stopped VMM retains lifetime exclusion")
            report["passed"].append("authority loss stops the live participant; stopped participant retains independent lifetime exclusion")
            b.signal(signal.SIGKILL)
            b.process.wait(timeout=5)
            wait_for(lambda: exclusive_available(lease), 5, "lifetime exclusion survived participant teardown")
            report["passed"].append("replacement ownership becomes available only after stopped participant is killed and mappings retire")
        elif mode == "large-append":
            for peer in peers:
                peer.call("open", name="large-append", handle="large-append", create=peer is a, append=True)
            output = trial / "account" / run_id / "large-append"
            size = 2 * 1024 * 1024 + 17
            report["single_syscall_bytes"] = size
            report["pairs"] = []
            for index in range(4):
                payloads = (bytes([65 + index * 2]) * size, bytes([66 + index * 2]) * size)
                counts = parallel_writes(peers, payloads)
                contract.expect(counts, {"a": size, "b": size}, "full single-syscall append counts")
                with output.open("rb") as stream:
                    stream.seek(index * size * 2)
                    actual = stream.read()
                report["pairs"].append({"order": append_order(actual, *payloads),
                                        "sha256": hashlib.sha256(actual).hexdigest(), "counts": counts})
            observed = any(pair["order"] == "interleaved" for pair in report["pairs"])
            report["status"] = "limitation-observed" if observed else "limitation-not-reproduced"
            report["interpretation"] = ("Whole-syscall append atomicity fails despite complete returned write counts."
                                        if observed else "This scheduling run did not reproduce interleaving; it does not disprove the deterministic native counterexample.")
        else:
            raise ValueError("unknown control")
        if report["status"] == "failed":
            report["status"] = "passed-bounded-control"
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        for peer in peers:
            try:
                if peer.process.poll() is None:
                    peer.signal(signal.SIGCONT)
                peer.stop()
            except Exception as error:
                report.setdefault("cleanup_errors", []).append(peer.name + ": " + repr(error))
                report["status"] = "failed"
        (trial / "control-report.json").write_text(json.dumps(report, indent=2) + "\n")
    return 1 if report["status"] == "failed" or "error" in report else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build", required=True, type=Path)
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="ycf-followup-", dir="/var/tmp"))
    print("Follow-up artifacts: " + str(root), flush=True)
    report = {"status": "completed", "results": {},
              "limits": "Characterization includes a known append failure, not a passing complete filesystem contract."}
    try:
        for mode in MODES:
            command = [sys.executable, str(HERE / "trial.py"), "--build", str(args.build.resolve()),
                       "--refresh-launcher", "--control", mode, "--output-parent", str(root)]
            # trial.py owns a 180-second alarm and always tears down Endpoint
            # groups. Keep it as the cleanup authority rather than SIGKILLing it.
            with (root / (mode + ".log")).open("x") as log:
                result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT)
            report["results"][mode] = {"exit_code": result.returncode}
            print(mode + ": " + ("completed" if result.returncode == 0 else "failed"), flush=True)
            if result.returncode:
                report["status"] = "failed"
                break
    except KeyboardInterrupt:
        report["status"] = "interrupted"
    finally:
        (root / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"status": report["status"], "artifacts": str(root)}))
    return 0 if report["status"] == "completed" else 1


if __name__ == "__main__":
    sys.exit(main())
