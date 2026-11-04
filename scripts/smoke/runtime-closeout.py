#!/usr/bin/env python3
"""Short isolated kernel, forwarding and OCI declaration check; no image builds."""
import argparse
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import tempfile
import time

SERVER = r'''
import socket, threading, sys
port=int(sys.argv[1]); state={'reverse':''}
tcp=socket.socket();tcp.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);tcp.bind(('127.0.0.1',port));tcp.listen()
udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);udp.bind(('127.0.0.1',port))
def client(c):
    with c:
        c.settimeout(20)
        data=b''
        while not data.endswith(b'\n'):
            part=c.recv(4096)
            if not part:break
            data+=part
        if data == b'reverse\n':
            c.sendall(b'REVERSE');c.shutdown(socket.SHUT_WR)
            data=b''
            while True:
                part=c.recv(4096)
                if not part:break
                data+=part
            state['reverse']=data.decode()
        else:
            c.sendall(b'REPLY:'+data)
def accept():
    while True:
        c,_=tcp.accept();threading.Thread(target=client,args=(c,),daemon=True).start()
threading.Thread(target=accept,daemon=True).start()
print('READY',flush=True)
while True:
    data,peer=udp.recvfrom(4096)
    if data==b'STOP':break
    udp.sendto(state['reverse'].encode() if data==b'STATUS' else b'UDP:'+data,peer)
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--yoloctl', default=str(Path(__file__).resolve().parents[2] / 'bin/yoloctl'))
    parser.add_argument('--fixture', help='Resume a retained yoloctl-closeout.* fixture in its original location')
    args = parser.parse_args()
    executable = str(Path(args.yoloctl).resolve(strict=True))
    if args.fixture:
        root = Path(args.fixture).resolve(strict=True)
        if (not root.name.startswith('yoloctl-closeout.')
                or not (root / 'home/spaces/smoke-runtime-closeout/space.yaml').is_file()
                or not (root / 'workdir').is_dir()):
            parser.error('--fixture must identify this smoke script\'s retained space')
    else:
        root = Path(tempfile.mkdtemp(prefix='yoloctl-closeout.')).resolve()
        (root / 'workdir').mkdir()
    env = dict(os.environ, YOLOCTL_HOME=str(root / 'home'),
               XDG_CONFIG_HOME=str(root / 'config'), XDG_CACHE_HOME=str(root / 'cache'))
    roots = json.loads(subprocess.check_output([executable, 'version', '--json'], env=env))
    assert roots['config_root'] == env['YOLOCTL_HOME']
    # Both roots belong to this fixture, including all downloads.
    space = 'smoke-runtime-closeout'
    log = (root / 'commands.log').open('a')
    echo_log = (root / 'echo.log').open('a')
    server = None
    created = False

    def run(*command, timeout=180, capture=False):
        return subprocess.run([executable, *command], env=env, check=True,
                              stdin=subprocess.DEVNULL, stdout=subprocess.PIPE if capture else log,
                              stderr=log, text=True, timeout=timeout).stdout

    def ssh(*command):
        return ('space', 'ssh', space, '--root', '--', *command)

    def request(payload, half_close=False):
        with socket.create_connection(('127.0.0.1', port), timeout=3) as c:
            c.sendall(payload)
            if half_close:
                c.shutdown(socket.SHUT_WR)
            result = b''
            while True:
                data = c.recv(4096)
                if not data:
                    return result
                result += data

    print(f'Isolated fixture: {root}', flush=True)
    try:
        created = True  # Creation may fail after publishing recoverable state.
        if args.fixture:
            run('space', 'start', space, '--update-controller')
        else:
            run('space', 'create', space, '--backend', 'firecracker', '--account', 'trial',
                '--memory', '4GiB', '--workdir', str(root / 'workdir'), timeout=600)
        release = run(*ssh('uname', '-r'), capture=True).strip()
        if release != '7.2.11':
            raise RuntimeError(f'unexpected kernel: {release}')
        run(*ssh('python3', '-c',
                 'from pathlib import Path; '
                 'controllers=Path("/sys/fs/cgroup/cgroup.controllers").read_text().split(); '
                 'assert {"memory","cpu","pids"} <= set(controllers), controllers; '
                 'assert "some avg10=" in Path("/proc/pressure/memory").read_text()'))
        print('PASS: bundled Linux 7.2.11, cgroup memory and pressure telemetry', flush=True)
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        server = subprocess.Popen([executable, *ssh('python3', '-u', '-c', SERVER, str(port))],
                                  env=env, stdin=subprocess.PIPE, stdout=echo_log, stderr=echo_log)
        deadline = time.monotonic() + 20
        while True:
            try:
                if request(b'frame\n') == b'REPLY:frame\n':
                    break
            except OSError:
                pass
            if time.monotonic() >= deadline or server.poll() is not None:
                raise RuntimeError('TCP forwarding did not become ready')
            time.sleep(.2)
        if request(b'eof-request', half_close=True) != b'REPLY:eof-request':
            raise RuntimeError('lost reply after client EOF')
        with socket.create_connection(('127.0.0.1', port), timeout=3) as c:
            c.sendall(b'reverse\n')
            reply = b''
            while True:
                data = c.recv(4096)
                if not data:
                    break
                reply += data
            if reply != b'REVERSE':
                raise RuntimeError('missing server half-close')
            c.sendall(b'after-server-EOF')
            c.shutdown(socket.SHUT_WR)
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as u:
            u.settimeout(3)
            u.sendto(b'hello', ('127.0.0.1', port))
            if u.recv(4096) != b'UDP:hello':
                raise RuntimeError('UDP mismatch')
            deadline = time.monotonic() + 3
            while True:
                u.sendto(b'STATUS', ('127.0.0.1', port))
                if u.recv(4096) == b'after-server-EOF':
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError('lost request after server EOF')
                time.sleep(.1)
            u.sendto(b'STOP', ('127.0.0.1', port))
        server.wait(timeout=5)
        print('PASS: TCP, both half-close directions and UDP', flush=True)
        feature = ('ghcr.io/devcontainers/features/github-cli@sha256:'
                   'bd7ab48a832228f633239277552c30b353867fef2e5b037e064b4e64f0b843f2')
        run('box', 'create', 'oci-original', '--space', space,
            '--agent', 'none', '--account', 'none', '--image', 'ubuntu:24.04',
            '--workdir', str(root / 'workdir'), '--manifest-only',
            '--feature', feature, '--feature-option', 'github-cli.version=latest')
        original = json.loads(run('box', 'inspect', 'oci-original', '--space', space,
                                  '--json', capture=True))['manifest']
        if original['feature_options'][feature]['version'] != 'latest':
            raise RuntimeError('OCI option did not persist')
        run('box', 'copy', 'oci-original', 'oci-copy', '--space', space, '--from-space', space,
            '--manifest-only', '--feature-option', 'github-cli.version=2.75.0')
        copied = json.loads(run('box', 'inspect', 'oci-copy', '--space', space,
                                '--json', capture=True))['manifest']
        if (copied['feature_options'][feature]['version'] != '2.75.0'
                or copied['runtime_image'] == original['runtime_image']):
            raise RuntimeError('OCI copy option did not change image identity')
        completion = run('__complete', 'box', 'set', 'oci-copy', '--space', space,
                         '--feature-option', 'github-cli.version=l', capture=True)
        if 'github-cli.version=latest' not in completion.splitlines():
            raise RuntimeError('OCI persisted option completion missing')
        recipe = run('image', 'build', '--space', space, '--tag', 'closeout',
                     '--base', 'ubuntu:24.04', '--feature', feature,
                     '--feature-option', 'github-cli.version=latest', '--dry-run', capture=True)
        if "VERSION='latest'" not in recipe or './install.sh' not in recipe:
            raise RuntimeError('OCI build recipe missing option or installer')
        print('PASS: OCI resolution, declaration/copy, option identity, completion and build plan', flush=True)
        run('space', 'destroy', space, '--force')
        created = False
        result = {'kernel': release, 'forwarding': 'pass', 'oci_declarations': 'pass',
                  'coverage': 'No image builds or installer execution; Codex packaging not exercised.'}
        (root / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        print(f'PASS: short runtime gate; VM removed, report in {root}', flush=True)
    finally:
        if server is not None and server.poll() is None:
            server.terminate()
            try:
                server.wait(timeout=5)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()
        if server is not None:
            server.stdin.close()
        log.close()
        echo_log.close()
        if created:
            print('Failed fixture retained; do not move it. Cleanup after inspection:')
            print(f'env YOLOCTL_HOME={shlex.quote(env["YOLOCTL_HOME"])} '
                  f'{shlex.quote(executable)} space destroy {space} --force')


if __name__ == '__main__':
    main()
