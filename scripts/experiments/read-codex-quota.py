#!/usr/bin/env python3
"""Read only the current session's recent quota events; never print auth/state."""

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import time


def read_live(reserve_percent=20):
    """Use the installed CLI's read-only RPC; do not start a model thread."""
    process = subprocess.Popen(
        ["codex", "app-server", "--stdio"],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL, text=True, bufsize=1, start_new_session=True,
    )
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)

    def send(message):
        process.stdin.write(json.dumps(message) + "\n")
        process.stdin.flush()

    try:
        send({"id": 1, "method": "initialize", "params": {
            "clientInfo": {"name": "yoloctl-storage-quota", "version": "0.1"},
            "capabilities": {"experimentalApi": True},
        }})
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            if not selector.select(1):
                continue
            line = process.stdout.readline()
            if not line:
                break
            try:
                record = json.loads(line)
            except ValueError:
                continue
            if record.get("id") not in (1, 2):
                continue
            if "error" in record:
                # Do not expose arbitrary server error text or notifications.
                return {"status": "unknown", "reason": "quota RPC failed",
                        "code": record["error"].get("code")}
            if record["id"] == 1:
                send({"method": "initialized"})
                send({"id": 2, "method": "account/rateLimits/read"})
                continue
            result = record.get("result", {})
            limits = (result.get("rateLimitsByLimitId") or {}).get("codex")
            limits = limits or result.get("rateLimits") or {}
            if limits.get("limitId") != "codex":
                break
            weekly = [limits[name] for name in ("primary", "secondary")
                      if limits.get(name) and
                      limits[name].get("windowDurationMins") == 10080]
            if not weekly:
                break
            window = max(weekly, key=lambda entry: entry["usedPercent"])
            used = float(window["usedPercent"])
            if not 0 <= used <= 100:
                break
            return {"status": "stop" if 100 - used <= reserve_percent else "continue",
                    "weekly_remaining_percent": 100 - used,
                    "weekly_used_percent": used,
                    "observed_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                    "resets_at": window.get("resetsAt"),
                    "source": "account/rateLimits/read"}
        return {"status": "unknown", "reason": "no weekly quota RPC response"}
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        selector.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rollout", type=Path)
    parser.add_argument("--max-age-seconds", type=int, default=300)
    parser.add_argument("--live", action="store_true",
                        help="fetch current allowance through account/rateLimits/read")
    parser.add_argument("--reserve-percent", type=float, default=20,
                        help="stop at or below this remaining weekly percentage (default: 20)")
    args = parser.parse_args()
    if not 0 <= args.reserve_percent < 100:
        parser.error("reserve percent must be between 0 and less than 100")
    if args.live:
        result = read_live(args.reserve_percent)
        result["reserve_percent"] = args.reserve_percent
        print(json.dumps(result))
        return 20 if result["status"] == "stop" else 0 if result["status"] == "continue" else 2
    rollout = args.rollout
    if rollout is None:
        thread = os.environ.get("CODEX_THREAD_ID")
        if not thread:
            parser.error("supply --rollout or run within the current Codex session")
        root = Path(os.environ.get("CODEX_HOME", "/home/sandbox/.codex"))
        matches = list((root / "sessions").glob(f"*/*/*/rollout-*-{thread}.jsonl"))
        if len(matches) != 1:
            parser.error("cannot identify exactly one current rollout; supply --rollout")
        rollout = matches[0]
    with rollout.open("rb") as stream:
        stream.seek(max(0, rollout.stat().st_size - 16 * 1024 * 1024))
        lines = stream.read().splitlines()
    samples = []
    for line in reversed(lines):
        if b'"rate_limits"' not in line:
            continue
        try:
            record = json.loads(line.lstrip(b"\x00"))
            limits = record.get("payload", {}).get("rate_limits")
            if not limits or limits.get("limit_id") != "codex":
                continue
            timestamp = dt.datetime.fromisoformat(record["timestamp"].replace("Z", "+00:00"))
            for name in ("primary", "secondary"):
                window = limits.get(name)
                if window and window.get("window_minutes") == 10080:
                    used = float(window["used_percent"])
                    if not 0 <= used <= 100:
                        continue
                    samples.append((timestamp, used, window.get("resets_at")))
        except (ValueError, TypeError, KeyError, AttributeError):
            continue
    if not samples:
        print(json.dumps({"status": "unknown", "reason": "no valid weekly quota event"}))
        return 2
    timestamp, used, resets = max(samples, key=lambda sample: (sample[0], sample[1]))
    age = (dt.datetime.now(dt.timezone.utc) - timestamp).total_seconds()
    remaining = 100 - used
    status = "stop" if remaining <= args.reserve_percent else "continue"
    if age > args.max_age_seconds or age < -60:
        status = "stale"
    print(json.dumps({
        "status": status,
        "weekly_remaining_percent": remaining,
        "weekly_used_percent": used,
        "observed_at": timestamp.isoformat(),
        "age_seconds": round(age, 1),
        "resets_at": resets,
        "source": str(rollout),
        "reserve_percent": args.reserve_percent,
        "note": "Stop at the configured reserve because reported usage may be rounded or delayed.",
    }))
    return 20 if status == "stop" else 2 if status == "stale" else 0


if __name__ == "__main__":
    sys.exit(main())
