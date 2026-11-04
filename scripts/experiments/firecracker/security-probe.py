"""Executed only through OpenShell inside the disposable guest."""

import errno
import json
import os

# The guest prepares this file mode 0666 beneath a mode 0755 directory; an
# unconfined same-UID control must be able to both read and write it first.
forbidden = "/var/poc-forbidden.txt"
if os.environ.get("POC_DAC_CONTROL") == "1":
    with open(forbidden, "r+") as stream:
        assert stream.read() == "synthetic fixture\n"
        stream.seek(0)
        stream.write("synthetic fixture\n")
    print("POC_DAC_CONTROL_OK", flush=True)
    raise SystemExit(0)

assert os.getuid() == 2000 and os.getgid() == 2000, "OpenShell did not drop identity"
with open("/poc-workdir/allowed.txt", "w+") as stream:
    stream.write("allowed\n")
    stream.flush()
    os.fsync(stream.fileno())
    stream.seek(0)
    assert stream.read() == "allowed\n"

for mode in ("r", "r+"):
    try:
        with open(forbidden, mode):
            raise AssertionError("OpenShell allowed access outside the filesystem policy")
    except OSError as exc:
        assert exc.errno in (errno.EACCES, errno.EPERM), f"unexpected denial: {exc}"

with open("/proc/self/status") as stream:
    status = dict(line.split(":", 1) for line in stream if ":" in line)
assert int(status["NoNewPrivs"]) == 1, "no_new_privs absent"
assert int(status["Seccomp"]) == 2, "seccomp filter absent"
assert int(status["CapEff"].strip(), 16) == 0, "effective capabilities remain"
with open("/tmp/poc-outer-netns") as stream:
    outer = stream.read()
assert os.readlink("/proc/self/ns/net") != outer, "network namespace was not isolated"
print("POC_OPENSHELL_SECURITY_OK " + json.dumps({
    "allowed_write": True, "forbidden_read_denied": True, "forbidden_write_denied": True,
    "uid": 2000, "seccomp": True, "no_new_privs": True, "network_namespace": True,
}), flush=True)
