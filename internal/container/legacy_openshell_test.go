package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Execute the production's selected-VM shell script against private files.
// Only the fixed historical pathname/hash and uname are replaced by the fake
// adapter. No VM, old executable, download or installation runs in this test.
func retainedLegacyFixture(t *testing.T, arch string, payload []byte, kind string) (*Client, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable for selected-command fixture")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	binary := filepath.Join(dir, "legacy")
	log := filepath.Join(dir, "calls")
	good := []byte("historical executable fixture")
	switch kind {
	case "missing":
	case "symlink":
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, payload, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, binary); err != nil {
			t.Fatal(err)
		}
	default:
		mode := os.FileMode(0755)
		if kind == "nonexec" {
			mode = 0644
		}
		if err := os.WriteFile(binary, payload, mode); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256(good)
	for k, v := range map[string]string{"TEST_RETAINED_BINARY": binary, "TEST_RETAINED_LOG": log, "TEST_RETAINED_ARCH": arch, "TEST_RETAINED_HASH": hex.EncodeToString(digest[:]), "TEST_RETAINED_MODE": kind} {
		t.Setenv(k, v)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "uname"), []byte("#!/bin/sh\nprintf '%s\\n' \"$TEST_RETAINED_ARCH\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// The script runs in the Linux guest, whose GNU sha256sum it uses. On the
	// host that tool may be missing or differ (macOS), so the fixture supplies
	// the one behaviour the script needs: --check --status of "hash  path".
	const sha256sum = `#!/usr/bin/env python3
import hashlib,sys
if sys.argv[1:]!=['--check','--status']: sys.exit(64)
for line in sys.stdin.read().splitlines():
 want,path=line.split('  ',1)
 try: got=hashlib.sha256(open(path,'rb').read()).hexdigest()
 except OSError: sys.exit(1)
 if got!=want: sys.exit(1)
`
	if err := os.WriteFile(filepath.Join(dir, "sha256sum"), []byte(sha256sum), 0755); err != nil {
		t.Fatal(err)
	}
	const adapter = `#!/usr/bin/env python3
import os,sys,subprocess
from pathlib import Path
args=sys.argv[1:]
Path(os.environ['TEST_RETAINED_LOG']).write_text('\n'.join(args))
if args[:6]!=['shell','yolo-fixture','--','sudo','sh','-c'] or len(args)!=7: sys.exit(70)
if os.environ['TEST_RETAINED_MODE']=='hang':
 import time
 time.sleep(30)
script=args[6]
if '/opt/openshell/bin/openshell-sandbox' not in script: sys.exit(71)
if any(token in script for token in ['curl ', 'wget ', 'chmod ', 'mkdir ', 'mv ', 'rm ']): sys.exit(72)
script=script.replace('/opt/openshell/bin/openshell-sandbox',os.environ['TEST_RETAINED_BINARY'])
for old in ['f9f991a24d10772ad5d24ae27a8ea6baad8cac671695bd90fcd0355e0e0ad198','32ca44fe7d9e6d332f2a753c6b8a1a6117b7388281dad9b5274d23ffc67e216f']:
 script=script.replace(old,os.environ['TEST_RETAINED_HASH'])
sys.exit(subprocess.run(['sh','-c',script]).returncode)
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(adapter), 0755); err != nil {
		t.Fatal(err)
	}
	return NewWithOutput("fixture", io.Discard, io.Discard), log
}

func TestRetainedLegacyOpenShellChecksActualBytesAndGuestArchitecture(t *testing.T) {
	for _, tc := range []struct {
		arch, kind string
		good       bool
	}{{"x86_64", "regular", true}, {"aarch64", "regular", true}, {"x86_64", "wrong", false}, {"x86_64", "missing", false}, {"x86_64", "symlink", false}, {"x86_64", "nonexec", false}, {"unknown", "regular", false}} {
		t.Run(tc.arch+"/"+tc.kind, func(t *testing.T) {
			data := []byte("historical executable fixture")
			if tc.kind == "wrong" {
				data = []byte("substituted native 0.1.6 fixture")
			}
			c, log := retainedLegacyFixture(t, tc.arch, data, tc.kind)
			err := c.CheckRetainedLegacyOpenShell(t.Context())
			if (err == nil) != tc.good {
				t.Fatal("runtime availability changed", err)
			}
			if !tc.good && !errors.Is(err, ErrLegacyOpenShellUnavailable) {
				t.Fatal("migration instruction lost", err)
			}
			raw, e := os.ReadFile(log)
			if e != nil || !strings.HasPrefix(string(raw), "shell\nyolo-fixture\n--\nsudo\nsh\n-c\n") {
				t.Fatal("wrong selected VM/command", e)
			}
		})
	}
}

func TestRetainedLegacyOpenShellCancellationNeverLaunchesOrRetries(t *testing.T) {
	c, log := retainedLegacyFixture(t, "x86_64", []byte("historical executable fixture"), "hang")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.CheckRetainedLegacyOpenShell(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled admission touched VM", err)
	}
	ctx, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stop()
	began := time.Now()
	err := c.CheckRetainedLegacyOpenShell(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrLegacyOpenShellUnavailable) || time.Since(began) > 3*time.Second {
		t.Fatal("unsettled read or cancellation erased", err)
	}
}
