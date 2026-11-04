#!/usr/bin/env python3
"""Build the isolated two-process writable mapping/fencing control; no Lima."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--prepare-only", action="store_true")
    mode.add_argument("--native-control", action="store_true",
                      help="exercise the coordinator on /tmp without KVM")
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        parser.error("this control supports only Linux x86_64")
    compiler = shutil.which("cc")
    if not compiler:
        parser.error("a C compiler named cc is required")
    local = args.prepare_only or args.native_control
    if not local and not os.access("/dev/kvm", os.R_OK | os.W_OK):
        parser.error("run on the host with ordinary-user access to /dev/kvm")
    root = Path(tempfile.mkdtemp(prefix="ykw-write-", dir="/tmp" if local else "/var/tmp"))
    source = Path(__file__).with_suffix(".c").read_bytes()
    (root / "probe.c").write_bytes(source)
    report = {
        "status": "preparing", "artifacts": str(root),
        "kernel": platform.release(), "machine": platform.machine(),
        "source_sha256": hashlib.sha256(source).hexdigest(),
        "runner_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "mode": "prepare" if args.prepare_only else "native" if args.native_control else "kvm",
        "steps": [],
    }
    print(f"Trial artifacts: {root}", flush=True)

    def step(name, argv, timeout):
        started = time.monotonic()
        # Own process group: deadline cleanup also reaches both VMM workers.
        with subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                              start_new_session=True) as process:
            try:
                output, _ = process.communicate(timeout=timeout)
                code = process.returncode
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                output, _ = process.communicate()
                output += b"\nRunner deadline expired; trial process group killed.\n"
                code = 124
            except BaseException:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.communicate()
                raise
        log = root / f"{name}.log"
        log.write_bytes(output)
        report["steps"].append({"name": name, "argv": argv, "exit_code": code,
                                "seconds": round(time.monotonic() - started, 3), "log": str(log)})
        print(f">>> {name}: exit {code} (log: {log})", flush=True)
        if name == "control" or code:
            print(output.decode(errors="replace").rstrip(), flush=True)
        return code

    try:
        step("compiler", [compiler, "--version"], 10)
        findmnt = shutil.which("findmnt")
        if findmnt:
            step("backing-filesystem", [findmnt, "-T", str(root), "-J", "-o",
                                       "TARGET,SOURCE,FSTYPE,OPTIONS"], 10)
        executable = str(root / "probe")
        if step("build", [compiler, "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror",
                          "-pthread", str(root / "probe.c"), "-o", executable], 60):
            report["status"] = "build-failed"
            return 1
        if args.prepare_only:
            report["status"] = "prepared-not-run"
            return 0
        result = step("control", [executable,
                                  "--native-control" if args.native_control else "--kvm",
                                  str(root)], 25)
        if result:
            report["status"] = "primitive-failed"
            return 1
        report["status"] = "native-control-passed" if args.native_control else "primitive-passed"
        return 0
    except BaseException:
        report["status"] = "runner-error"
        raise
    finally:
        (root / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print(f"Result: {report['status']}; report: {root / 'report.json'}", flush=True)


if __name__ == "__main__":
    sys.exit(main())
