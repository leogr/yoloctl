"""Read-only confined image verification. Exit 3 means use the upload fallback."""
import hashlib
import os
import stat
import sys


def matches(root, relative, size, digest):
    # The holder already checked the canonical host workdir boundary. Walk
    # every guest component without following a replacement symlink. O_NONBLOCK
    # and fstat reject FIFOs/devices without waiting for a writer.
    directory_flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    # The workdir mount root is trusted and readable under the inner policy;
    # opening / for directory reads is neither needed nor normally permitted.
    fd = os.open(root, directory_flags)
    try:
        components = relative.split("/")
        if any(part in ("", ".", "..") for part in components):
            return False
        for part in components[:-1]:
            next_fd = os.open(part, directory_flags, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        image_fd = os.open(components[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
        with os.fdopen(image_fd, "rb") as image:
            info = os.fstat(image.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size != size:
                return False
            data = image.read(size + 1)
            return len(data) == size and hashlib.sha256(data).hexdigest() == digest
    finally:
        os.close(fd)


try:
    root, relative, size, digest = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
    matched = 0 < size <= (16 << 20) and matches(root, relative, size, digest)
except (OSError, ValueError):
    matched = False
sys.exit(0 if matched else 3)
