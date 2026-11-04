package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Actual Client->VM command dispatch and exit/output handling. The fixture is
// only an executable adapter, not a live containerd/VM or kernel proof.
func runtimeIdentityCommandFixture(t *testing.T, config map[string]any) (*Client, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "case")
	if err := os.WriteFile(data, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YOLOCTL_TEST_RUNTIME_IDENTITIES", data)
	t.Setenv("YOLOCTL_TEST_RUNTIME_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	const script = `#!/usr/bin/env python3
import json,os,sys,subprocess,time
from pathlib import Path
case=json.loads(Path(os.environ['YOLOCTL_TEST_RUNTIME_IDENTITIES']).read_text())
log=Path(os.environ['YOLOCTL_TEST_RUNTIME_LOG'])
old=[json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
a=sys.argv[1:]
with log.open('a') as f:f.write(json.dumps(a)+'\n')
if a[:5]!=['shell','yolo-fixture','--','sudo','nerdctl']:sys.exit(72)
a=a[5:]
if a==['ps','-a','-q','--no-trunc']:
 n=sum(1 for row in old if row[5:6]==['ps'])
 values=case.get('lists',[''])
 print(values[min(n,len(values)-1)],flush=True)
 if case.get('hold_pipe'):
  mode=case['hold_pipe']
  child=subprocess.Popen([sys.executable,'-c','import time;time.sleep(300)'],start_new_session=True,stdout=sys.stdout if mode!='stderr' else subprocess.DEVNULL,stderr=sys.stderr if mode=='stderr' else subprocess.DEVNULL)
  Path(str(log)+'.child').write_text(str(child.pid))
  if mode=='cancel':time.sleep(300)
  sys.exit(0)
 if case.get('fail_list_at')==n+1:sys.exit(23)
elif a[:3]==['inspect','--mode','dockercompat']:
 n=sum(1 for row in old if row[5:6]==['inspect'])
 if case.get('overflow'):print('x'*(8*1024*1024+1))
 elif 'inspects' in case:
  values=case['inspects'];print(values[min(n,len(values)-1)])
 else:print(json.dumps([{'Id':cid,'Name':case.get('names',{}).get(cid,'other')} for cid in a[3:]]))
 if case.get('fail_inspect_at')==n+1:sys.exit(24)
else:sys.exit(73)
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return NewWithOutput("fixture", io.Discard, io.Discard), log
}

func TestRuntimeIdentityInventoryFullScopeAndLookupCopies(t *testing.T) {
	ids := []string{}
	names := map[string]string{}
	for n := 1; n <= 33; n++ {
		id := fmt.Sprintf("%064x", n)
		ids = append(ids, id)
		names[id] = fmt.Sprintf("external-%d", n)
	}
	names[ids[2]] = "/selected"
	names[ids[32]] = "selected"
	names[ids[3]] = ""
	client, log := runtimeIdentityCommandFixture(t, map[string]any{"lists": []string{strings.Join(ids, "\n")}, "names": names})
	observation, err := client.RuntimeIdentityInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found, err := observation.BoxCandidates("selected")
	if err != nil || !reflect.DeepEqual(found, []string{ids[2], ids[32]}) {
		t.Fatal("name evidence omitted", found, err)
	}
	found[0] = "changed-copy"
	again, _ := observation.BoxCandidates("selected")
	if again[0] != ids[2] {
		t.Fatal("caller mutated captured identities")
	}
	absent, err := observation.BoxCandidates("absent")
	if err != nil || len(absent) != 0 {
		t.Fatal("qualified observation unavailable", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(rows) != 7 {
		t.Fatal("unexpected retry/query count", len(rows))
	}
	for index, row := range rows {
		var args []string
		if json.Unmarshal([]byte(row), &args) != nil {
			t.Fatal("bad fixture transcript")
		}
		args = args[5:]
		if index == 0 || index == 3 || index == 6 {
			if !reflect.DeepEqual(args, []string{"ps", "-a", "-q", "--no-trunc"}) {
				t.Fatal(args)
			}
		} else if !reflect.DeepEqual(args[:3], []string{"inspect", "--mode", "dockercompat"}) {
			t.Fatal(args)
		}
	}
}

func TestRuntimeIdentityInventoryRejectsEveryFailedOrChangedRead(t *testing.T) {
	id := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	good := `[{"Id":"` + id + `","Name":"other"}]`
	cases := map[string]map[string]any{
		"first-list-failure":     {"lists": []string{id}, "fail_list_at": 1},
		"middle-list-failure":    {"lists": []string{id}, "fail_list_at": 2},
		"last-list-failure":      {"lists": []string{id}, "fail_list_at": 3},
		"first-inspect-failure":  {"lists": []string{id}, "inspects": []string{good}, "fail_inspect_at": 1},
		"second-inspect-failure": {"lists": []string{id}, "inspects": []string{good}, "fail_inspect_at": 2},
		"middle-set-change":      {"lists": []string{id, other}},
		"last-set-change":        {"lists": []string{id, id, other}},
		"name-change":            {"lists": []string{id}, "inspects": []string{good, strings.ReplaceAll(good, "other", "selected")}},
		"missing-row":            {"lists": []string{id}, "inspects": []string{`[]`}},
		"foreign-row":            {"lists": []string{id}, "inspects": []string{strings.ReplaceAll(good, id, other)}},
		"duplicate-list":         {"lists": []string{id + "\n" + id}},
		"short-id":               {"lists": []string{id[:12]}},
		"too-many-ids":           {"lists": []string{strings.Repeat(id+"\n", nativeMountInventoryLimit+1)}},
		"overflow":               {"lists": []string{id}, "overflow": true},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			client, _ := runtimeIdentityCommandFixture(t, config)
			observation, err := client.RuntimeIdentityInventory(t.Context())
			if err == nil || observation != nil {
				t.Fatal("uncertainty became captured absence", err)
			}
		})
	}
}

func TestRuntimeIdentityInventoryLookupKeepsIDAliasCandidates(t *testing.T) {
	id := strings.Repeat("a", 64)
	client, _ := runtimeIdentityCommandFixture(t, map[string]any{"lists": []string{id}, "names": map[string]string{id: "unrelated"}})
	observation, err := client.RuntimeIdentityInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{id[:12], id[:24]} {
		found, err := observation.BoxCandidates(name)
		if err != nil || !reflect.DeepEqual(found, []string{id}) {
			t.Fatal("ID lookup candidate became missing", name, found, err)
		}
	}
	if _, err := observation.BoxCandidates("../selected"); err == nil {
		t.Fatal("invalid selected box accepted")
	}
	if _, err := observation.BoxCandidates(id); err == nil {
		t.Fatal("overlong selected box accepted")
	}
}

func TestRuntimeIdentityInventoryClosedIdentityDecoder(t *testing.T) {
	id := strings.Repeat("a", 64)
	good := `{"Id":"` + id + `","Name":"selected"}`
	for _, raw := range []string{
		`null`, `[]`, `[` + good + `,` + good + `]`,
		`[{"Id":"` + id + `"}]`, `[{"Id":"` + id + `","Name":null}]`,
		`[{"Id":"` + id + `","Name":1}]`,
		`[{"Id":"` + id + `","Name":"selected","Name":"other"}]`,
		`[{"Id":"` + id + `","Name":"selected","name":"other"}]`,
		`[{"name":"other","Name":"selected","Id":"` + id + `"}]`,
		`[{"Id":"` + id + `","ID":"` + id + `","Name":"other"}]`,
		`[` + good + `] []`,
	} {
		if got, err := decodeRuntimeIdentityBatch([]byte(raw), []string{id}); err == nil || got != nil {
			t.Fatal("invalid ID/name became evidence", raw)
		}
	}
	for _, name := range []string{`""`, ` "" `, `"foreign/containerd/name"`} {
		if _, err := decodeRuntimeIdentityBatch([]byte(`[{"Id":"`+id+`","Name":`+name+`}]`), []string{id}); err != nil {
			t.Fatal("unnamed/foreign container lost from scope", err)
		}
	}
}

func TestRuntimeIdentityInventoryEmptyRequiresSuccessfulCompleteLists(t *testing.T) {
	client, log := runtimeIdentityCommandFixture(t, map[string]any{})
	got, err := client.RuntimeIdentityInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := got.BoxCandidates("selected"); err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	raw, _ := os.ReadFile(log)
	if len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 3 {
		t.Fatal("empty namespace not fully observed")
	}
	for _, unavailable := range []*RuntimeIdentityInventory{nil, {}} {
		if _, err := unavailable.BoxCandidates("selected"); err == nil {
			t.Fatal("uncaptured empty accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if p, err := client.RuntimeIdentityInventory(ctx); err == nil || p != nil {
		t.Fatal("cancelled read accepted")
	}
}

func TestRuntimeIdentityInventoryBoundsInheritedOutputPipes(t *testing.T) {
	for _, mode := range []string{"stdout", "stderr", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			client, log := runtimeIdentityCommandFixture(t, map[string]any{"hold_pipe": mode})
			childFile := log + ".child"
			t.Cleanup(func() {
				raw, err := os.ReadFile(childFile)
				if errors.Is(err, os.ErrNotExist) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				pid, err := strconv.Atoi(string(raw))
				if err != nil || pid <= 1 {
					t.Error("invalid owned fixture group", err)
					return
				}
				// Only the new fixture's independently sessioned, retained pipe child.
				if err := unix.Kill(-pid, unix.SIGKILL); err != nil && err != unix.ESRCH {
					t.Error("owned pipe fixture cleanup", err)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ready := make(chan struct{})
			if mode == "cancel" {
				go func() {
					defer close(ready)
					for {
						if _, err := os.Stat(childFile); err == nil {
							cancel()
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(10 * time.Millisecond):
						}
					}
				}()
			} else {
				close(ready)
			}
			start := time.Now()
			observation, err := client.RuntimeIdentityInventory(ctx)
			<-ready
			if err == nil || observation != nil {
				t.Fatal("incomplete inherited pipe became empty inventory", err)
			}
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("original cancellation lost", err)
				}
			} else if !errors.Is(err, exec.ErrWaitDelay) {
				t.Fatal("pipe settlement failure lost", err)
			}
			if time.Since(start) > 4*time.Second {
				t.Fatal("pipe settlement exceeded finite bound")
			}
			if _, err := os.Stat(childFile); err != nil {
				t.Fatal("descendant fixture never started", err)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 1 {
				t.Fatal("failed read retried or continued")
			}
		})
	}
}
