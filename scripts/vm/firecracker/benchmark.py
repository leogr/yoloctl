#!/usr/bin/env python3
"""Compare isolated VM adapters through a confined box command on Linux amd64."""
import argparse
import json
import os
import platform
from pathlib import Path
import statistics
import subprocess
import time
import uuid

from terminal_probe import measure


def main(args):
    cli = Path(args.cli).resolve()
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        raise RuntimeError('this comparison and its offline fixture require Linux amd64')
    if args.backend == 'firecracker' and not os.access('/dev/kvm', os.R_OK | os.W_OK):
        raise RuntimeError('requires accessible /dev/kvm; software emulation is not a timing result')
    vm_args = ['--backend', args.backend]
    if args.backend == 'firecracker':
        if args.image:
            vm_args.extend(['--vm-image', str(Path(args.image).resolve())])
    output = Path(args.output).resolve()
    output.mkdir(mode=0o700, exist_ok=False)
    project = output / 'project'
    project.mkdir()
    (project / 'expected.txt').write_text('host project\n')
    env = dict(os.environ, YOLOCTL_HOME=str(output / 'home'), XDG_CONFIG_HOME=str(output / 'config'), XDG_CACHE_HOME=str(output / 'cache'))
    roots = json.loads(subprocess.check_output([str(cli), 'version', '--json'], env=env))
    space = 'vm-' + uuid.uuid4().hex[:10]
    rows = []
    def run(*words, stdin=None):
        command_begin = time.perf_counter_ns()
        result = subprocess.run([str(cli), *words], env=env, stdin=stdin or subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=args.timeout)
        command_timings.append({'command': list(words[:3]), 'elapsed_ms': (time.perf_counter_ns()-command_begin)/1e6})
        with (output / 'commands.log').open('ab') as log:
            log.write(('\n$ ' + ' '.join(words) + '\n').encode())
            log.write(result.stdout + result.stderr)
        if result.returncode:
            raise RuntimeError(f'{words[:3]} failed ({result.returncode}); inspect {output / "commands.log"}')
        return result.stdout.decode()
    command_timings = []
    try:
        begin = time.perf_counter_ns()
        run('space', 'create', space, *vm_args,
            '--workdir', str(project), '--account', 'trial')
        create_ms = (time.perf_counter_ns() - begin) / 1e6
        if args.fixture:
            with Path(args.fixture).resolve().open('rb') as source:
                run('space', 'ssh', space, '--root', '--', 'nerdctl', 'load', stdin=source)
        initial_controller = time.perf_counter_ns()
        probe = ('import pathlib; assert pathlib.Path("expected.txt").read_text()=="host project\\n"; '
                 's=pathlib.Path("/proc/self/status").read_text(); '
                 'assert "NoNewPrivs:\\t1" in s and "Seccomp:\\t2" in s; print("YFC_MANAGED_READY")')
        import shlex
        prompt = 'python3 -c ' + shlex.quote(probe)
        run('box', 'create', 'probe', '--space', space, '--agent', 'none', '--account', 'none',
            '--image', args.box_image, '--workdir', str(project), '--capability', 'fs.pty.allocate',
            '--env', 'PROMPT_COMMAND=' + prompt, '--no-attach', '--no-autoupdate')
        initial_box_ms = (time.perf_counter_ns() - initial_controller) / 1e6
        for sample in range(args.samples):
            stop_begin = time.perf_counter_ns()
            run('space', 'stop', space)
            stop_ms = (time.perf_counter_ns()-stop_begin)/1e6
            begin = time.perf_counter_ns()
            run('space', 'start', space)
            controller = time.perf_counter_ns()
            if args.readiness == 'interactive':
                usable = measure([str(cli), 'box', 'start', 'probe', '--space', space, '--no-autoupdate'],
                                 env, output / f'terminal-{sample+1}.raw', b'YFC_MANAGED_READY', args.timeout)
                endpoint = 'request_to_managed_terminal_ms'
            else:
                run('box', 'start', 'probe', '--space', space, '--no-attach', '--no-autoupdate')
                result = run('box', 'exec', 'probe', '--space', space, '--', 'python3', '-c', probe)
                usable = time.perf_counter_ns()
                if 'YFC_MANAGED_READY' not in result:
                    raise RuntimeError('confined readiness marker missing')
                endpoint = 'request_to_confined_box_ms'
            raw = run('space', 'ssh', space, '--', 'cat', '/proc/meminfo')
            mem = {line.split(':')[0]: int(line.split()[1]) for line in raw.splitlines()}
            row = {'sample': sample + 1, 'request_to_controller_ms': (controller - begin) / 1e6,
                   endpoint: (usable - begin) / 1e6,
                   'controller_to_ready_ms': (usable-controller)/1e6, 'stop_ms': stop_ms,
                   'guest_used_mib': (mem['MemTotal'] - mem['MemAvailable']) / 1024}
            if args.backend == 'firecracker':
                state = json.loads((Path(roots['config_root']) / 'firecracker/instances' / space / 'runtime.json').read_text())
                for kind in ['helper', 'vm']:
                    status = Path(f"/proc/{state[kind]['pid']}/status").read_text()
                    rss = next(int(line.split()[1]) for line in status.splitlines() if line.startswith('VmRSS:'))
                    row[kind + '_host_rss_mib'] = rss / 1024
            rows.append(row)
            print(json.dumps(row), flush=True)
        report = {'backend': args.backend, 'mode': 'existing-space-restart-cached-box-image',
                  'initial_space_create_ms': create_ms, 'initial_box_create_ms': initial_box_ms,
                  'readiness': args.readiness, 'samples': rows, 'commands': command_timings,
                  'median_usable_ms': statistics.median(r[endpoint] for r in rows),
                  'all_within_2s': all(r[endpoint] <= 2000 for r in rows),
                  'scope': 'real controller and OpenShell-confined project read; interactive mode measures a headless managed PTY response; no provider login/model request or graphical-terminal fidelity claim'}
        (output / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
        print(f'Report: {output / "report.json"}')
    finally:
        # Delete only this generated space through normal verified lifecycle.
        # On any uncertainty retain all files for inspection and recovery.
        try:
            run('space', 'destroy', space, '--force')
        except Exception as error:
            print(f'Cleanup incomplete: {error}; retain {output}', flush=True)
            raise


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cli', default='bin/yoloctl')
    parser.add_argument('--backend', choices=['firecracker', 'lima'], default='firecracker')
    parser.add_argument('--image', help='prepared image directory to seed the isolated Firecracker image cache')
    parser.add_argument('--readiness', choices=['interactive', 'exec'], default='interactive')
    parser.add_argument('--output', required=True, help='new directory, never overwritten')
    parser.add_argument('--fixture', help='optional offline OCI archive loaded outside the timed restart')
    parser.add_argument('--box-image', default='yfc-fixture:latest')
    parser.add_argument('--samples', type=int, default=5)
    parser.add_argument('--timeout', type=float, default=300)
    args = parser.parse_args()
    if not 1 <= args.samples <= 100:
        parser.error('--samples must be between 1 and 100')
    main(args)
