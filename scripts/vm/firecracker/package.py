#!/usr/bin/env python3
"""Package a verified image into the exact archive accepted by release builds."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import tarfile


def digest(p):
    with p.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def package(image, output):
    image = Path(image)
    names = ['image.json', 'kernel', 'rootfs.ext4', 'firecracker']
    for name in names:
        if (image / name).is_symlink() or not (image / name).is_file():
            raise ValueError('image artifacts must be regular files: ' + name)
    manifest = json.loads((image / 'image.json').read_text())
    for name in names[1:]:
        key = ('rootfs' if name == 'rootfs.ext4' else name) + '_sha256'
        if (image / name).is_symlink() or digest(image / name) != manifest[key]:
            raise ValueError('image artifact checksum mismatch: ' + name)
    with Path(output).open('xb') as f:
        with gzip.GzipFile(fileobj=f, mode='wb', filename='', mtime=0) as gz:
            with tarfile.open(fileobj=gz, mode='w|', format=tarfile.USTAR_FORMAT) as tar:
                for name in names:
                    info = tar.gettarinfo(str(image / name), arcname=name)
                    info.uid = info.gid = 0
                    info.uname = info.gname = ''
                    info.mtime = 0
                    with (image / name).open('rb') as artifact:
                        tar.addfile(info, artifact)
    return digest(Path(output))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    print(package(args.image, args.output))
