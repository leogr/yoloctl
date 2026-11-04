#!/usr/bin/env python3
"""Three isolated boxes: a room server with its human account and two proxy-confined AIRC clients."""
import argparse
from collections import deque
import hashlib
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

spec = importlib.util.spec_from_file_location('box_use', Path(__file__).with_name('box-use.py'))
box_use = importlib.util.module_from_spec(spec)
spec.loader.exec_module(box_use)


def visible(client, marker):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        screen = client.request('screen')['screen']
        if any(marker in line for line in screen.get('lines', [])):
            return
        client.request('wait', since=screen['revision'], timeout_ms=500, settle_ms=100)
    raise AssertionError(f'session did not display {marker}\nLast screen:\n' + '\n'.join(screen.get('lines', [])))


def request(client, op, **fields):
    response = client.request(op, **fields)
    assert response.get('ok'), response
    return response.get('result', {})


def until(client, predicate, description='expected IRC event'):
    deadline = time.monotonic() + 30
    recent = deque(maxlen=20)
    while time.monotonic() < deadline:
        result = request(client, 'read', after=client.cursor, wait_ms=500, limit=200)
        assert not result['gap'], 'unexpected cursor gap'
        client.cursor = result['next']
        for event in result['events']:
            recent.append(event)
            if predicate(event):
                return event
    raise AssertionError(f'{description} did not arrive; recent events: ' + json.dumps(list(recent)))


def message(client, text, account=None, history=False):
    return until(client, lambda event: (
        event['command'] == 'PRIVMSG' and text in event['params'][-1]
        and (account is None or event['tags'].get('account') == account)
        and (not history or 'batch' in event['tags'])
    ), description=f'PRIVMSG containing {text!r}')


def member(client, channel, nick):
    request(client, 'members', channel=channel)
    # NAMES is a snapshot. The human can finish joining after that response; its
    # later JOIN is equally valid evidence and does not trigger another NAMES.
    return until(client, lambda event: (
        event['command'] == '353' and event['params'][-2] == channel
        and nick in [name.lstrip('@+') for name in event['params'][-1].split()]
    ) or (
        event['command'] == 'JOIN' and event['params'][0] == channel
        and (event.get('prefix') or '').split('!', 1)[0] == nick
    ), description=f'{nick} joining {channel}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['yoloctl', 'space', 'output']:
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    # This driver writes a catalog entry: require common.sh's disposable root.
    root = Path(os.environ['SMOKE_ROOT']).resolve()
    os.environ['YOLOCTL_HOME'] = str(root / 'home')
    config = Path(json.loads(subprocess.check_output([args.yoloctl, 'version', '--json']))['config_root']).resolve()
    assert config == root / 'home', 'requires the isolated smoke configuration'
    assert args.space == os.environ['SMOKE_SPACE']
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    clients = []
    room = 'smoke-airc-room'
    participants = ['smoke-airc-a', 'smoke-airc-b']
    execution = 0
    session = None

    def command(verb, box, *rest):
        return [args.yoloctl, 'box', verb, box, '--space', args.space, *rest]

    def execute(box, script, data=None):
        nonlocal execution
        execution += 1
        result = subprocess.run(
            command('exec', box, '--', 'python3', '-c', script),
            input='' if data is None else json.dumps(data),
            capture_output=True, text=True, timeout=60,
        )
        if result.returncode:
            log = output / f'exec-{execution}.stderr'
            log.write_text(result.stderr)
            raise RuntimeError(f'confined command in {box} failed; see {log}')
        return result.stdout

    def room_address():
        address = execute(room, 'import socket; print(socket.gethostbyname(socket.gethostname()))').strip()
        ip = ipaddress.IPv4Address(address)
        assert ip.is_private and not ip.is_loopback and not ip.is_link_local and not ip.is_unspecified, 'expected VM bridge address'
        return address

    def start_client(box, label, config='.airc/client.toml'):
        client = box_use.Client(command('exec', box, '--', 'airc', '--config', config), output / f'{label}.stderr')
        clients.append(client)
        assert client.receive()['event'] == 'ready'
        client.cursor = 0
        return client

    def view(label):
        # The room's managed session runs aircd; its output is the server log.
        client = box_use.Client(command('attach', room, '--box-use', '--no-autoupdate'), output / f'{label}.stderr')
        clients.append(client)
        ready = client.receive()
        assert ready['type'] == 'ready'
        visible(client, 'aircd listening on')
        return client, ready

    def start_human(label):
        # The human account's client runs beside the server in the room box.
        return start_client(room, label, config='.airc/human.toml')

    def quit_client(client):
        request(client, 'quit')
        assert client.process.wait(timeout=10) == 0

    def detach(client):
        client.request('detach')
        assert client.process.wait(timeout=10) == 0

    try:
        # Only the server fixture opts out of network isolation so other boxes
        # can reach its listener. SSH, filesystem and process confinement remain.
        # Plaintext is explicit on this disposable VM bridge; no ports are published.
        subprocess.run(command('create', room, '--agent', 'none', '--account', 'none',
            '--workdir-mode', 'ephemeral', '--features', 'yolo.chat.skill', '--unconfined-network',
            '--session-command', '/bin/sh', '--session-command=-c',
            '--session-command', 'umask 077; exec aircd serve --config .airc/server.toml',
            '--no-attach', '--no-autoupdate'), check=True, timeout=1200)
        subprocess.run(command('exec', room, '--', 'aircd', 'init', '.airc'), check=True, timeout=60)
        address = room_address()
        execute(room, '''import json, sys
from pathlib import Path
address = json.load(sys.stdin)
path = Path('.airc/server.toml')
text = path.read_text().replace('127.0.0.1', address).replace('allow_insecure = false', 'allow_insecure = true')
path.write_text(text)
path = Path('.airc/human.toml')
path.write_text(path.read_text().replace('127.0.0.1', address).replace('allow_insecure = false', 'allow_insecure = true'))
''', address)
        capability = config / 'capabilities/network.smoke-airc.yaml'
        capability.parent.mkdir(parents=True, exist_ok=True)
        capability.write_text(f'''schema_version: 1
name: network.smoke-airc
description: This isolated AIRC fixture only
grants:
  network:
    egress:
      - hosts: ["{address}"]
        ports: [6667, 8080]
        binaries: [/usr/local/bin/airc]
''')
        for box, account in zip(participants, ['agent-a', 'agent-b']):
            subprocess.run(command('create', box, '--agent', 'none', '--account', 'none',
                '--workdir-mode', 'ephemeral', '--features', 'yolo.chat.skill', '--capability', 'network.smoke-airc',
                '--no-attach', '--no-autoupdate'), check=True, timeout=1200)
            # Credentials pass over confined SSH stdin; neither argv nor logs
            # contain them. Each participant receives only its own token/config.
            bundle = json.loads(execute(room, '''import json, sys
from pathlib import Path
account = json.load(sys.stdin)
root = Path('.airc')
print(json.dumps({'config': (root / (account + '.toml')).read_text(), 'token': (root / (account + '.token')).read_text(), 'account': account}))
''', account))
            bundle['address'] = address
            execute(box, '''import json, os, sys
from pathlib import Path
data = json.load(sys.stdin)
proxy = os.environ.get('HTTPS_PROXY') or os.environ.get('https_proxy') or os.environ.get('HTTP_PROXY') or os.environ.get('http_proxy')
assert proxy, 'missing confined network proxy'
root = Path('.airc')
root.mkdir(mode=0o700)
text = data['config'].replace('127.0.0.1', data['address']).replace('allow_insecure = false', 'allow_insecure = true')
text += '\\nproxy = ' + json.dumps(proxy) + '\\n'
for name, value in [('client.toml', text), (data['account'] + '.token', data['token'])]:
    fd = os.open(root / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(value)
assert sorted(p.name for p in root.iterdir()) == [data['account'] + '.token', 'client.toml']
''', bundle)
            del bundle
            # A different executable cannot borrow airc's endpoint grant.
            execute(box, '''import json, os, socket, sys, urllib.parse
address = json.load(sys.stdin)
proxy = urllib.parse.urlsplit(os.environ.get('HTTPS_PROXY') or os.environ.get('https_proxy') or os.environ.get('HTTP_PROXY') or os.environ['http_proxy'])
with socket.create_connection((proxy.hostname, proxy.port or 80), timeout=10) as sock:
    sock.sendall(f'CONNECT {address}:6667 HTTP/1.1\\r\\nHost: {address}:6667\\r\\n\\r\\n'.encode())
    reply = b''
    while b'\\r\\n' not in reply and len(reply) < 8192:
        part = sock.recv(1024)
        assert part, 'proxy closed without a denial'
        reply += part
    assert reply.split(b'\\r\\n', 1)[0].split()[1] == b'403', 'ungranted executable was not denied'
''', address)
        session, first_ready = view('session')
        human = start_human('human')
        alice, bob = [start_client(box, label) for box, label in zip(participants, ['alice', 'bob'])]
        member(alice, '#work', 'human')
        marker = 'airc-' + uuid.uuid4().hex[:8]
        for client, account in [(alice, 'agent-a'), (bob, 'agent-b')]:
            text = marker + '-' + account
            request(client, 'send', target='#work', text=text)
            message(human, text, account=account)
        steering = marker + '-steer'
        request(human, 'send', target='#work', text=steering)
        anchor = message(alice, steering, account='human')['tags']['msgid']
        message(bob, steering, account='human')
        for client, suffix in [(alice, 'ack-a'), (bob, 'ack-b')]:
            request(client, 'send', target='#work', text=marker + '-' + suffix)
            message(human, marker + '-' + suffix)
        quit_client(bob)
        missed = marker + '-offline'
        request(alice, 'send', target='#work', text=missed)
        durable_id = message(alice, missed, account='agent-a')['tags']['msgid']
        bob = start_client(participants[1], 'bob-reconnected')
        request(bob, 'history', channel='#work', direction='AFTER', reference='msgid=' + anchor, limit=50)
        assert message(bob, missed, history=True)['tags']['msgid'] == durable_id
        payload = (marker + '\nshared artifact\n').encode()
        execute(participants[0], "import json, sys; from pathlib import Path; Path('.airc/report.txt').write_text(json.load(sys.stdin))", payload.decode())
        artifact = request(alice, 'upload', channel='#work', path='.airc/report.txt')['id']
        message(bob, artifact)
        transfer = request(bob, 'download', artifact=artifact, path='.airc/received.txt')
        assert transfer['sha256'] == hashlib.sha256(payload).hexdigest()
        assert transfer['bytes'] == len(payload)
        assert not bob.request('download', artifact=artifact, path='.airc/received.txt')['ok']
        detach(session)
        # The server outlives a detached view of its session.
        request(alice, 'send', target='#work', text=marker + '-detached')
        message(human, marker + '-detached', account='agent-a')
        session, rejoined_ready = view('session-rejoined')
        assert rejoined_ready['session'] == first_ready['session']
        detach(session)
        quit_client(alice)
        quit_client(bob)
        quit_client(human)
        subprocess.run(command('restart', room, '--no-attach', '--no-autoupdate'), check=True, timeout=180)
        # Container IP allocation is not a persistence contract. If it changes,
        # explicitly reconfigure this test's listeners and grants before launch.
        revived_address = room_address()
        if revived_address != address:
            execute(room, '''import json, sys
from pathlib import Path
old, new = json.load(sys.stdin)
for name in ['server.toml', 'human.toml']:
    path = Path('.airc') / name
    path.write_text(path.read_text().replace(old, new))
''', [address, revived_address])
            capability.write_text(capability.read_text().replace(address, revived_address))
            for box in participants:
                execute(box, '''import json, sys
from pathlib import Path
old, new = json.load(sys.stdin)
path = Path('.airc/client.toml')
path.write_text(path.read_text().replace(old, new))
''', [address, revived_address])
                subprocess.run(command('restart', box, '--no-attach', '--no-autoupdate'), check=True, timeout=180)
        session, revived_ready = view('session-revived')
        assert revived_ready['session'] != first_ready['session']
        bob = start_client(participants[1], 'bob-revived')
        request(bob, 'history', channel='#work', direction='AFTER', reference='msgid=' + anchor, limit=50)
        assert message(bob, missed, history=True)['tags']['msgid'] == durable_id
        transfer = request(bob, 'download', artifact=artifact, path='.airc/after-restart.txt')
        assert transfer['sha256'] == hashlib.sha256(payload).hexdigest()
        quit_client(bob)
        detach(session)
        print('PASS: separate confined clients, binary-scoped proxy policy, human steering, reconnect history, artifacts and server restart')
    except Exception:
        if session is not None and session.process.poll() is None:
            try:
                screen = session.request('screen')['screen']
                print('Server session screen at failure:\n' + '\n'.join(screen.get('lines', [])), file=sys.stderr)
            except Exception as error:
                print(f'Could not capture the server session screen: {error}', file=sys.stderr)
        raise
    finally:
        for client in reversed(clients):
            client.close()
        # common.sh owns destruction of the entire disposable space, including
        # partial setup. Preserve all boxes when SMOKE_PRESERVE_SPACE is set.


if __name__ == '__main__':
    main()
