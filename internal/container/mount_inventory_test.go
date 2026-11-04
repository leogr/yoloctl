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

func nativeMountInspectFixture(id, source string) map[string]any {
	return map[string]any{"ID": id, "Spec": map[string]any{"ociVersion": "1.3.0", "root": map[string]any{"path": "rootfs"}, "mounts": []any{map[string]any{"type": "bind", "source": source, "destination": "/selected"}}, "process": map[string]any{"env": []string{"PRIVATE=fixture-secret-never-return"}}}, "Labels": map[string]any{"nerdctl/mounts": "[]"}}
}

// The actual container.Client backend/argv and response handling run against a
// private command fixture. No real VM, containerd, source path or mount probe.
func nativeMountCommandFixture(t *testing.T, config map[string]any) (*Client, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.jsonl")
	data := filepath.Join(dir, "case.json")
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YOLOCTL_TEST_MOUNT_CASE", data)
	t.Setenv("YOLOCTL_TEST_MOUNT_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	const script = `#!/usr/bin/env python3
import json,os,sys,subprocess,time
from pathlib import Path
case=json.loads(Path(os.environ['YOLOCTL_TEST_MOUNT_CASE']).read_text())
log=Path(os.environ['YOLOCTL_TEST_MOUNT_LOG'])
old=[json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
args=sys.argv[1:]
with log.open('a') as f:f.write(json.dumps(args)+'\n')
base=['shell','yolo-fixture','--','sudo','nerdctl']
if args[:5]!=base:sys.exit(72)
args=args[5:]
def hold_pipe():
 if not case.get('hold_pipe'):return
 sys.stdout.flush();sys.stderr.flush()
 mode=case['hold_pipe']
 child=subprocess.Popen([sys.executable,'-c','import time;time.sleep(300)'],start_new_session=True,stdout=sys.stdout if mode!='stderr' else subprocess.DEVNULL,stderr=sys.stderr if mode=='stderr' else subprocess.DEVNULL)
 Path(str(log)+'.child').write_text(str(child.pid))
 if mode=='cancel':time.sleep(300)
 sys.exit(0)
if args==['ps','-a','-q','--no-trunc']:
 n=sum(1 for row in old if row[5:6]==['ps'])
 values=case.get('lists',[])
 print(values[min(n,len(values)-1)] if values else '')
 hold_pipe()
 if case.get('fail_list_at')==n+1:sys.exit(23)
elif args[:3]==['inspect','--mode','native']:
 n=sum(1 for row in old if row[5:6]==['inspect'])
 if case.get('overflow_inspect'):print(json.dumps([{'ID':args[3],'Spec':{'ociVersion':'1.3.0','root':{'path':'rootfs'}},'ignored_padding':'x'*(8*1024*1024)}]))
 else:
  values=case.get('inspects',[])
  if values:print(values[min(n,len(values)-1)])
  else:print(json.dumps([case['entries'][cid] for cid in args[3:]]))
 hold_pipe()
 if case.get('fail_inspect_at')==n+1:sys.exit(24)
else:sys.exit(73)
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return NewWithOutput("fixture", io.Discard, io.Discard), log
}

func TestNativeMountInventoryActualAdapterUsesAllFullIDsAndNativeOCIInBothPasses(t *testing.T) {
	ids := []string{}
	entries := map[string]any{}
	for n := 0; n < 33; n++ {
		id := fmt.Sprintf("%064x", n+1)
		ids = append(ids, id)
		entries[id] = nativeMountInspectFixture(id, fmt.Sprintf("/actual-oci/%d", n))
	}
	client, log := nativeMountCommandFixture(t, map[string]any{"lists": []string{strings.Join(ids, "\n")}, "entries": entries})
	got, err := client.NativeMountInventory(t.Context())
	if err != nil {
		t.Fatal("native OCI mount snapshot refused", err)
	}
	if len(got) != 33 {
		t.Fatal("native inventory dropped stopped/unlabelled containers", len(got))
	}
	for n, entry := range got {
		if entry.ContainerID != ids[n] || entry.RootPath != "rootfs" || len(entry.Mounts) != 1 || entry.Mounts[0].Source != fmt.Sprintf("/actual-oci/%d", n) {
			t.Fatal("native inventory substituted label/summary/path evidence", entry)
		}
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixture-secret-never-return") || strings.Contains(fmt.Sprintf("%#v", got), "fixture-secret-never-return") {
		t.Fatal("native inventory returned private environment")
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args[5:])
	}
	if len(calls) != 7 {
		t.Fatal("native inventory retried or omitted complete reads", calls)
	}
	for _, n := range []int{0, 3, 6} {
		if !reflect.DeepEqual(calls[n], []string{"ps", "-a", "-q", "--no-trunc"}) {
			t.Fatal("native inventory used filtered/truncated/running-only list", calls[n])
		}
	}
	for _, n := range []int{1, 2, 4, 5} {
		if !reflect.DeepEqual(calls[n][:3], []string{"inspect", "--mode", "native"}) {
			t.Fatal("native inventory used Docker-compatible label projection", calls[n])
		}
	}
}

func TestNativeMountInventoryEmptyRequiresStableUnfilteredLists(t *testing.T) {
	client, log := nativeMountCommandFixture(t, map[string]any{"lists": []string{""}})
	got, err := client.NativeMountInventory(t.Context())
	if err != nil || len(got) != 0 {
		t.Fatal("stable explicit empty inventory refused", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 3 {
		t.Fatal("empty inventory skipped repeated complete lists", err, string(raw))
	}
}

func TestNativeMountInventoryFailedCommandCannotBecomePartialSuccessOrRetry(t *testing.T) {
	id := strings.Repeat("a", 64)
	entry, _ := json.Marshal([]any{nativeMountInspectFixture(id, "/source")})
	for _, where := range []string{"list", "inspect"} {
		t.Run(where, func(t *testing.T) {
			config := map[string]any{"lists": []string{id}, "inspects": []string{string(entry)}}
			want, calls := 23, 1
			if where == "list" {
				config["fail_list_at"] = 1
			} else {
				config["fail_inspect_at"] = 1
				want, calls = 24, 2
			}
			client, log := nativeMountCommandFixture(t, config)
			got, err := client.NativeMountInventory(t.Context())
			var exit *exec.ExitError
			if err == nil || got != nil || !errors.As(err, &exit) || exit.ExitCode() != want {
				t.Fatal("failed native inventory reply became successful/empty evidence", err, got)
			}
			raw, readErr := os.ReadFile(log)
			if readErr != nil || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != calls {
				t.Fatal("failed native inventory command was retried/probed", readErr, string(raw))
			}
		})
	}
}

func TestNativeMountInventoryRefusesChangedIDsOrOCIProjection(t *testing.T) {
	id := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	first, _ := json.Marshal([]any{nativeMountInspectFixture(id, "/first")})
	second, _ := json.Marshal([]any{nativeMountInspectFixture(id, "/second")})
	for _, change := range []string{"middle-ID", "last-ID", "mounts", "missing-inspect", "foreign-inspect", "root"} {
		t.Run(change, func(t *testing.T) {
			config := map[string]any{"lists": []string{id}, "inspects": []string{string(first)}}
			switch change {
			case "middle-ID":
				config["lists"] = []string{id, other}
			case "last-ID":
				config["lists"] = []string{id, id, other}
			case "mounts":
				config["inspects"] = []string{string(first), string(second)}
			case "missing-inspect":
				config["inspects"] = []string{"[]"}
			case "foreign-inspect":
				raw, _ := json.Marshal([]any{nativeMountInspectFixture(other, "/first")})
				config["inspects"] = []string{string(raw)}
			case "root":
				e := nativeMountInspectFixture(id, "/first")
				e["Spec"].(map[string]any)["root"] = map[string]any{"path": "/changed-root"}
				raw, _ := json.Marshal([]any{e})
				config["inspects"] = []string{string(first), string(raw)}
			}
			client, _ := nativeMountCommandFixture(t, config)
			if got, err := client.NativeMountInventory(t.Context()); err == nil || got != nil {
				t.Fatal("changing/incomplete native mount inventory became authoritative", change, err, got)
			}
		})
	}
}

func TestNativeMountInventoryRejectsAliasesMalformedEvidenceAndUnboundedOutput(t *testing.T) {
	id := strings.Repeat("a", 64)
	valid := `{"ID":"` + id + `","Spec":{"ociVersion":"1.3.0","root":{"path":"rootfs"},"mounts":[{"type":"bind","source":"/src","destination":"/dst"}]}}`
	for name, body := range map[string]string{
		"dockercompat-ID":     strings.Replace(valid, `"ID"`, `"Id"`, 1),
		"duplicate-ID":        strings.Replace(valid, `"ID":"`+id+`"`, `"ID":"`+id+`","ID":"`+id+`"`, 1),
		"Spec-alias":          strings.Replace(valid, `"Spec"`, `"spec"`, 1),
		"null-Spec":           `{"ID":"` + id + `","Spec":null}`,
		"missing-Spec":        `{"ID":"` + id + `"}`,
		"mounts-alias":        strings.Replace(valid, `"mounts"`, `"Mounts"`, 1),
		"duplicate-source":    strings.Replace(valid, `"source":"/src"`, `"source":"/src","source":"/other"`, 1),
		"source-alias":        strings.Replace(valid, `"source"`, `"Source"`, 1),
		"source-null":         strings.Replace(valid, `"source":"/src"`, `"source":null`, 1),
		"missing-root-path":   strings.Replace(valid, `"path":"rootfs"`, `"readonly":true`, 1),
		"missing-destination": strings.Replace(valid, `,"destination":"/dst"`, "", 1),
		"duplicate-entry":     valid + "," + valid,
		"trailing":            valid + `] []`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseNativeMountEntries([]byte("["+body+"]"), []string{id}); err == nil || got != nil {
				t.Fatal("invalid native OCI evidence accepted", name, got)
			}
		})
	}
	for _, list := range []string{strings.Repeat("a", 12), strings.Repeat("A", 64), id + "\n" + id, strings.Repeat(id+"\n", 1025)} {
		client, log := nativeMountCommandFixture(t, map[string]any{"lists": []string{list}})
		if got, err := client.NativeMountInventory(t.Context()); err == nil || got != nil {
			t.Fatal("invalid/unbounded CID list accepted", err)
		}
		raw, err := os.ReadFile(log)
		if err != nil || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 1 {
			t.Fatal("invalid ID list reached inspection", err)
		}
	}
	client, _ := nativeMountCommandFixture(t, map[string]any{"lists": []string{id}, "overflow_inspect": true})
	if got, err := client.NativeMountInventory(t.Context()); err == nil || got != nil {
		t.Fatal("unbounded inspection output accepted", err)
	}
}

func TestNativeMountInventoryOCIOmittedMountFieldsRemainExplicitObservation(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, mounts := range []string{"", `,"mounts":null`, `,"mounts":[]`, `,"mounts":[{"destination":"/proc"}]`} {
		raw := []byte(`[{"ID":"` + id + `","Spec":{"ociVersion":"1.3.0","root":{"path":"rootfs"}` + mounts + `}}]`)
		entries, err := parseNativeMountEntries(raw, []string{id})
		if err != nil || len(entries) != 1 {
			t.Fatal("OCI omitempty semantics became a missing-Spec inference", err)
		}
		if len(entries[0].Mounts) > 0 && !reflect.DeepEqual(entries[0].Mounts[0], NativeMount{Destination: "/proc"}) {
			t.Fatal("omitted OCI mount fields were invented", entries)
		}
	}
}

// Hide strings.Reader's WriterTo to force io.Copy's destination ReaderFrom
// selection; *os.File also exercises the optimized os/exec pipe-copy fallback.
type nativeMountReaderOnly struct{ io.Reader }

func TestNativeMountInventoryCopyDispatchRetainsOutputBound(t *testing.T) {
	for _, kind := range []string{"reader-fallback", "file-optimized"} {
		t.Run(kind, func(t *testing.T) {
			for _, payload := range []string{"12345678", "123456789abcdef"} {
				var source io.Reader = nativeMountReaderOnly{strings.NewReader(payload)}
				if kind == "file-optimized" {
					file, err := os.CreateTemp(t.TempDir(), "copy-source-")
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = file.Close() }()
					if _, err := file.WriteString(payload); err != nil {
						t.Fatal(err)
					}
					if _, err := file.Seek(0, io.SeekStart); err != nil {
						t.Fatal(err)
					}
					source = file
				}
				output := &nativeMountOutput{limit: 8}
				_, err := io.Copy(output, source)
				if len(payload) <= 8 {
					if err != nil || output.buffer.String() != payload {
						t.Fatal("bounded optimized copy lost allowed bytes", err)
					}
				} else if err == nil || output.buffer.Len() > output.limit {
					t.Fatal("optimized copy bypassed bounded Write", kind, err, output.buffer.Len())
				}
			}
			if _, promoted := any(&nativeMountOutput{}).(io.ReaderFrom); promoted {
				t.Fatal("bounded command output exposes unbounded ReaderFrom")
			}
		})
	}
}

func TestNativeMountInventoryBoundsInheritedOutputPipes(t *testing.T) {
	for _, operation := range []string{"inventory", "projection"} {
		for _, mode := range []string{"stdout", "stderr", "cancel"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				config := map[string]any{"hold_pipe": mode, "inspects": []string{operatorProjectionJSON(t, operatorProjectionFixture())}}
				client, log := nativeMountCommandFixture(t, config)
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
						t.Error("invalid owned metadata fixture group", err)
						return
					}
					if err := unix.Kill(-pid, unix.SIGKILL); err != nil && err != unix.ESRCH {
						t.Error("owned metadata fixture cleanup", err)
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
				var err error
				if operation == "inventory" {
					var result []NativeMountEntry
					result, err = client.NativeMountInventory(ctx)
					if result != nil {
						t.Fatal("incomplete inventory returned metadata")
					}
				} else {
					var result *NativeOperatorProjection
					result, err = client.NativeOperatorProjection(ctx, operatorProjectionCID, "selected")
					if result != nil {
						t.Fatal("incomplete projection returned metadata")
					}
				}
				<-ready
				if err == nil {
					t.Fatal("inherited metadata pipe became success")
				}
				if mode == "cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatal("original cancellation lost", err)
					}
				} else if !errors.Is(err, exec.ErrWaitDelay) {
					t.Fatal("pipe settlement failure lost", err)
				}
				if time.Since(start) > 4*time.Second {
					t.Fatal("metadata pipe settlement exceeded finite bound")
				}
				if _, err := os.Stat(childFile); err != nil {
					t.Fatal("owned metadata descendant never started", err)
				}
				if calls := operatorProjectionCalls(t, log); len(calls) != 1 {
					t.Fatal("failed metadata read retried or continued", calls)
				}
			})
		}
	}
}
