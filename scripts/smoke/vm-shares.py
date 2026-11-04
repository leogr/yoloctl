"""Run inside a disposable VM against its granted host directory, never native state."""

import hashlib
import os
from pathlib import Path
import stat
import sys


def run(root):
    private = root / "private"
    private.mkdir(mode=0o700)
    assert stat.S_IMODE(private.stat().st_mode) == 0o700
    original, renamed = private / "before", private / "after"
    fd = os.open(original, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
    try:
        assert stat.S_IMODE(os.fstat(fd).st_mode) == 0o600
        os.write(fd, b"original")
        original.rename(renamed)
        original.write_bytes(b"replacement")
        os.ftruncate(fd, 2)
        os.fchmod(fd, 0o640)
        os.utime(fd, (123456, 123457))
        os.fsync(fd)
        assert renamed.read_bytes() == b"or"
        assert original.read_bytes() == b"replacement"
        assert stat.S_IMODE(renamed.stat().st_mode) == 0o640
        assert int(os.fstat(fd).st_mtime) == 123457
        renamed.unlink()
        os.ftruncate(fd, 1)
        os.fsync(fd)
        assert os.fstat(fd).st_size == 1
    finally:
        os.close(fd)
    link = private / "link"
    link.symlink_to("../created-through-link")
    link.write_bytes(b"relative link")
    assert (root / "created-through-link").read_bytes() == b"relative link"
    # Editor-style replacement: sync content before atomic namespace publication.
    payload = bytes(range(256)) * 16384
    temp = root / "replacement.tmp"
    with temp.open("wb") as stream:
        stream.write(payload)
        stream.flush()
        os.fsync(stream.fileno())
    temp.replace(root / "durable-content")
    assert (root / "durable-content").read_bytes() == payload
    # Directory fsync is handled eagerly by the SFTP server's namespace replies;
    # this smoke establishes live operations, not a host power-loss guarantee.
    (root / "expected.sha256").write_text(hashlib.sha256(payload).hexdigest())


if __name__ == "__main__":
    run(Path(sys.argv[1]))
