#!/usr/bin/env python3
"""Exercise installed aircd and airc clients, including the human account, with private state."""
import asyncio
import base64
import contextlib
import json
from pathlib import Path
import re
import socket
import tempfile
import uuid


async def line(client):
    data = await asyncio.wait_for(client.stdout.readline(), 10)
    assert data, 'client exited before replying'
    return json.loads(data)


async def request(client, value):
    value = {'id': 'smoke', **value}
    client.stdin.write((json.dumps(value) + '\n').encode())
    await client.stdin.drain()
    result = await line(client)
    assert result.get('ok'), result
    return result.get('result', {})


async def until_all(client, texts, command='PRIVMSG', timeout=30):
    # Every event of a read is checked before the cursor moves past it, so
    # markers that arrive together are all found in that one read.
    found = {}
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        result = await request(client, {'op': 'read', 'after': client.cursor, 'wait_ms': 500})
        client.cursor = result['next']
        for event in result['events']:
            for text in texts:
                if text not in found and event['command'] == command and any(text in p for p in event['params']):
                    found[text] = event
        if len(found) == len(texts):
            return found
    missing = [text for text in texts if text not in found]
    raise AssertionError(f'missing {command} markers: {missing}')


async def until(client, text, command='PRIVMSG'):
    return (await until_all(client, [text], command))[text]


def shared_msgid(reference):
    # An m: reference is the unpadded base64url of the canonical UUID msgid's
    # 16 bytes; only the exact re-encoding is accepted.
    assert re.fullmatch(r'm:[A-Za-z0-9_-]{22}', reference), reference
    raw = base64.urlsafe_b64decode(reference[2:] + '==')
    assert base64.urlsafe_b64encode(raw).rstrip(b'=').decode() == reference[2:], reference
    return str(uuid.UUID(bytes=raw))


def confirmed(output, target):
    # A confirmed named send prints its shared reference and send token.
    match = re.fullmatch(rf'confirmed {re.escape(target)} (m:\S+); send=(\S+)', output.strip())
    assert match, output
    shared_msgid(match[1])
    return match[1], match[2]


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


async def main():
    children = []
    with tempfile.TemporaryDirectory(prefix='aircd-smoke-') as temporary:
        root = Path(temporary)
        demo = root / 'state'
        named_state = root / 'named'

        async def named(*args):
            process = await asyncio.create_subprocess_exec(
                'airc', '--state-dir', str(named_state), *args,
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            children.append(process)
            out, err = await asyncio.wait_for(process.communicate(), 50)
            assert process.returncode == 0, (args[0], out, err)
            return out.decode()

        async def client(name):
            process = await asyncio.create_subprocess_exec('airc', '--config', str(demo / f'{name}.toml'), stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            children.append(process)
            process.cursor = 0
            assert (await line(process))['event'] == 'ready'
            return process

        async def quit_client(process):
            await request(process, {'op': 'quit'})
            assert await asyncio.wait_for(process.wait(), 5) == 0

        init = await asyncio.create_subprocess_exec('aircd', 'init', str(demo), stdout=asyncio.subprocess.DEVNULL)
        assert await init.wait() == 0
        port, artifact_port = free_port(), free_port()
        while artifact_port == port:
            artifact_port = free_port()
        for name in ['server', 'agent-a', 'agent-b', 'human']:
            config = demo / f'{name}.toml'
            assert config.stat().st_mode & 0o777 == 0o600, f'{config} is not private'
            text = config.read_text().replace(':6667', f':{port}').replace('port = 6667', f'port = {port}').replace(':8080', f':{artifact_port}')
            config.write_text(text)
        original = (demo / 'server.toml').read_bytes()
        repeated = await asyncio.create_subprocess_exec('aircd', 'init', str(demo), stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL)
        assert await repeated.wait() != 0, 'initialization overwrote an existing directory'
        assert (demo / 'server.toml').read_bytes() == original
        try:
            server = await asyncio.create_subprocess_exec('aircd', 'serve', '--config', str(demo / 'server.toml'), stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL)
            children.append(server)
            for _ in range(150):
                assert server.returncode is None, 'server exited early'
                try:
                    _, writer = await asyncio.open_connection('127.0.0.1', port)
                    writer.close()
                    await writer.wait_closed()
                    break
                except OSError:
                    await asyncio.sleep(0.1)
            else:
                raise AssertionError('server did not listen')
            duplicate = await asyncio.create_subprocess_exec('aircd', 'serve', '--config', str(demo / 'server.toml'), stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL)
            children.append(duplicate)
            assert await asyncio.wait_for(duplicate.wait(), 10) != 0, 'duplicate server unexpectedly started'
            agents = [await client('agent-a'), await client('agent-b')]
            human = await client('human')
            # Query fresh membership until the human has joined.
            for _ in range(100):
                await asyncio.sleep(0.2)
                await request(agents[0], {'op': 'members', 'channel': '#work'})
                result = await request(agents[0], {'op': 'read', 'after': agents[0].cursor, 'wait_ms': 300})
                agents[0].cursor = result['next']
                if any(e['command'] == '353' and 'human' in ' '.join(e['params']) for e in result['events']):
                    break
            else:
                raise AssertionError('the human account did not join')
            for index, agent in enumerate(agents):
                await request(agent, {'op': 'send', 'target': '#work', 'text': f'agent-{index}-visible-marker'})
            await until_all(human, [f'agent-{index}-visible-marker' for index in range(2)])
            await request(human, {'op': 'send', 'target': '#work', 'text': 'human-steering-marker'})
            anchor = None
            for agent in agents:
                event = await until(agent, 'human-steering-marker')
                assert event['tags'].get('account') == 'human', event
                anchor = event['tags']['msgid']
            # The server keeps history while the human client is away.
            await quit_client(human)
            missed = 'server-survives-human-quit'
            await request(agents[0], {'op': 'send', 'target': '#work', 'text': missed})
            await until(agents[1], missed)
            human = await client('human')
            await request(human, {'op': 'history', 'channel': '#work', 'direction': 'AFTER', 'reference': 'msgid=' + anchor, 'limit': 50})
            event = await until(human, missed)
            assert 'batch' in event['tags'], 'missed message did not arrive as history'
            for process in agents:
                await quit_client(process)
            # Separate shell invocations must share one persistent connection;
            # saved reads and confirmed sends remain usable after reconnect.
            # The human stays connected to observe the named sends' wire IDs.
            await named('connect', 'smoke', '--config', str(demo / 'agent-a.toml'))
            for _ in range(2):
                ready = await named('wait', 'smoke', '--timeout', '0')
                assert 'Unread events available' in ready, ready
            history = await named('read', 'smoke')
            assert 'human-steering-marker' in history, history
            assert await named('read', 'smoke', '--again') == history
            confirmation = await named('send', 'smoke', '#work', 'named-client-confirmed-marker')
            reference, send_id = confirmed(confirmation, '#work')
            event = await until(human, 'named-client-confirmed-marker')
            msgid = shared_msgid(reference)
            assert event['tags']['msgid'] == msgid, (reference, event)
            # The shared reference names that exact message for a reply and show.
            reply = await named('send', 'smoke', '#work', 'named-client-reply-marker', '--reply', reference)
            reply_reference, reply_id = confirmed(reply, '#work')
            event = await until(human, 'named-client-reply-marker')
            assert event['tags']['msgid'] == shared_msgid(reply_reference), (reply_reference, event)
            assert event['tags']['+reply'] == msgid, event
            shown = (await named('show', 'smoke', reply_reference, '--parent', '--channel', '#work')).splitlines()
            assert len(shown) == 2, shown
            assert shown[0].startswith(f'context {reference} #work ') and shown[0].endswith(': named-client-confirmed-marker'), shown
            assert shown[1].startswith(f'context {reply_reference} #work ') and shown[1].endswith(f' reply={reference}: named-client-reply-marker'), shown
            await quit_client(human)
            await named('disconnect', 'smoke')
            await named('connect', 'smoke', '--config', str(demo / 'agent-a.toml'))
            status = await named('status', 'smoke')
            assert f'send={send_id} target=#work: confirmed #work {reference}\n' in status, status
            assert f'send={reply_id} target=#work: confirmed #work {reply_reference}\n' in status, status
            await named('disconnect', 'smoke')
            print('PASS: installed named client, non-consuming readiness, durable read replay, shared-reference send, reply and show, and explicit reconnect')
            server.terminate()
            assert await asyncio.wait_for(server.wait(), 12) in (0, -15)
            with socket.socket() as sock:
                assert sock.connect_ex(('127.0.0.1', port)) != 0, 'server leaked after shutdown'
            print('PASS: two agents and the human account, steering, history after reconnect and server shutdown')
        finally:
            if named_state.exists():
                # A named worker deliberately outlives its initiating command.
                # Explicitly stop it before the server/temp directory on failure.
                with contextlib.suppress(Exception):
                    await named('disconnect', 'smoke')
            for child in reversed(children):
                if child.returncode is None:
                    child.terminate()
                    try:
                        await asyncio.wait_for(child.wait(), 12)
                    except asyncio.TimeoutError:
                        child.kill()
                        await child.wait()


if __name__ == '__main__':
    asyncio.run(main())
