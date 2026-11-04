#!/usr/bin/env python3
"""Build/run the bounded, synthetic Linux KVM mapping control on the host."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare-only", action="store_true",
                        help="compile the probe without opening /dev/kvm")
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        parser.error("this primitive control supports only Linux x86_64")
    compiler = shutil.which("cc")
    if not compiler:
        parser.error("a C compiler named cc is required")
    if not args.prepare_only and not os.access("/dev/kvm", os.R_OK | os.W_OK):
        parser.error("run on the host with ordinary-user access to /dev/kvm")

    # Snapshot source into a fresh private directory; never run a copied binary.
    try:
        root = Path(tempfile.mkdtemp(prefix="ykw-",
                                    dir="/tmp" if args.prepare_only else "/var/tmp"))
    except OSError as exc:
        parser.error(f"cannot create the private trial directory: {exc}")
    source = Path(__file__).with_suffix(".c").read_bytes()
    snapshot = root / "probe.c"
    snapshot.write_bytes(source)
    report = {
        "status": "preparing", "artifacts": str(root),
        "kernel": platform.release(), "machine": platform.machine(),
        "source_sha256": hashlib.sha256(source).hexdigest(),
        "prepare_only": args.prepare_only, "steps": [],
    }
    print(f"Trial artifacts: {root}", flush=True)

    def step(name, argv, timeout):
        started = time.monotonic()
        log = root / f"{name}.log"
        try:
            result = subprocess.run(argv, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, timeout=timeout,
                                    check=False)
            output = result.stdout
            code = result.returncode
        except subprocess.TimeoutExpired as exc:
            output = (exc.stdout or b"") + b"\nRunner deadline expired.\n"
            code = 124
        log.write_bytes(output)
        report["steps"].append({"name": name, "argv": argv,
                                "exit_code": code, "log": str(log),
                                "seconds": round(time.monotonic() - started, 3)})
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
        result = step("build", [compiler, "-std=c11", "-O2", "-Wall", "-Wextra",
                                "-Werror", "-pthread", str(snapshot), "-o",
                                executable], 60)
        if result:
            report["status"] = "build-failed"
            return 1
        if args.prepare_only:
            report["status"] = "prepared-not-run"
            return 0
        result = step("control", [executable], 25)
        report["status"] = "primitive-passed" if result == 0 else "primitive-failed"
        return 0 if result == 0 else 1
    finally:
        (root / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print(f"Result: {report['status']}; report: {root / 'report.json'}", flush=True)


if __name__ == "__main__":
    sys.exit(main())
