#!/usr/bin/env python3
"""Lima process boundary fake: persists disks and VMs separately, records calls."""
import json
import gzip
import struct
import os
from pathlib import Path
import shutil
import sys

home = Path(os.environ['LIMA_HOME'])
home.mkdir(parents=True, exist_ok=True)
args = sys.argv[1:]
with (home / 'calls').open('a') as out:
    # Do not record the embedded guest program or secret request body.
    out.write(' '.join(args[:3]) + '\n')
# Optional mutation oracle: record only operation names, never env/secret argv.
mutation_log = os.environ.get('FAKE_LIMA_BOX_MUTATIONS')
if mutation_log and args[0] == 'shell':
    for program, operations in [('nerdctl', ['run', 'start', 'stop', 'rm', 'build', 'exec']),
                                ('systemctl', ['stop'])]:
        if program in args:
            tail = args[args.index(program) + 1:]
            for operation in operations:
                if operation in tail:
                    with Path(mutation_log).open('a') as out:
                        out.write(f'{program} {operation}\n')
fail = os.environ.get('FAKE_LIMA_FAIL', '')
if fail and ' '.join(args[:2]).startswith(fail):
    sys.exit(23)
if args[0] == 'disk':
    name = args[3] if args[1] == 'list' else args[2]
    disk = home / '_disks' / name
    if args[1] == 'create':
        disk.mkdir(parents=True)
        if os.environ.get('FAKE_DISK_FORMAT', 'raw') == 'qcow2':
            (disk / "datadisk").write_bytes(gzip.decompress(Path(os.environ['FAKE_QCOW2_FIXTURE']).read_bytes()))
        else:
            (disk / "datadisk").write_bytes(bytes(4096))
    elif args[1] == 'list':
        if disk.exists():
            print(json.dumps(dict(name=name, instance=os.environ.get('FAKE_DISK_OWNER', ''), format='qcow2' if (disk / 'datadisk').read_bytes()[:4] == b'QFI\xfb' else 'raw')))
    elif args[1] == 'delete':
        if not os.environ.get('FAKE_KEEP_DISK'):
            shutil.rmtree(disk, ignore_errors=True)
elif args[0] == 'list':
    for directory in sorted(home.glob('yolo-*')):
        if (directory / 'status').exists():
            print(json.dumps(dict(name=directory.name, status=(directory / 'status').read_text(), dir=str(directory))))
elif args[0] == 'create':
    name = next(a.split('=', 1)[1] for a in args if a.startswith('--name='))
    instance = home / name
    instance.mkdir()
    (instance / 'lima.yaml').write_text(sys.stdin.read())
    (instance / 'status').write_text('Stopped')
elif args[0] in ('start', 'stop'):
    if args[0] == 'start':
        import re
        config = (home / args[-1] / 'lima.yaml').read_text()
        match = re.search(r'additionalDisks:\s*- name: ["\']?([a-z0-9]+)', config)
        if match:
            raw = home / '_disks' / match[1] / 'datadisk'
            data = raw.read_bytes()
            qcow2 = data.startswith(b'QFI\xfb')
            # Our fixture starts with an empty L1 table. Formatting installs
            # one mapped data cluster so retries must see nonzero guest bytes.
            blank = not any(data[3*65536:4*65536]) if qcow2 else not any(data)
            if blank:
                if not re.search(r'format: true', config):
                    sys.exit(24)  # Lima cannot mount an unformatted disk.
                if qcow2:
                    data = bytearray(data)
                    data.extend(bytes(2*65536))
                    struct.pack_into('>Q', data, 3*65536, (1 << 63) | 4*65536)
                    struct.pack_into('>Q', data, 4*65536, (1 << 63) | 5*65536)
                    struct.pack_into('>HH', data, 2*65536+8, 1, 1)
                    data[5*65536:5*65536+9] = b'fake ext4'
                    raw.write_bytes(data)
                else:
                    raw.write_bytes(b'fake ext4' + data[9:])
    (home / args[-1] / 'status').write_text('Running' if args[0] == 'start' else 'Stopped')
elif args[0] == 'delete':
    if not os.environ.get('FAKE_KEEP_VM'):
        shutil.rmtree(home / args[-1])
elif args[0] == 'shell':
    if 'python3' in args:
        request = json.load(sys.stdin)
        # Match guest.py's verify response exactly; its decoder deliberately
        # rejects unrelated account-list fields rather than ignoring them.
        if request.get('action') == 'verify':
            print(json.dumps(dict(uuid='fake-filesystem-uuid')))
        else:
            print(json.dumps(dict(uuid='fake-filesystem-uuid', leaves=[])))
    # nerdctl ps: no boxes. Other guest cleanup is empty in this fake.
else:
    raise ValueError('unexpected fake Lima invocation')
