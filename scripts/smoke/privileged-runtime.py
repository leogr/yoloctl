#!/usr/bin/env python3
"""Owned, bounded runtime fixtures for the disposable privileged-box smoke."""
import argparse
import json
from pathlib import Path
import subprocess
import time
import uuid


HTTP_SERVER = r'''
import http.server, sys, threading
body = sys.argv[1].encode()
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
server = http.server.HTTPServer(('0.0.0.0', 8080), Handler)
thread = threading.Thread(target=server.serve_forever)
thread.start()
print('LISTENING', flush=True)
try:
    if sys.stdin.buffer.readline() != b'stop\n':
        raise RuntimeError('serving fixture lost its stop handshake')
finally:
    server.shutdown()
    thread.join()
    server.server_close()
'''


# The root fixture must escape the launcher's group and ignore HUP/TERM: a
# foreground sleep could be killed by session cleanup rather than container stop.
ROOT_LAUNCHER = r'''
import json, pathlib, signal, subprocess

def ignore_shutdown_signals():
    signal.signal(signal.SIGHUP, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)

def child_snapshot(pid):
    root = pathlib.Path('/proc') / str(pid)
    stat = (root / 'stat').read_text()
    fields = stat[stat.rindex(')') + 2:].split()
    status = dict(line.split(':', 1) for line in (root / 'status').read_text().splitlines())
    return dict(pid=pid, pgid=int(fields[2]), sid=int(fields[3]), state=fields[0],
                start_ticks=int(fields[19]), uids=[int(uid) for uid in status['Uid'].split()],
                ignored=int(status['SigIgn'].strip(), 16))

def verify_snapshot(record):
    required = (1 << (signal.SIGHUP - 1)) | (1 << (signal.SIGTERM - 1))
    if not (record['pid'] > 1 and record['pid'] == record['pgid'] == record['sid']
            and record['uids'] == [0, 0, 0, 0] and record['state'] not in ('Z', 'X')
            and record['start_ticks'] > 0 and record['ignored'] & required == required):
        raise RuntimeError('root descendant readiness not proved: ' + json.dumps(record))

def launch():
    child = subprocess.Popen(['sleep', '600'], start_new_session=True,
                             preexec_fn=ignore_shutdown_signals, close_fds=True,
                             stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                             stderr=subprocess.DEVNULL)
    try:
        # Popen's exec-error pipe has closed: setsid and SIG_IGN precede exec.
        record = child_snapshot(child.pid)
        verify_snapshot(record)
        if child.poll() is not None:
            raise RuntimeError('root descendant exited before readiness')
        record['status'] = 'READY'
        print(json.dumps(record), flush=True)
    except BaseException:
        # This is still our direct child; never signal a guessed/reused PID.
        if child.poll() is None:
            child.kill()
        child.wait(timeout=5)
        raise

if __name__ == '__main__':
    launch()
'''


def box_exec(cli, space, box, *args):
    return [cli, 'box', 'exec', box, '--space', space, '--', *args]


def remaining(deadline):
    value = deadline - time.monotonic()
    if value <= 0:
        raise TimeoutError('fixture phase deadline expired')
    return value


class ControllerInterrupted(RuntimeError):
    """The fixture must stop, not retry or silently acknowledge a receipt."""


def receipt_hint():
    return ('controller outcome may be uncertain; inspect yoloctl space operation SPACE '
            'for this smoke space and acknowledge only reviewed uncertain outcomes; '
            'do not retry the fixture automatically')


def decoded(value):
    return value.decode(errors='replace') if isinstance(value, bytes) else value


def capture(command, output, label, timeout, *, controller=False):
    interrupted = False
    if controller:
        process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True)
        forced = False
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            interrupted = True
            # The CLI forwards the first TERM to the remote command and waits
            # for its exit receipt. Do not immediately SIGKILL a control stream.
            process.terminate()
            try:
                stdout, stderr = process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                forced = True
                process.kill()
                try:
                    stdout, stderr = process.communicate(timeout=5)
                except subprocess.TimeoutExpired as error:
                    stdout, stderr = decoded(error.stdout), decoded(error.stderr)
        result = subprocess.CompletedProcess(command, process.returncode, stdout, stderr)
        record = dict(command=command, client_pid=process.pid, status=process.returncode, stdout=stdout, stderr=stderr,
                      interrupted=interrupted, forced_client_kill=forced)
    else:
        try:
            result = subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True,
                                    text=True, timeout=timeout, check=False)
            record = dict(command=command, status=result.returncode,
                          stdout=result.stdout, stderr=result.stderr)
        except subprocess.TimeoutExpired as error:
            record = dict(command=command, stdout=decoded(error.stdout), stderr=decoded(error.stderr),
                          error='command timed out')
            result = None
    with (output / (label + '.jsonl')).open('a') as log:
        log.write(json.dumps(record) + '\n')
    if interrupted:
        # Even a cooperative forwarded TERM means the probe did not finish in
        # its deadline. Fail once; never turn interruption into another exec.
        raise ControllerInterrupted(receipt_hint())
    return result


def stop_owned(process):
    if process.poll() is None:
        try:
            process.stdin.write(b'stop\n')
            process.stdin.flush()
            process.stdin.close()
            process.wait(timeout=5)
        except (BrokenPipeError, subprocess.TimeoutExpired) as error:
            # Abnormal teardown is bounded, but is never a passing phase.
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        pass
            raise ControllerInterrupted(receipt_hint()) from error
        finally:
            try:
                process.stdin.close()
            except BrokenPipeError:
                pass
    if process.returncode != 0:
        raise RuntimeError(f'serving exec exited {process.returncode}; {receipt_hint()}')


def curl_args(url):
    # These are fixture-local addresses. Ambient proxy configuration must not
    # make a proxy response look like a tested host/container route.
    return ['curl', '--noproxy', '*', '-fsS', '--connect-timeout', '1',
            '--max-time', '2', url]


def port_fixture(cli, space, box, port, output, timeout=60):
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    marker = 'privileged-port-' + uuid.uuid4().hex
    local = box_exec(cli, space, box, *curl_args('http://127.0.0.1:8080/'))
    forwarded = curl_args(f'http://127.0.0.1:{port}/')
    report = dict(status='FAIL', phase='launch', marker=marker, local_ready=False)
    process = None
    deadline = time.monotonic() + timeout
    diagnosed = False

    def diagnose():
        nonlocal diagnosed
        if diagnosed:
            return
        diagnosed = True
        # Capture while the owned listener still exists, before cleanup.
        for label, command, control in [
            ('sockets', box_exec(cli, space, box, 'ss', '-ltn'), True),
            ('box-logs', [cli, 'box', 'logs', box, '--space', space], False),
        ]:
            if control and report.get('controller_interrupted'):
                continue
            try:
                capture(command, output, label, 3, controller=control)
            except (OSError, ControllerInterrupted) as diagnostic_error:
                report[label + '_error'] = str(diagnostic_error)
                if isinstance(diagnostic_error, ControllerInterrupted):
                    report['controller_interrupted'] = True

    def wait_response(command, label):
        while True:
            budget = remaining(deadline)
            if process.poll() is not None:
                raise RuntimeError(f'HTTP serving exec exited {process.returncode}; see server logs')
            result = capture(command, output, label, min(10, budget) if command is local else min(3, budget),
                             controller=command is local)
            if result is not None and result.returncode == 0 and result.stdout == marker:
                if process.poll() is not None:
                    raise RuntimeError('HTTP serving exec died during observation')
                return
            time.sleep(min(.1, remaining(deadline)))

    try:
        with (output / 'server.stdout').open('wb') as stdout, (output / 'server.stderr').open('wb') as stderr:
            try:
                command = box_exec(cli, space, box, 'python3', '-u', '-c', HTTP_SERVER, marker)
                # Asynchronous only on the host: the guest server stays the
                # foreground SSH command, owned until this phase cleans it up.
                process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=stdout, stderr=stderr)
                report['phase'] = 'local-readiness'
                wait_response(local, 'local-http')
                report['local_ready'] = True
                report['phase'] = 'host-forward'
                wait_response(forwarded, 'host-http')
                report['forwarded'] = True
            except Exception as error:
                report['primary_error'] = str(error)
                report['controller_interrupted'] = isinstance(error, ControllerInterrupted)
                diagnose()
                raise
            finally:
                if process is not None:
                    try:
                        stop_owned(process)
                    finally:
                        report['server_exec_status'] = process.returncode
        if report.get('controller_interrupted'):
            raise ControllerInterrupted(receipt_hint())
        report['phase'] = 'listener-cleanup'
        cleanup_deadline = time.monotonic() + 5
        while True:
            result = capture(local, output, 'cleanup-http', remaining(cleanup_deadline), controller=True)
            if result is not None and result.returncode == 7:
                # curl ran inside the box and could not connect. A failed
                # box exec/timeout is not proof that the listener is gone.
                break
            time.sleep(min(.1, remaining(cleanup_deadline)))
        report.update(status='PASS', phase='complete')
        return report
    except Exception as error:
        report['error'] = f'{type(error).__name__}: {error}'
        if isinstance(error, ControllerInterrupted):
            report['controller_interrupted'] = True
        diagnose()
        if report.get('controller_interrupted'):
            report['receipt_guidance'] = receipt_hint()
            if not isinstance(error, ControllerInterrupted):
                raise ControllerInterrupted(receipt_hint()) from error
        raise
    finally:
        (output / 'port-result.json').write_text(json.dumps(report, indent=2) + '\n')


def root_fixture(cli, space, box, output):
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    report = dict(status='FAIL')
    try:
        # One launch only. A lost/timed-out reply is a failed gate, never a
        # reason to create another detached process. Space cleanup owns failure.
        command = box_exec(cli, space, box, 'sudo', '-n', 'python3', '-c', ROOT_LAUNCHER)
        result = capture(command, output, 'root-launch', 10, controller=True)
        if result is None or result.returncode != 0:
            raise RuntimeError('root descendant launch failed or is uncertain; see root-launch.jsonl')
        record = json.loads(result.stdout)
        if not isinstance(record, dict) or record.get('status') != 'READY':
            raise RuntimeError('root descendant readiness receipt missing')
        report.update(status='READY', child=record)
        print('Root descendant readiness: ' + json.dumps(record, sort_keys=True), flush=True)
        return report
    except Exception as error:
        report['error'] = f'{type(error).__name__}: {error}'
        raise
    finally:
        (output / 'root-result.json').write_text(json.dumps(report, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=['port', 'root-ready'])
    for field in ('yoloctl', 'space', 'box', 'output'):
        parser.add_argument('--' + field, required=True)
    parser.add_argument('--port', type=int)
    args = parser.parse_args()
    if args.phase == 'port':
        if args.port is None:
            parser.error('--port is required for the port phase')
        port_fixture(args.yoloctl, args.space, args.box, args.port, args.output)
        print('PASS: local HTTP readiness, exact forwarded response and listener cleanup')
    else:
        root_fixture(args.yoloctl, args.space, args.box, args.output)


if __name__ == '__main__':
    main()
