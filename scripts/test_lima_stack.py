"""Hermetic Lima host-runner routing/cleanup tests. No VM execution."""
import contextlib
import errno
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import shlex
import subprocess
import time
import sys
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('lima_stack', Path(__file__).parent / 'smoke/lima_stack.py')
STACK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(STACK)

FAKE = r'''#!/usr/bin/env python3
import hashlib, json, os, pathlib, shutil, subprocess, sys, time
p=pathlib.Path
args=sys.argv[1:]; tool=p(sys.argv[0]).name
mode=os.environ.get('STACK_CASE','pass')
root=p(os.environ.get('SMOKE_ROOT',os.environ['STACK_EXISTING']))
leaf='yoloctl'
config=p(os.environ['YOLOCTL_HOME']) if os.environ.get('YOLOCTL_HOME') else p(os.environ['XDG_CONFIG_HOME'])/leaf
space=os.environ.get('SMOKE_SPACE','yolo')
# All child commands must receive EOF, including the reused operator smoke.
assert sys.stdin.read()==''
with open(os.environ['STACK_AUDIT'],'a') as f:
 f.write(json.dumps({'tool':tool,'home':os.environ['HOME'],'args':args,'config':str(config),'env':{k:v for k,v in os.environ.items() if k.startswith(('SMOKE_','YOLOCTL','XDG_','LIMA_','GO'))}})+'\n')
if tool=='git':
 print('a'*40 if args==['rev-parse','HEAD'] else ('fix/lima-container-stack' if '--abbrev-ref' in args else ''))
elif tool=='go':
 selected=os.environ.get('GOTOOLCHAIN')
 version=os.environ.get('STACK_GO_HOST','go1.26.4') if selected=='local' else selected
 if selected!='local':
  if mode=='go-download-fail':
   print('go: toolchain download failed: checksum service unavailable',file=sys.stderr); sys.exit(1)
  cache=p(os.environ['GOMODCACHE'])/'cache/download/golang.org/toolchain'
  cache.mkdir(parents=True,exist_ok=True); (cache/'verified-toolchain').write_text(selected)
  if mode=='go-too-old': version='go1.26.4'
 if args==['env','GOVERSION']: print(version)
 elif args==['version']: print('go version '+version+' darwin/arm64')
 else: sys.exit(19)
elif tool=='make':
 dest=p(args[0].split('=',1)[1]); dest.mkdir()
 for name in ['yoloctl','yoloctl-linux-amd64','yoloctl-linux-arm64','yoloctl-vm-init-linux-amd64']:
  shutil.copyfile(__file__,dest/name); (dest/name).chmod(0o700)
elif tool=='limactl':
 if args==['--version']: print('limactl version 2.2.1')
 elif args[:3]==['nested','space','create']:
  url='https://github.com/containerd/nerdctl/releases/download/v2.3.5/nerdctl-full-2.3.5-linux-arm64.tar.gz'
  cache=p(os.environ['HOME'])/'Library/Caches/lima/download/by-url-sha256'/hashlib.sha256(url.encode()).hexdigest()
  cache.mkdir(parents=True,exist_ok=True); (cache/'data').write_bytes(b'fake archive')
 elif args[:1]==['nested']: pass
 elif '--json' in args:
  if not (root/'destroyed').exists(): print(json.dumps({'name':'yolo-'+space,'status':'Running'}))
 else:
  print(json.dumps({'name':'yolo-'+space,'status':'Stopped' if mode=='stopped' else 'Running','arch':'aarch64',
   'containerd':{'system':True,'user':False,'archives':[
    {'arch':'x86_64','location':'https://github.com/containerd/nerdctl/releases/download/v2.3.5/nerdctl-full-2.3.5-linux-amd64.tar.gz','digest':'sha256:b697295c623639734aaab737523c808fd3cc8d3046039fd94fff1744e4c317aa'},
    {'arch':'aarch64','location':'https://github.com/containerd/nerdctl/releases/download/v2.3.5/nerdctl-full-2.3.5-linux-arm64.tar.gz','digest':'sha256:6e4b687f1d138e750a3c8372abc0f81d3d7490b6359c48c0562fc7dfe98859b2'}]},
   'mounts':[{'location':os.environ.get('STACK_MOUNT',str(root/'workdir'))}],'disks':[]}))
elif tool=='yoloctl':
 subprocess.run(['limactl','nested',*args],stdout=subprocess.DEVNULL,check=True)
 if args==['version','--json']:
  roots={'config_root':str(config),'cache_root':str(config/'cache')}
  if mode=='roots-mismatch': roots['config_root']=str(root/'unexpected')
  if mode=='roots-missing': roots={}
  print(json.dumps({'bundle':'test-bundle',**roots}))
 elif args[:2]==['space','create']:
  for d in [config/'lima'/('yolo-'+space),config/'lima/_disks/data',config/'spaces'/space]: d.mkdir(parents=True,exist_ok=True)
  (config/'spaces'/space/'space.yaml').write_text('fixture\n')
  if mode.startswith('create-fail'): sys.exit(29)
  if mode=='create-timeout': time.sleep(10)
 elif args[:2]==['space','destroy']:
  if mode.endswith('destroy-fail'): sys.exit(67)
  for d in [config/'lima'/('yolo-'+space),config/'lima/_disks/data',config/'spaces'/space]:
   if d.exists(): shutil.rmtree(d)
  (root/'destroyed').touch()
 elif args[:2]==['space','recreate']: (root/'recreated').touch()
 elif args[:2]==['space','show']:
  if mode=='unavailable': sys.exit(12)
  print(json.dumps({'declarations_available':True,'account_disk_id':'disk-id','account_disk_uuid':'disk-uuid'}))
 elif args[:2]==['space','list']: print(json.dumps([{'name':space,'status':'Running'}]))
 elif args[:2]==['account','list']: print(json.dumps([{'name':'default'}]))
 elif args[:2]==['box','list']:
  print(os.environ.get('STACK_BOXES',json.dumps([{'name':'fixture','state':'running','dimensions':{'runtime':'running','session':'none','drift':'in_sync'}}])))
 elif args[:2]==['box','inspect']: print(json.dumps({'runtime':{'runtime_mode':'confined','running':True}}))
 elif args[:2]==['space','ssh']:
  guest=args[args.index('--')+1:]
  if guest[0]=='bash':
   v={'id':('b' if mode=='replace-id' and (root/'pty-granted').exists() else 'a')*64,'privileged':False}
   if mode in ('absent','null'): v['privileged']=None
   if mode=='string': v['privileged']='false'
   if mode=='true': v['privileged']=True
   print(json.dumps(v))
  elif '--version' in guest:
   versions={'nerdctl':'2.3.5','containerd':'2.3.3','runc':'1.5.1','buildctl':'0.31.2','buildkitd':'0.31.2'}
   value=versions[guest[0]]
   if mode=='post-recreate-version' and (root/'recreated').exists(): value='0.0.1'
   print(guest[0]+' version v'+value)
  else: print('active')
 elif args[:3]==['box','exec','smoke-operator']:
  if '--capability' in args: (root/'pty-granted').touch()
 elif args[:2]==['box','exec']:
  if 'python3' in args and 'LIMA_STACK_PTY_DENIED' in args[-1]:
   if (root/'pty-granted').exists(): print('LIMA_STACK_PTY_ALLOWED:/dev/pts/2')
   else:
    print('offline transport' if mode=='wrong-denial' else 'LIMA_STACK_PTY_DENIED:13')
    sys.exit(73)
  elif 'cat' in args: print('lima-stack-marker')
'''


class LimaStackTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='lima-stack-tests-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.repo = self.root / 'repo'
        self.tools = self.root / 'tools'
        self.scratch = self.root / 'scratch'
        self.home = self.root / 'home'
        for p in (self.repo / 'scripts/smoke', self.tools, self.scratch, self.home): p.mkdir(parents=True)
        (self.repo / 'go.mod').write_text('module fixture\n\ngo 1.27\n')
        for name in ('git', 'make', 'limactl', 'ssh', 'go'):
            (self.tools / name).write_text(FAKE); (self.tools / name).chmod(0o700)
        (self.repo / 'scripts/smoke/controller-operator.sh').write_text('''#!/bin/bash
set -euo pipefail
if [[ "${STACK_CASE:-}" == operator-fail ]]; then exit 31; fi
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
''')
        self.sentinel = self.home/'Library/Caches/lima/sentinel'
        self.sentinel.parent.mkdir(parents=True)
        self.sentinel.write_text('keep original home')
        self.existing = self.root / 'existing'
        self.existing.mkdir()
        self.audit = self.root / 'audit.jsonl'
        self.env = {k:v for k,v in os.environ.items() if not k.startswith(('YOLOCTL', 'SMOKE_'))}
        self.env.update(PATH=str(self.tools) + os.pathsep + self.env['PATH'], HOME=str(self.home),
                        STACK_EXISTING=str(self.existing), STACK_AUDIT=str(self.audit),
                        XDG_CACHE_HOME=str(self.root/'normal-cache'), XDG_CONFIG_HOME=str(self.root/'normal-config'),
                        LIMA_HOME=str(self.root/'normal-lima'), SMOKE_ROOT=str(self.existing), SMOKE_SPACE='yolo',
                        LIMA_STACK_CHILD_HOME=str(self.existing), YOLOCTL_HOME=str(self.existing), YOLOCTL='/do/not/use', SMOKE_KEEP_ON_FAILURE='1')
        self.contexts = contextlib.ExitStack()
        self.addCleanup(self.contexts.close)
        self.contexts.enter_context(patch.dict(os.environ, self.env, clear=True))
        real_digest=STACK.file_digest
        self.contexts.enter_context(patch.object(STACK,'file_digest',lambda p: STACK.PINS['aarch64'][1] if p.name=='data' else real_digest(p)))
        self.contexts.enter_context(patch.object(STACK, 'REPO', self.repo))
        self.contexts.enter_context(patch.object(STACK, 'TEMP_ROOT', self.scratch))
        self.contexts.enter_context(patch.object(STACK.platform, 'system', return_value='Darwin'))
        self.contexts.enter_context(patch.object(STACK.Path, 'home', return_value=self.home))
        # Linux test hosts may themselves have a native context; only mock its
        # existence for the host preflight. All operation paths stay real.
        real_exists = Path.exists
        self.contexts.enter_context(patch.object(Path, 'exists', lambda p: False if str(p) in
            ('/run/yoloctl/operator','/etc/yoloctl/controller.json') else real_exists(p)))

    def run_gate(self, *args):
        existing = set(self.scratch.glob('ylr-*/report.json'))
        with contextlib.redirect_stdout(io.StringIO()):
            status = STACK.main(list(args))
        reports = list(set(self.scratch.glob('ylr-*/report.json')) - existing)
        report = json.loads(reports[-1].read_text()) if reports else None
        return status, report

    def calls(self):
        return [json.loads(x) for x in self.audit.read_text().splitlines()]

    def test_default_order_owned_environment_and_one_bundle(self):
        status, report = self.run_gate()
        self.assertEqual(status, 0, report)
        self.assertEqual([p['phase'] for p in report['phases']], ['setup','toolchain','build','create','operator','lifecycle','images','cleanup'])
        self.assertEqual(report['result'], 'PASS')
        self.assertFalse(Path(report['fixture']).exists())
        calls = self.calls()
        self.assertEqual(sum(c['tool']=='make' for c in calls), 1)
        cli = [c for c in calls if c['tool']=='yoloctl']
        self.assertTrue(all(c['env']['SMOKE_SPACE']=='stack-qa' for c in cli))
        self.assertTrue(all(c['config'].startswith(report['fixture']+'/') for c in cli))
        self.assertTrue(all(c['env']['LIMA_HOME']==report['lima_home'] for c in cli))
        self.assertTrue(all(c['env']['YOLOCTL_HOME'] == report['fixture'] + '/home' for c in cli))
        self.assertTrue(all('SMOKE_KEEP_ON_FAILURE' not in c['env'] for c in cli))
        self.assertEqual(report['config_root'], report['fixture'] + '/home')
        self.assertEqual(report['cache_root'], report['config_root'] + '/cache')
        self.assertEqual(list(self.existing.iterdir()), [])
        self.assertEqual(sum(c['args'][:2]==['box','reconcile'] for c in cli), 1)
        self.assertEqual(report['bundle'], 'test-bundle')
        self.assertEqual(self.sentinel.read_text(),'keep original home')
        self.assertEqual(os.environ['HOME'],str(self.home))
        self.assertTrue(all(c['home']==str(self.home) for c in calls if c['tool']!='limactl'))
        nested=[c for c in calls if c['tool']=='limactl' and c['args'][:1]==['nested']]
        self.assertTrue(nested)
        self.assertTrue(all(c['home']==report['lima_child_home'] for c in nested))
        self.assertTrue(all(c['home']==report['lima_child_home'] for c in calls if c['tool']=='limactl' and c['args'][:1]==['list']))
        self.assertTrue(report['download_evidence']['archive'].startswith(report['lima_download_cache']+'/'))
        self.assertIn(['nested','space','destroy','stack-qa','--force'],[c['args'] for c in nested])

    def test_root_query_refuses_mismatch_or_missing_fields_before_vm_work(self):
        for mode in ('roots-mismatch', 'roots-missing'):
            with self.subTest(mode=mode):
                os.environ['STACK_CASE'] = mode
                offset = len(self.calls()) if self.audit.exists() else 0
                status, report = self.run_gate()
                self.assertNotEqual(status, 0, report)
                self.assertFalse(Path(report['fixture']).exists())
                cli = [c for c in self.calls()[offset:] if c['tool'] == 'yoloctl']
                self.assertEqual([c['args'] for c in cli], [['version', '--json']])

    def test_owned_home_survives_nested_common_setup_in_both_channels(self):
        # Exercise the actual shared entry used by controller-operator.sh.
        source = Path(__file__).parent / 'smoke/common.sh'
        shutil.copyfile(source, self.repo / 'scripts/smoke/common.sh')
        (self.repo / 'scripts/smoke/controller-operator.sh').write_text('''#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
smoke_init
[[ "$YOLOCTL_HOME" == "$SMOKE_ROOT/home" ]]
[[ "$SMOKE_CONFIG_ROOT" == "$YOLOCTL_HOME" ]]
smoke_ensure_space
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
''')
        for channel in ('dev', 'release'):
            with self.subTest(channel=channel):
                os.environ['STACK_CHANNEL'] = channel
                offset = len(self.calls()) if self.audit.exists() else 0
                status, report = self.run_gate()
                self.assertEqual(status, 0, report)
                cli = [c for c in self.calls()[offset:] if c['tool'] == 'yoloctl']
                self.assertTrue(all(c['env']['YOLOCTL_HOME'] == report['config_root'] for c in cli))
                self.assertEqual(list(self.existing.iterdir()), [])
                self.assertFalse(Path(report['fixture']).exists())

    def test_old_host_go_selects_module_toolchain_in_owned_caches_before_build(self):
        # Model the host failure: installed Go 1.26.4, module language 1.27.
        os.environ.update(STACK_GO_HOST='go1.26.4', GOTOOLCHAIN='local', GOENV='/host/go-env',
                          GOSUMDB='off', GOPROXY='https://do-not-use.invalid')
        status, report = self.run_gate()
        self.assertEqual(status, 0, report)
        self.assertEqual(report['go_toolchain']['minimum'], '1.27')
        self.assertEqual(report['go_toolchain']['selected'], 'go1.27.0')
        self.assertIn('go1.26.4', report['go_toolchain']['host'])
        self.assertEqual(report['go_toolchain']['active'], 'go1.27.0')
        calls = self.calls()
        go = [c for c in calls if c['tool'] == 'go']
        self.assertEqual([c['args'] for c in go], [['version'], ['env', 'GOVERSION'], ['version']])
        self.assertEqual(go[0]['env']['GOTOOLCHAIN'], 'local')
        self.assertTrue(all(c['env']['GOTOOLCHAIN'] == 'go1.27.0' for c in go[1:]))
        build = next(c for c in calls if c['tool'] == 'make')
        self.assertEqual(build['env']['GOTOOLCHAIN'], 'go1.27.0')
        self.assertLess(calls.index(go[-1]), calls.index(build))
        for c in [*go, build]:
            self.assertEqual(c['env']['GOENV'], 'off')
            self.assertNotIn('GOSUMDB', c['env'])
            self.assertNotIn('GOPROXY', c['env'])
            for key in ('GOCACHE', 'GOMODCACHE', 'GOPATH'):
                self.assertTrue(c['env'][key].startswith(report['fixture'] + '/'))
        self.assertTrue(report['go_toolchain']['ready'])
        (self.repo / 'go.mod').write_text('module fixture\n\ngo 1.27 // language minimum\ntoolchain go1.27.3\n')
        self.assertEqual(STACK.module_toolchain(), ('1.27', 'go1.27.3'))
        (self.repo / 'go.mod').write_text('module fixture\n\ngo 1.27\ntoolchain go1.26.4\n')
        with self.assertRaisesRegex(STACK.Failure, 'toolchain is older'):
            STACK.module_toolchain()

    def test_unavailable_or_old_go_fails_preflight_before_build_or_vm_work(self):
        for mode in ('go-download-fail', 'go-too-old'):
            with self.subTest(mode=mode):
                os.environ['STACK_CASE'] = mode
                offset = len(self.calls()) if self.audit.exists() else 0
                status, report = self.run_gate()
                self.assertNotEqual(status, 0)
                self.assertIn('Go preflight failed: host go version go1.26.4', report['error'])
                self.assertIn('go.mod requires >= 1.27', report['error'])
                self.assertIn('selected go1.27.0', report['error'])
                self.assertIn('Install go1.27.0 on PATH or permit Go standard proxy/checksum downloads', report['error'])
                self.assertFalse(report['go_toolchain']['ready'])
                self.assertFalse(any(c['tool'] in ('make', 'yoloctl') for c in self.calls()[offset:]))
                self.assertFalse(Path(report['fixture']).exists())

    def test_wrapper_preserves_exact_argv_and_rejects_recursion(self):
        runner=STACK.Runner()
        with contextlib.redirect_stdout(io.StringIO()): runner.setup()
        args=['nested','argument with spaces','$(do not execute)','literal "quote"']
        runner.command(['limactl',*args])
        self.assertEqual(self.calls()[-1]['args'],args)
        self.assertEqual(self.calls()[-1]['home'],runner.report['lima_child_home'])
        self.assertEqual(os.environ['HOME'],str(self.home))
        runner.cleanup()
        (self.tools/'limactl').write_text('#!/bin/sh\n# LIMA_STACK_WRAPPER_V1\nexit 0\n')
        status,report=self.run_gate()
        self.assertNotEqual(status,0)
        self.assertIn('recursive Lima wrapper refused',report['error'])

    def test_phase_failures_cleanup_and_preserved_status(self):
        for mode, expected in [('create-fail',29), ('create-fail-destroy-fail',29), ('operator-fail',31), ('wrong-denial',1), ('post-recreate-version',1), ('replace-id',1),
                               ('null',1), ('absent',1), ('string',1), ('true',1)]:
            with self.subTest(mode=mode):
                os.environ['STACK_CASE']=mode
                status, report = self.run_gate()
                self.assertEqual(status, expected, report)
                self.assertEqual(report['result'], 'FAIL')
                self.assertEqual(report['phases'][-1]['phase'], 'cleanup')
                self.assertNotIn('images',[p['phase'] for p in report['phases']])
                self.assertEqual(Path(report['fixture']).exists(), mode.endswith('destroy-fail'))

    def test_cleanup_removes_read_only_module_cache_without_following_external_links(self):
        for created, destroy_fails in ((False, False), (True, False), (True, True)):
            with self.subTest(created=created, destroy_fails=destroy_fails):
                runner = STACK.Runner()
                with contextlib.redirect_stdout(io.StringIO()): runner.setup()
                if created:
                    runner.toolchain()
                    runner.build()
                    runner.create()
                module = runner.root / 'go-mod/example.test/readonly@v1.0.0'
                package = module / 'nested'
                package.mkdir(parents=True)
                payload = package / 'source.go'
                payload.write_text('package fixture\n')
                payload.chmod(0o444)
                outside = self.root / ('external-cache-' + str(created) + str(destroy_fails))
                outside.mkdir()
                sentinel = outside / 'keep'
                sentinel.write_text('outside fixture')
                sentinel.chmod(0o444)
                (module / 'external-link').symlink_to(outside, target_is_directory=True)
                outside.chmod(0o555)
                for directory in (package, module, module.parent): directory.chmod(0o555)
                self.assertEqual(stat.S_IMODE(module.stat().st_mode), 0o555)
                if os.geteuid() != 0:
                    with self.assertRaises(PermissionError): payload.unlink()
                if destroy_fails:
                    runner.env['STACK_CASE'] = 'destroy-fail'
                    with self.assertRaises(STACK.Failure): runner.cleanup()
                    self.assertTrue(runner.root.exists())
                    self.assertEqual(stat.S_IMODE(module.stat().st_mode), 0o555)
                else:
                    runner.cleanup()
                    self.assertFalse(runner.root.exists())
                self.assertEqual(sentinel.read_text(), 'outside fixture')
                self.assertEqual(stat.S_IMODE(outside.stat().st_mode), 0o555)
                self.assertEqual(stat.S_IMODE(sentinel.stat().st_mode), 0o444)
                # Only test-owned leftovers are made writable for unittest's
                # exact TemporaryDirectory teardown, after assertions above.
                outside.chmod(0o755)
                if destroy_fails:
                    for directory in (module.parent, module, package): directory.chmod(0o755)

    def test_failed_destroy_retains_fixture_binaries_and_report(self):
        os.environ['STACK_CASE']='destroy-fail'
        status, report = self.run_gate()
        self.assertEqual(status, 67)
        self.assertIn('CLEANUP_INCOMPLETE', report['cleanup'])
        self.assertTrue((Path(report['fixture'])/'bin/yoloctl').exists())
        self.assertIn(report['fixture'], report['recovery'])
        self.assertTrue((Path(report['fixture'])/'libexec/limactl').exists())
        self.assertTrue(Path(report['lima_child_home']).exists())
        self.assertIn('LIMA_STACK_CHILD_HOME=',report['recovery'])
        self.assertIn('YOLOCTL_HOME=' + report['config_root'], shlex.split(report['recovery']))

    def test_timeout_and_interrupt_reap_before_cleanup(self):
        original = STACK.Runner.command
        for mode in ('timeout','interrupt'):
            os.environ['STACK_CASE']='create-timeout'
            def command(runner, args, seconds=120, allow_failure=False):
                if list(args)[1:3]==['space','create']:
                    if mode=='interrupt':
                        import threading
                        threading.Timer(.2, lambda: os.kill(os.getpid(), signal.SIGINT)).start()
                    seconds=.4 if mode=='timeout' else 5
                return original(runner,args,seconds,allow_failure)
            with patch.object(STACK.Runner,'command',command):
                status, report = self.run_gate()
            self.assertEqual(status, 124 if mode=='timeout' else 130, report)
            self.assertFalse(Path(report['fixture']).exists(), report)

    def test_failed_concurrent_writer_settles_before_fixture_cleanup(self):
        marker = self.root / 'late-writer'
        writer = ('import pathlib,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); '
                  'time.sleep(.8); pathlib.Path(' + repr(str(marker)) + ').write_text("late")')
        script = self.repo / 'scripts/smoke/controller-operator.sh'
        script.write_text('set -eu\n' + shlex.join([sys.executable, '-c', 'raise SystemExit(31)']) + ' &\nfirst=$!\n'
                          + shlex.join([sys.executable, '-c', writer]) + ' &\nwait "$first"\nwait\n')
        status, report = self.run_gate()
        self.assertEqual(status, 31, report)
        self.assertEqual(report['phases'][-1], {'phase': 'cleanup', 'result': 'PASS', 'exit_code': 0})
        self.assertFalse(Path(report['fixture']).exists())
        time.sleep(1)
        self.assertFalse(marker.exists(), 'writer survived into/after fixture destruction')

    def test_unverified_command_cancellation_preserves_original_failure_and_fixture(self):
        os.environ['STACK_CASE'] = 'operator-fail'
        with patch.object(STACK.Runner, 'settle_command', return_value='injected unavailable group inventory'):
            status, report = self.run_gate()
        self.assertEqual(status, 31, report)
        self.assertIn('CLEANUP_INCOMPLETE', report['cleanup'])
        self.assertTrue(Path(report['fixture']).exists())
        self.assertTrue((Path(report['fixture']) / 'libexec/limactl').exists())
        self.assertTrue(report['unsettled_commands'])
        self.assertFalse(any(c['tool'] == 'yoloctl' and c['args'][:2] == ['space', 'destroy'] for c in self.calls()))

    def test_symlink_overlap_and_global_refusal(self):
        target=self.root/'target'; target.mkdir()
        link=self.root/'link'; link.symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(STACK.Failure,'symlink'): STACK.canonical(link/'new')
        with self.assertRaisesRegex(STACK.Failure,'overlap'): STACK.separated(target/'new',[target])
        runner=STACK.Runner(); runner.config=self.root/'config'
        p=runner.config/'lima/_config'; p.mkdir(parents=True)
        (p/'override.yaml').write_text('containerd: {}\n')
        with self.assertRaisesRegex(STACK.Failure,'global configuration'): runner.globals(absent=True)
        self.assertTrue((p/'override.yaml').exists())

    def test_rejects_in_box_and_unknown_arguments(self):
        with patch.object(STACK.platform,'system',return_value='Linux'):
            with self.assertRaisesRegex(STACK.Failure,'macOS host'): STACK.main([])
        os.environ['YOLOCTL_CONTROLLER_CONTEXT']='native'
        with self.assertRaisesRegex(STACK.Failure,'context refused'): STACK.main([])
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit): STACK.main(['--unknown'])
        self.assertFalse(self.audit.exists())

    def prepare_check(self):
        config=self.root/'target/custom-home'
        for p in (config/'lima', config/'spaces/yolo'):
            p.mkdir(parents=True)
        (config/'spaces/yolo/space.yaml').write_text('schema_version: 3\nname: yolo\ndisk_id: disk-id\ndisk_uuid: disk-uuid\nbundle: test-bundle\nvm:\n  backend: lima\n')
        binary=self.tools/'yoloctl'; binary.write_text(FAKE); binary.chmod(0o700)
        for name in ('yoloctl-linux-amd64','yoloctl-linux-arm64','yoloctl-vm-init-linux-amd64'):
            shutil.copyfile(binary,self.tools/name)
        output=self.root/'check-report'
        return config, binary, output

    def test_check_writes_only_report_and_does_not_start_stopped_target(self):
        config,binary,output=self.prepare_check()
        def snapshot():
            return {str(p): (p.stat().st_mtime_ns, p.read_bytes()) for p in config.rglob('*') if p.is_file()}
        before=snapshot()
        args=['--check','--space','yolo','--binary',str(binary),'--config-root',str(config),'--output',str(output)]
        status,_=self.run_gate(*args)
        self.assertEqual(status,0,(output/'report.json').read_text())
        self.assertEqual(before,snapshot())
        self.assertFalse((config/'cache').exists(), 'read-only root query created a cache')
        self.assertEqual(list(self.existing.iterdir()), [])
        calls=self.calls()
        cli=[c for c in calls if c['tool']=='yoloctl']
        self.assertTrue(all(c['env']['YOLOCTL_HOME']==str(config) for c in cli))
        self.assertEqual(json.loads((output/'report.json').read_text())['cache_root'],str(config/'cache'))
        self.assertTrue(all(c['tool'] not in ('make','go') for c in calls))
        self.assertTrue(all(c['args'][:2] not in (['space','start'],['space','create'],['space','destroy'],['space','recreate']) for c in calls))
        os.environ['STACK_CASE']='stopped'
        args[-1]=str(self.root/'stopped-report')
        status,_=self.run_gate(*args)
        self.assertNotEqual(status,0)
        self.assertIn('not already running',(self.root/'stopped-report/report.json').read_text())
        self.assertEqual(before,snapshot())
        os.environ['STACK_CASE']='unavailable'
        args[-1]=str(self.root/'unavailable-report')
        status,_=self.run_gate(*args)
        self.assertNotEqual(status,0)
        self.assertEqual(before,snapshot())

    def test_check_output_overlap_and_missing_target_refused(self):
        config,binary,output=self.prepare_check()
        os.environ['STACK_MOUNT']=str(self.root)
        args=['--check','--space','yolo','--binary',str(binary),'--config-root',str(config),'--output',str(output)]
        status,_=self.run_gate(*args)
        self.assertNotEqual(status,0)
        self.assertFalse(output.exists())
        self.assertFalse(any(c['tool']=='yoloctl' for c in self.calls()))
        args[args.index('--space')+1]='missing'
        status,_=self.run_gate(*args)
        self.assertNotEqual(status,0)
        self.assertFalse((config/'spaces/missing').exists())

    def test_check_compare_requires_versions_identity_and_expected_restarts(self):
        config,binary,output=self.prepare_check()
        args=['--check','--space','yolo','--binary',str(binary),'--config-root',str(config),'--output',str(output)]
        status,_=self.run_gate(*args)
        self.assertEqual(status,0)
        args[-1]=str(self.root/'after')
        args+=['--compare',str(output/'report.json')]
        status,_=self.run_gate(*args)
        self.assertEqual(status,0,(self.root/'after/report.json').read_text())
        previous=json.loads((output/'report.json').read_text()); previous['bootstrap']['disk_uuid']='different'
        (output/'report.json').write_text(json.dumps(previous))
        args[args.index('--output')+1]=str(self.root/'different')
        status,_=self.run_gate(*args)
        self.assertNotEqual(status,0)
        self.assertIn('disk_uuid',(self.root/'different/report.json').read_text())


    def test_check_uses_running_runtime_with_exited_lost_and_drifted_displays(self):
        config, binary, output = self.prepare_check()
        displays = ('exited', 'lost', 'drifted')
        before_boxes = [{'name': d, 'state': d, 'dimensions': {'runtime': 'running', 'session': d, 'drift': 'in_sync'}} for d in displays]
        os.environ['STACK_BOXES'] = json.dumps(before_boxes)
        args = ['--check', '--space', 'yolo', '--binary', str(binary), '--config-root', str(config), '--output', str(output)]
        status, _ = self.run_gate(*args)
        self.assertEqual(status, 0, (output / 'report.json').read_text())
        before = json.loads((output / 'report.json').read_text())
        self.assertEqual(before['desired_start_list'], sorted(displays))
        self.assertEqual(before['boxes'], before_boxes)
        # Running containers with different exited/lost/drifted display states
        # are still restarted; retain their independent display/session changes.
        after_boxes = [dict(b, state=displays[(i+1) % 3], dimensions=dict(b['dimensions'], session='none')) for i, b in enumerate(before_boxes)]
        os.environ['STACK_BOXES'] = json.dumps(after_boxes)
        args[-1] = str(self.root / 'after-running')
        args += ['--compare', str(output / 'report.json')]
        status, _ = self.run_gate(*args)
        self.assertEqual(status, 0, (self.root / 'after-running/report.json').read_text())
        after = json.loads((self.root / 'after-running/report.json').read_text())
        self.assertEqual(after['differences'], [])
        self.assertTrue(after['status_changes'])
        self.assertTrue(after['session_changes'])
        for i, display in enumerate(displays):
            with self.subTest(display=display):
                missing = [dict(b, dimensions=dict(b['dimensions'])) for b in before_boxes]
                missing[i].update(state='configured', dimensions={'runtime': 'missing', 'session': 'none'})
                os.environ['STACK_BOXES'] = json.dumps(missing)
                target = self.root / ('after-missing-' + display)
                args[args.index('--output') + 1] = str(target)
                status, _ = self.run_gate(*args)
                self.assertNotEqual(status, 0)
                after = json.loads((target / 'report.json').read_text())
                self.assertIn('desired restarts', after['differences'])
                self.assertEqual(after['runtime_changes'][display], ['running', 'missing'])

    def test_check_rejects_unknown_runtime_in_current_and_previous_reports(self):
        config, binary, output = self.prepare_check()
        args = ['--check', '--space', 'yolo', '--binary', str(binary), '--config-root', str(config), '--output', str(output)]
        status, _ = self.run_gate(*args)
        self.assertEqual(status, 0)
        for i, runtime in enumerate(('unknown', 'unavailable', 'orphaned', '', None)):
            with self.subTest(runtime=runtime):
                dims = {'session': 'exited'}
                if runtime is not None: dims['runtime'] = runtime
                os.environ['STACK_BOXES'] = json.dumps([{'name': 'fixture', 'state': 'exited', 'dimensions': dims}])
                target = self.root / ('unknown-' + str(i))
                args[-1] = str(target)
                status, _ = self.run_gate(*args)
                self.assertNotEqual(status, 0)
                self.assertIn('UNKNOWN: box runtime evidence', (target / 'report.json').read_text())
        os.environ['STACK_BOXES'] = '[null]'
        args[-1] = str(self.root / 'invalid-runtime-row')
        status, _ = self.run_gate(*args)
        self.assertNotEqual(status, 0)
        self.assertIn('UNKNOWN: invalid box inventory row', (self.root / 'invalid-runtime-row/report.json').read_text())
        del os.environ['STACK_BOXES']
        # Old reports lacking runtime evidence cannot silently become an empty
        # restart set, even if their historical desired_start_list was empty.
        previous = json.loads((output / 'report.json').read_text())
        del previous['boxes'][0]['dimensions']
        previous['desired_start_list'] = []
        (output / 'report.json').write_text(json.dumps(previous))
        args[-1] = str(self.root / 'unknown-before')
        args += ['--compare', str(output / 'report.json')]
        status, _ = self.run_gate(*args)
        self.assertNotEqual(status, 0)
        self.assertIn('UNKNOWN: box runtime evidence', (self.root / 'unknown-before/report.json').read_text())


class CommandGroupTests(unittest.TestCase):
    """Owned process-group inventory and cancellation; no yoloctl/Lima execution."""
    def test_denied_null_signal_still_checks_group_members(self):
        runner = STACK.Runner()
        for rows, alive in (('', False), ('123 123 Z\n', False),
                            ('123 123 Z+\n124 123 S\n', True),
                            ('456 456 S\n123 123 Z\n', False)):
            with self.subTest(rows=rows), \
                    patch.object(STACK.os, 'killpg', side_effect=PermissionError(errno.EPERM, 'denied')) as probe, \
                    patch.object(STACK.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, rows, '')) as inventory:
                self.assertIs(runner.group_alive(123), alive)
                probe.assert_called_once_with(123, 0)
                inventory.assert_called_once_with(['/bin/ps', '-axo', 'pid=,pgid=,stat='],
                    stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=1)

    def test_denied_null_signal_keeps_unverified_inventory_uncertain(self):
        runner = STACK.Runner()
        for status, rows, error in ((1, '', 'cannot inspect'), (0, 'bad inventory\n', 'invalid command-group')):
            with self.subTest(status=status, rows=rows), \
                    patch.object(STACK.os, 'killpg', side_effect=PermissionError(errno.EPERM, 'denied')), \
                    patch.object(STACK.subprocess, 'run', return_value=subprocess.CompletedProcess([], status, rows, '')):
                with self.assertRaisesRegex(STACK.Failure, error):
                    runner.group_alive(123)

    def test_timeout_with_denied_null_signal_still_settles_the_owned_group(self):
        with tempfile.TemporaryDirectory(prefix='lima-stack-denied-') as temp:
            runner = STACK.Runner()
            runner.output = Path(temp)
            real_killpg = os.killpg

            def killpg(pgid, sig):
                if sig == 0:
                    raise PermissionError(errno.EPERM, 'denied null signal')
                return real_killpg(pgid, sig)

            with patch.object(STACK.os, 'killpg', side_effect=killpg):
                with self.assertRaises(STACK.Failure) as raised:
                    runner.command([sys.executable, '-c', 'import time; time.sleep(10)'], seconds=.1)
            self.assertEqual(raised.exception.code, 124)
            self.assertTrue(runner.report['commands'][-1]['cancellation']['settled'])
            self.assertNotIn('unsettled_commands', runner.report)

    def test_exited_failed_leader_cannot_leave_a_delayed_writer(self):
        with tempfile.TemporaryDirectory(prefix='lima-stack-group-') as temp:
            root = Path(temp)
            runner = STACK.Runner()
            runner.output = root
            marker = root / 'late-write'
            child = ('import pathlib,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); '
                     'time.sleep(.8); pathlib.Path(' + repr(str(marker)) + ').write_text("late")')
            parent = 'import subprocess,sys; subprocess.Popen([sys.executable,"-c",' + repr(child) + ']); sys.exit(31)'
            with self.assertRaises(STACK.Failure) as raised:
                runner.command([sys.executable, '-c', parent])
            self.assertEqual(raised.exception.code, 31)
            time.sleep(1)
            self.assertFalse(marker.exists())
            self.assertTrue(runner.report['commands'][-1]['cancellation']['settled'])

    def test_exited_leader_pipes_cannot_bypass_timeout(self):
        with tempfile.TemporaryDirectory(prefix='lima-stack-pipe-') as temp:
            root = Path(temp)
            runner = STACK.Runner()  # Before report allocation: actual pipes.
            marker = root / 'late-write'
            child = ('import pathlib,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); '
                     'time.sleep(2); pathlib.Path(' + repr(str(marker)) + ').write_text("late")')
            parent = 'import subprocess,sys; subprocess.Popen([sys.executable,"-c",' + repr(child) + ']); sys.exit(0)'
            start = time.monotonic()
            with self.assertRaises(STACK.Failure) as raised:
                runner.command([sys.executable, '-c', parent], seconds=.1)
            self.assertEqual(raised.exception.code, 124)
            self.assertLess(time.monotonic() - start, 1.5)
            time.sleep(2)
            self.assertFalse(marker.exists())
            self.assertTrue(runner.report['commands'][-1]['cancellation']['settled'])


    def test_escaped_pipe_holder_retains_recovery_state_after_bounded_drain(self):
        # The timeout must strike after the leader has started its escaped
        # pipe holder. Under load interpreter startup can exceed a short budget;
        # the leader then dies first, nothing holds the pipes and cancellation
        # settles cleanly (asserted), so retry with a longer budget.
        for seconds in (.1, .4, 1.6, 6.4):
            with tempfile.TemporaryDirectory(prefix='lima-stack-drain-') as temp:
                root = Path(temp)
                runner = STACK.Runner()
                pidfile = root / 'pipe-holder.pid'
                # The holder records itself, so an escaped holder is always found.
                child = ('import os,pathlib,time; pathlib.Path(' + repr(str(pidfile))
                         + ').write_text(str(os.getpid())); time.sleep(20)')
                parent = ('import subprocess,sys; subprocess.Popen([sys.executable,"-c",'
                          + repr(child) + '],start_new_session=True)')
                escaped = False
                try:
                    start = time.monotonic()
                    with self.assertRaises(STACK.Failure) as raised:
                        runner.command([sys.executable, '-c', parent], seconds=seconds)
                    self.assertEqual(raised.exception.code, 124)
                    self.assertLess(time.monotonic() - start, seconds + 2)
                    escaped = 'unsettled_commands' in runner.report
                    if not escaped:
                        # The bounded drain completed: no process holds the pipes.
                        self.assertTrue(runner.report['commands'][-1]['cancellation']['settled'])
                        continue
                    with self.assertRaisesRegex(STACK.Failure, 'cancellation is unverified'):
                        runner.cleanup()
                    return
                finally:
                    # Exact PID of this test's explicitly created private process
                    # group, settled before TemporaryDirectory removes its paths.
                    recorded = lambda: pidfile.exists() and pidfile.read_text().isdigit()
                    deadline = time.monotonic() + 5
                    while escaped and not recorded() and time.monotonic() < deadline:
                        time.sleep(.02)
                    if recorded():
                        pgid = int(pidfile.read_text())
                        try: os.killpg(pgid, signal.SIGKILL)
                        except ProcessLookupError: pass
                        deadline = time.monotonic() + 2
                        while runner.group_alive(pgid) and time.monotonic() < deadline:
                            time.sleep(.02)
                        self.assertFalse(runner.group_alive(pgid))
                    self.assertEqual(pidfile.exists(), escaped)
        self.fail('the leader never started its pipe holder before the timeout')

if __name__ == '__main__': unittest.main()
