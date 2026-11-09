package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// Exercise the lifecycle paths used by chat.ensure and membership --recreate.
// Only the VM subprocess is substituted. Like a controller worker, stdout is
// the structured response, while nerdctl emits IDs/names and build progress.
func TestChatRuntimeLifecycleKeepsStructuredOutput(t *testing.T) {
	for _, action := range []string{"create", "start", "recreate-member", "build", "storage-cleanup", "start-failure"} {
		t.Run(action, func(t *testing.T) {
			cfg, callsPath := chatOutputFixture(t, action)
			response, err := os.CreateTemp(t.TempDir(), "response")
			if err != nil {
				t.Fatal(err)
			}
			oldOut := os.Stdout
			os.Stdout = response
			t.Cleanup(func() { os.Stdout = oldOut; _ = response.Close() })
			notice, err := os.CreateTemp(t.TempDir(), "notice")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = notice.Close() })
			c := newChatRuntimeClient(cfg.Space, notice)
			switch action {
			case "create":
				var policy []byte
				policy, err = agentbox.ComposePolicy(cfg)
				if err == nil {
					err = newManagedLiveApplier(c, notice).Create(t.Context(), cfg, policy)
				}
			case "start", "start-failure":
				err = startExistingAgentboxContainer(t.Context(), c, cfg)
			case "recreate-member":
				err = recreateChatMember(t.Context(), cfg.Space, managedchat.Member{ID: managedchat.NewID(), Name: cfg.Name, BoxID: cfg.ID}, notice)
			case "storage-cleanup":
				var removed int
				removed, err = nativeStorageCollector(cfg.Space, notice).images(t.Context(), nil)
				if err == nil && removed != 1 {
					t.Fatalf("expected one obsolete image removal, got %d", removed)
				}
			case "build":
				err = c.Build(t.Context(), container.BuildOptions{Tag: "fixture:1", ContextTar: strings.NewReader("")})
			}
			if (err != nil) != (action == "start-failure") {
				t.Fatalf("unexpected lifecycle result: %v", err)
			}
			if err == nil {
				// This is the same one-value response written after runtime work.
				if err := json.NewEncoder(response).Encode(managedchat.Status{}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(response.Name())
			if err != nil {
				t.Fatal(err)
			}
			if action == "start-failure" {
				if len(raw) != 0 {
					t.Fatalf("failed mutation emitted a response: %q", raw)
				}
			} else if err := json.Unmarshal(raw, new(managedchat.Status)); err != nil {
				t.Errorf("lifecycle output corrupted controller Status: %v; raw=%q", err, raw)
			}
			diagnostics, err := os.ReadFile(notice.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(diagnostics), "fixture stdout") || !strings.Contains(string(diagnostics), "fixture stderr") {
				t.Errorf("lifecycle diagnostics lost: %q", diagnostics)
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatal(err)
			}
			if action == "start" || action == "start-failure" {
				assertChatRetainedRuntimeBeforeStart(t, string(calls))
			}
			want := map[string][]string{
				"create":          {"nerdctl --cgroup-manager systemd run"},
				"start":           {"nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
				"recreate-member": {"nerdctl stop --time 30 ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "nerdctl rm ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "nerdctl --cgroup-manager systemd run"},
				"build":           {"nerdctl build -t fixture:1"},
				"storage-cleanup": {"nerdctl rmi yoloctl/agentbox-composed:obsolete"},
				"start-failure":   {"nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
			}[action]
			for _, command := range want {
				if strings.Count(string(calls), command) != 1 {
					t.Errorf("expected exactly one %q (no replay); calls=%s", command, calls)
				}
			}
		})
	}
}

// Reach the real ensure constructor, not only the lower lifecycle methods.
// The feature probe then deliberately fails its unchanged native-controller
// guard: this fixture never configures native context or touches live sockets.
func TestChatEnsureLifecycleKeepsStructuredOutput(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopped=%v", stopped), func(t *testing.T) {
			action := "ensure-absent"
			if stopped {
				action = "ensure-stopped"
			}
			old, callsPath := chatOutputFixture(t, action)
			if err := old.Delete(); err != nil {
				t.Fatal(err)
			}
			store, inventory := roomRuntimeFixture(t)
			room, err := ensureManagedRoom(store, chatRuntimeRequest{ServerBox: "coder", DefaultHuman: "person"}, inventory)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &agentbox.Config{Name: "coder", ID: room.Server.ID, Space: "default", Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir("coder"), Features: []string{"yolo.chat.skill"}, SessionCommand: managedServerArgv()}
			var boxes []boxstate.Box
			if stopped {
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
				boxes = []boxstate.Box{{Name: cfg.Name, Declaration: boxstate.DeclarationDeclared, Config: cfg, Container: &container.Container{Name: cfg.Name, Status: "Exited (0)"}}}
			}
			image, _, err := agentbox.ResolvedImage(cfg)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := agentbox.ComposePolicy(cfg)
			if err != nil {
				t.Fatal(err)
			}
			command := sandbox.EntrypointArgs(cfg.Workdir.ContainerPath(), false, "sleep", "infinity")
			inspect, err := json.Marshal([]any{map[string]any{
				"Id": strings.Repeat("f", 64), "Name": cfg.Name, "Path": command[0], "Args": command[1:], "State": map[string]any{"Running": !stopped},
				"Config": map[string]any{"Image": image, "Labels": map[string]string{agentbox.LabelAgent: "none", resources.WorkloadLabel: resources.WorkloadVersion}},
				"Mounts": []any{map[string]any{"Source": cfg.Workdir.Source, "Destination": cfg.Workdir.ContainerPath(), "RW": false}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{"drift.json": inspect, "policy.yaml": policy} {
				if err := os.WriteFile(filepath.Join(filepath.Dir(callsPath), name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			response, err := os.CreateTemp(t.TempDir(), "response")
			if err != nil {
				t.Fatal(err)
			}
			notice, err := os.CreateTemp(t.TempDir(), "notice")
			if err != nil {
				_ = response.Close()
				t.Fatal(err)
			}
			oldOut := os.Stdout
			os.Stdout = response
			t.Cleanup(func() { os.Stdout = oldOut; _ = response.Close(); _ = notice.Close() })
			err = ensureManagedServer(t.Context(), cfg.Space, store, room, boxes, notice)
			if err == nil || !strings.Contains(err.Error(), "managed server image lacks required AIRC interfaces: managed chat requires the native controller") {
				t.Fatalf("did not reach the deliberate post-lifecycle probe failure: %v", err)
			}
			raw, err := os.ReadFile(response.Name())
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != 0 {
				t.Errorf("ensure leaked lifecycle output into the response: %q", raw)
			}
			diagnostics, err := os.ReadFile(notice.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(diagnostics), "fixture stdout") || !strings.Contains(string(diagnostics), "fixture stderr") {
				t.Errorf("ensure lost lifecycle diagnostics: %q", diagnostics)
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatal(err)
			}
			if stopped {
				assertChatRetainedRuntimeBeforeStart(t, string(calls))
			}
			runs, starts := strings.Count(string(calls), "nerdctl --cgroup-manager systemd run"), strings.Count(string(calls), "nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			if (!stopped && (runs != 1 || starts != 0)) || (stopped && (runs != 0 || starts != 1)) {
				t.Errorf("unexpected ensure create/start/replay: runs=%d starts=%d", runs, starts)
			}
		})
	}
}

func chatOutputFixture(t *testing.T, action string) (*agentbox.Config, string) {
	t.Helper()
	calls := setupExistingStartFixture(t, "before start")
	cfg, err := agentbox.Load("default", "coder")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID = agentbox.NewIdentity()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	budget, _ := resources.Workloads(4 * resources.GiB)
	t.Setenv("CHAT_TEST_ACTION", action)
	script := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >> "$START_TEST_FIXTURE/calls"
case "$*" in
  *'nerdctl ps'*) if [ "$CHAT_TEST_ACTION" = storage-cleanup ] || [ "$CHAT_TEST_ACTION" = ensure-absent ]; then exit 0; fi; printf '{"Names":"coder","Status":"Exited (0)"}\n' ;;
  *'nerdctl images'*) printf '{"Repository":"yoloctl/agentbox-composed","Tag":"obsolete","ID":"sha256:%s"}\n' ;;
  *'nerdctl image inspect'*) printf '[{"Id":"sha256:%s","RepoTags":["yoloctl/agentbox-composed:obsolete"],"Config":{"Labels":{"yoloctl.kind":"composed"}}}]\n' ;;
  *'nerdctl inspect'*) cat "$START_TEST_FIXTURE/drift.json" ;;
  *'cat /sys/fs/cgroup/'*) printf '%d\nmax\n0\nMemTotal: 4194304 kB\nMemAvailable: 100 kB\n' ;;
  *'sudo cat'*) cat "$START_TEST_FIXTURE/policy.yaml" ;;
  *'binary=/opt/openshell/bin/openshell-sandbox'*'sha256sum --check --status'*) exit 0 ;;
  *'nerdctl --cgroup-manager systemd run'*|*'nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'*|*'nerdctl stop --time 30 ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'*|*'nerdctl rm ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'*|*'nerdctl build -t fixture:1'*' .'|*'nerdctl rmi yoloctl/agentbox-composed:obsolete'*)
    printf 'fixture stdout: container ID/name or build progress\n'
    printf 'fixture stderr: progress\n' >&2
    test "$CHAT_TEST_ACTION" != start-failure ;;
  *'sudo tee '*) cat >/dev/null ;;
  *'tar -xf -'*) cat >/dev/null ;;
  *'sudo socat '*) exit 1 ;;
  *' test -w '*|*'sudo mkdir -p '*|*' mkdir -p '*|*'sudo chmod '*|*'sudo chown '*|*'sudo rm -rf '*|*' rm -rf '*|*'sudo python3 -c '*|*'systemctl '*|*'buildctl debug workers'*) exit 0 ;;
  *) printf 'unexpected fixture command: %%s\n' "$*" >&2; exit 42 ;;
esac
`, strings.Repeat("a", 64), strings.Repeat("a", 64), budget.Max)
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return cfg, calls
}

// A stopped legacy fixture must pass the same read-only retained-byte guard as
// a real launch, before exercising its original lifecycle/output assertions.
func assertChatRetainedRuntimeBeforeStart(t *testing.T, calls string) {
	t.Helper()
	verified := strings.Index(calls, "binary=/opt/openshell/bin/openshell-sandbox")
	started := strings.Index(calls, "nerdctl start ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if verified < 0 || started < 0 || verified > started {
		t.Fatalf("chat legacy start skipped retained-byte verification: %s", calls)
	}
}
