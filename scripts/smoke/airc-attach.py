#!/usr/bin/env python3
"""Check a box whose managed session runs aircd, using the shared box-use smoke client."""
import argparse
import importlib.util
from pathlib import Path
import time
import uuid

spec = importlib.util.spec_from_file_location('box_use', Path(__file__).with_name('box-use.py'))
box_use = importlib.util.module_from_spec(spec)
spec.loader.exec_module(box_use)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['yoloctl', 'box', 'space', 'output']:
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    attach = [args.yoloctl, 'box', 'attach', args.box, '--space', args.space, '--box-use', '--no-autoupdate']
    clients = []

    def view(name):
        client = box_use.Client(attach, output / f'{name}.stderr')
        clients.append(client)
        ready = client.receive()
        assert ready['type'] == 'ready', ready
        return client, ready

    def visible(client, text):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            screen = client.request('screen')['screen']
            if any(text in line for line in screen.get('lines', [])):
                return
            client.request('wait', since=screen['revision'], timeout_ms=500, settle_ms=100)
        raise AssertionError(f'missing screen text: {text}\nLast screen:\n' + '\n'.join(screen.get('lines', [])))

    def connect(name):
        client = box_use.Client([
            args.yoloctl, 'box', 'exec', args.box, '--space', args.space,
            '--', 'airc', '--config', f'.airc/{name}.toml',
        ], output / f'{name}.stderr')
        clients.append(client)
        assert client.receive()['event'] == 'ready'
        client.cursor = 0
        return client

    def received(client, text):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            response = client.request('read', after=client.cursor, wait_ms=500)
            assert response['ok'], response
            client.cursor = response['result']['next']
            if any(e['command'] == 'PRIVMSG' and text in e['params'][-1] for e in response['result']['events']):
                return
        raise AssertionError(f'missing message: {text}')

    try:
        # The session's own output is the server's log.
        first, ready = view('first')
        visible(first, 'aircd listening on')
        agent, human = connect('agent-a'), connect('human')
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            assert agent.request('members', channel='#work')['ok']
            response = agent.request('read', after=agent.cursor, wait_ms=500)
            assert response['ok'], response
            agent.cursor = response['result']['next']
            if any(e['command'] == '353' and 'human' in ' '.join(e['params']) for e in response['result']['events']):
                break
        else:
            raise AssertionError('the human account did not join #work')
        marker = 'airc-' + uuid.uuid4().hex[:8]
        assert agent.request('send', target='#work', text=marker)['ok']
        received(human, marker)
        first.request('detach')
        assert first.process.wait(timeout=10) == 0
        # The server outlives a detached view.
        later = marker + '-detached'
        assert agent.request('send', target='#work', text=later)['ok']
        received(human, later)
        second, second_ready = view('second')
        assert second_ready['session'] == ready['session'], 'detaching replaced the session'
        visible(second, 'aircd listening on')
        second.request('detach')
        assert second.process.wait(timeout=10) == 0
        for client in [agent, human]:
            assert client.request('quit')['ok']
            assert client.process.wait(timeout=10) == 0
        print('PASS: persisted command, agent-to-human delivery, detach survival and rejoin')
    finally:
        for client in reversed(clients):
            client.close()


if __name__ == '__main__':
    main()
