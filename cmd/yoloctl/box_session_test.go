package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/session"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// sessionViewFixture declares box coder (running) for the box session view.
func sessionViewFixture(t *testing.T, cfg *agentbox.Config) *identityFixture {
	t.Helper()
	f := newIdentityFixture(t)
	cfg.SchemaVersion = agentbox.SchemaVersion
	cfg.Image = defaultAgentboxImage
	cfg.Workdir = agentbox.NewEphemeralWorkdir(cfg.Name)
	manifest, _ := layout.BoxPath("work", cfg.Name)
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := newBoxStateResolver
	t.Cleanup(func() { newBoxStateResolver = previous })
	newBoxStateResolver = func() boxstate.Resolver {
		return boxstate.Resolver{
			ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
				return []vmhost.Instance{{Name: "work", Status: "Running"}}, nil
			},
			ListContainers: func(context.Context, string) ([]container.Container, error) {
				return []container.Container{{Name: cfg.Name, Status: "Up"}}, nil
			},
			ProbeSession: func(context.Context, string, string, bool) boxstate.SessionInfo {
				t.Fatal("the session view used the boolean session probe")
				return boxstate.SessionInfo{}
			},
		}
	}
	return f
}

// runBoxSession runs the real command and decodes its JSON.
func runBoxSession(t *testing.T, args ...string) (*boxSessionView, string, error) {
	t.Helper()
	cmd := newAgentboxSessionCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"coder", "--space", "work"}, args...))
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	var view boxSessionView
	if slices.Contains(args, "--json") && out.Len() > 0 {
		if jerr := json.Unmarshal(out.Bytes(), &view); jerr != nil {
			t.Fatalf("box session JSON: %v\n%s", jerr, out.String())
		}
	}
	return &view, out.String(), err
}

// keys returns a JSON object's keys, sorted.
func keys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not an object: %s", raw)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// statusFixture is a status with every section populated.
func statusFixture() *agentsession.Status {
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	used, percent := int64(84000), 42
	return &agentsession.Status{
		Provider: "claude", Policy: "last (default)", Tracked: true,
		ID: "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", AssignedAt: &at, AssignedSource: "switched", Selection: "reported",
		Current: &agentsession.CurrentView{ID: "5d1c0000-0000-4000-8000-000000000001", Source: "clear", At: at, ReadAt: at, Generation: 3},
		Launch:  &agentsession.LaunchView{Generation: 3, Kind: "resume", State: "launched", Operation: "req", Incarnation: "inc", Holder: "7@x", At: at, Detail: "d"},
		Resume:  &agentsession.ResumeView{Requested: "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", Generation: 3, At: at, Outcome: "unverified", Detail: "d"},
		Observed: &agentsession.ObservedView{ID: "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", At: at, ReadAt: at, Source: "resume", Generation: 3, Health: "ok", Detail: "d",
			Model:    &agentsession.ObservedValueView{Value: "claude-opus-5-5", Source: "statusline", At: at, ReadAt: at},
			Effort:   &agentsession.ObservedValueView{Value: "high", Source: "hook", At: at, ReadAt: at},
			Version:  &agentsession.ObservedValueView{Value: "2.1.311", Source: "statusline", At: at, ReadAt: at},
			Context:  &agentsession.ContextView{Size: 200000, Used: &used, Percent: &percent, Source: "statusline", At: at, ReadAt: at},
			State:    &agentsession.StateView{Kind: "waiting_permission", Source: "hook", At: at, ReadAt: at},
			Diverged: &agentsession.DivergenceView{ID: "5d1c0000-0000-4000-8000-000000000001", Source: "clear", At: at, ReadAt: at, Pending: true},
			Live:     "current", PendingOutcome: "mismatch"},
		Previous: []agentsession.PreviousView{{ID: "5d1c0000-0000-4000-8000-000000000000", EndedAt: at, Why: "new-session"},
			{ID: "5d1c0000-0000-4000-8000-000000000002", EndedAt: at, Why: "switched", To: "0b8f6e3c-1d2a-4f5b-8c7d-9e0f1a2b3c4d", Source: "resume"}},
		Note: "n",
	}
}

// checkConversationShape pins the one conversation schema shared by box
// show, box list and box session: stable snake_case keys, never the launch
// token or the observation cursor.
func checkConversationShape(t *testing.T, surface string, raw json.RawMessage) {
	t.Helper()
	if want := []string{"assigned_at", "assigned_source", "current", "id", "launch", "note", "observed", "policy", "previous", "provider", "resume", "selection", "tracked"}; !slices.Equal(keys(t, raw), want) {
		t.Errorf("%s conversation keys = %v, want %v", surface, keys(t, raw), want)
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	for section, want := range map[string][]string{
		"launch":   {"at", "detail", "generation", "holder", "incarnation", "kind", "operation", "state"},
		"resume":   {"at", "detail", "generation", "outcome", "requested"},
		"current":  {"at", "generation", "id", "read_at", "source"},
		"observed": {"at", "context", "detail", "diverged", "effort", "generation", "health", "id", "live", "model", "pending_outcome", "read_at", "source", "state", "version"},
	} {
		if got := keys(t, doc[section]); !slices.Equal(got, want) {
			t.Errorf("%s conversation.%s keys = %v, want %v", surface, section, got, want)
		}
	}
	var observed map[string]json.RawMessage
	_ = json.Unmarshal(doc["observed"], &observed)
	for _, value := range []string{"model", "effort", "version"} {
		if got := keys(t, observed[value]); !slices.Equal(got, []string{"at", "read_at", "source", "value"}) {
			t.Errorf("%s conversation.observed.%s keys = %v", surface, value, got)
		}
	}
	if got := keys(t, observed["context"]); !slices.Equal(got, []string{"at", "percent", "read_at", "size", "source", "used"}) {
		t.Errorf("%s conversation.observed.context keys = %v", surface, got)
	}
	if got := keys(t, observed["state"]); !slices.Equal(got, []string{"at", "kind", "read_at", "source"}) {
		t.Errorf("%s conversation.observed.state keys = %v", surface, got)
	}
	if got := keys(t, observed["diverged"]); !slices.Equal(got, []string{"at", "id", "pending", "read_at", "source"}) {
		t.Errorf("%s conversation.observed.diverged keys = %v", surface, got)
	}
	var previous []json.RawMessage
	_ = json.Unmarshal(doc["previous"], &previous)
	if len(previous) != 2 || !slices.Equal(keys(t, previous[0]), []string{"ended_at", "id", "why"}) || !slices.Equal(keys(t, previous[1]), []string{"ended_at", "id", "source", "to", "why"}) {
		t.Errorf("%s conversation.previous = %s", surface, doc["previous"])
	}
	for _, leak := range []string{"abababab", "launch_token", "Token", "cursor", "Cursor", "c2VjcmV0", "applied", "Applied"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("%s conversation leaks %q: %s", surface, leak, raw)
		}
	}
}

// One schema for all three surfaces.
func TestConversationJSONShapeIsSharedAndHidesCorrelationData(t *testing.T) {
	status := statusFixture()
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	checkConversationShape(t, "status", raw)

	row, err := json.Marshal(boxStateJSONRow(boxstate.Box{Space: "work", Name: "coder", Conversation: status}))
	if err != nil {
		t.Fatal(err)
	}
	var list map[string]json.RawMessage
	_ = json.Unmarshal(row, &list)
	checkConversationShape(t, "box list", list["conversation"])

	d := &container.Details{Name: "coder", Image: "img:1", Status: "Up", Running: true, Labels: map[string]string{agentbox.LabelAgentbox: "coder", agentbox.LabelAgent: "claude"}}
	var show bytes.Buffer
	if err := writeAgentboxShowJSON(&show, d, "work", landlockState{}, agentStateMount{}, agentboxShowMetadata{AgentName: "claude"}, sessionStatus{State: "running"}, &boxstate.Box{Conversation: status}); err != nil {
		t.Fatal(err)
	}
	var shown map[string]json.RawMessage
	_ = json.Unmarshal(show.Bytes(), &shown)
	checkConversationShape(t, "box show", shown["conversation"])

	view, err := json.Marshal(boxSessionView{SchemaVersion: 1, Space: "work", Box: "coder", Session: boxSessionState{State: boxSessionRunning, Tracked: true}, Conversation: status})
	if err != nil {
		t.Fatal(err)
	}
	var session map[string]json.RawMessage
	_ = json.Unmarshal(view, &session)
	if !slices.Equal(keys(t, view), []string{"box", "conversation", "schema_version", "session", "space"}) || !slices.Equal(keys(t, session["session"]), []string{"state", "tracked"}) {
		t.Errorf("box session keys = %v / %v", keys(t, view), keys(t, session["session"]))
	}
	checkConversationShape(t, "box session", session["conversation"])
}

// box session is in box show's registry class: read-only, operator-visible
// within its space, no effect gate and no receipt.
func TestBoxSessionRegistryAndAuthorization(t *testing.T) {
	if op := controllerOperations["box.session"]; op != (controller.Operation{BoxAllowed: true}) || controllerOperations["box.show"] != op {
		t.Fatalf("box.session = %+v, want box.show's class %+v", op, controllerOperations["box.show"])
	}
	root := &cobra.Command{Use: "yoloctl"}
	box := newAgentboxCmd()
	root.AddCommand(box)
	var session *cobra.Command
	for _, c := range box.Commands() {
		if c.Name() == "session" {
			session = c
		}
	}
	if session == nil || commandMethod(session) != "box.session" {
		t.Fatal("box session is not routed as box.session")
	}
	operator := controller.Principal{Box: "lead", Incarnation: "live"}
	request := func(flags map[string][]string) controller.Request {
		return controller.Request{ID: strings.Repeat("r", 20), Method: "box.session", Space: "work", Args: []string{"coder"}, Flags: flags, Dash: -1}
	}
	if err := controller.Authorize(controller.Principal{}, request(nil), "work", controllerOperations); err != nil {
		t.Fatal("host admin refused:", err)
	}
	if err := controller.Authorize(operator, request(nil), "work", controllerOperations); err != nil {
		t.Fatal("operator refused in its own space:", err)
	}
	for name, flags := range map[string]map[string][]string{
		"another space": {"space": {"other"}},
		"all spaces":    {"all-spaces": {"true"}},
	} {
		if err := controller.Authorize(operator, request(flags), "work", controllerOperations); err == nil {
			t.Errorf("operator allowed %s", name)
		}
	}
}

func TestBoxSessionViewStates(t *testing.T) {
	t.Run("tracked launch", func(t *testing.T) {
		f := sessionViewFixture(t, claudeBox())
		if _, err := f.launchOnce(claudeBox(), sessionStartOptions{}); err != nil {
			t.Fatal(err)
		}
		view, out, err := runBoxSession(t, "--json")
		if err != nil || view.Session.State != boxSessionRunning || !view.Session.Tracked || view.Conversation == nil || view.Conversation.Launch == nil || view.Conversation.Launch.State != agentsession.StateLaunched || view.Conversation.Note != "" {
			t.Fatalf("tracked view = %+v (%v)\n%s", view, err, out)
		}
		_, text, err := runBoxSession(t)
		if err != nil || !strings.Contains(text, "Session:      running (tracked launch)") || !strings.Contains(text, "Launch:       generation 1 · assign · launched") {
			t.Fatalf("tracked text:\n%s (%v)", text, err)
		}
	})
	t.Run("untracked live session", func(t *testing.T) {
		f := sessionViewFixture(t, claudeBox())
		f.unit["coder"] = true
		f.marker["coder"] = session.Marker{StartedAt: time.Unix(1, 0), HolderPID: 4}
		view, _, err := runBoxSession(t, "--json")
		if err != nil || view.Session.State != boxSessionRunning || view.Session.Tracked || view.Conversation.Note != agentsession.Untracked || view.Conversation.ID != "" {
			t.Fatalf("untracked view = %+v (%v)", view, err)
		}
	})
	t.Run("undecidable probe is unknown, not idle", func(t *testing.T) {
		f := sessionViewFixture(t, claudeBox())
		f.unitErr["coder"] = errors.New("service manager unavailable")
		view, out, err := runBoxSession(t, "--json")
		if err != nil || view.Session.State != boxSessionUnknown || !strings.Contains(view.Session.Error, "service manager unavailable") {
			t.Fatalf("unknown view = %+v (%v)\n%s", view, err, out)
		}
		_, text, _ := runBoxSession(t)
		if !strings.Contains(text, "Session:      unknown — service manager unavailable") {
			t.Fatalf("unknown text:\n%s", text)
		}
		f.unitErr = map[string]error{}
		f.unit["coder"] = true
		f.markerErr["coder"] = errors.New("probe failed")
		view, _, _ = runBoxSession(t, "--json")
		if view.Session.State != boxSessionRunning || view.Session.Error == "" || view.Session.Tracked || view.Conversation.Note == agentsession.Untracked {
			t.Fatalf("unreadable marker of a live holder = %+v", view)
		}
	})
	t.Run("lost and not running", func(t *testing.T) {
		f := sessionViewFixture(t, claudeBox())
		f.marker["coder"] = session.Marker{StartedAt: time.Unix(1, 0), HolderPID: 4}
		if view, _, _ := runBoxSession(t, "--json"); view.Session.State != boxSessionLost {
			t.Fatalf("marker without holder = %+v", view.Session)
		}
		delete(f.marker, "coder")
		if view, _, _ := runBoxSession(t, "--json"); view.Session.State != boxSessionNotRunning || view.Conversation.Note != "" {
			t.Fatalf("idle box = %+v", view)
		}
	})
	t.Run("manual, unsupported and agentless", func(t *testing.T) {
		manual := claudeBox("--continue")
		sessionViewFixture(t, manual)
		if view, _, err := runBoxSession(t, "--json"); err != nil || view.Conversation.Tracked || !strings.Contains(view.Conversation.Policy, "manual (legacy") {
			t.Fatalf("manual view = %+v (%v)", view.Conversation, err)
		}
		unsupported := claudeBox()
		unsupported.Agent = "gemini"
		sessionViewFixture(t, unsupported)
		if view, _, err := runBoxSession(t, "--json"); err != nil || view.Conversation.Policy != "unsupported" {
			t.Fatalf("gemini view = %+v (%v)", view.Conversation, err)
		}
		shell := &agentbox.Config{Name: "coder", Space: "work", Agent: "none", Account: "none"}
		sessionViewFixture(t, shell)
		if view, text, err := runBoxSession(t); err != nil || view.Conversation != nil || !strings.Contains(text, "Conversation: none (agentless box)") {
			t.Fatalf("agentless view:\n%s (%v)", text, err)
		}
	})
	t.Run("unreadable record fails explicitly", func(t *testing.T) {
		sessionViewFixture(t, claudeBox())
		path, _ := layout.BoxSessionPath("work", "coder")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("schema_version: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		view, _, err := runBoxSession(t, "--json")
		if err == nil || !strings.Contains(err.Error(), "conversation record of box coder is unavailable") || !strings.HasPrefix(view.Conversation.Note, "record unavailable:") {
			t.Fatalf("unreadable record = %+v (%v)", view.Conversation, err)
		}
	})
	t.Run("missing declaration fails", func(t *testing.T) {
		sessionViewFixture(t, claudeBox())
		manifest, _ := layout.BoxPath("work", "coder")
		_ = os.Remove(manifest)
		if _, _, err := runBoxSession(t); err == nil || !strings.Contains(err.Error(), `no box "coder"`) {
			t.Fatalf("missing declaration: %v", err)
		}
	})
}

// The view takes no lock and writes nothing: it completes while the box lock
// is held elsewhere and leaves the record's bytes and mtime unchanged.
func TestBoxSessionIsReadOnly(t *testing.T) {
	f := sessionViewFixture(t, claudeBox())
	if _, err := f.launchOnce(claudeBox(), sessionStartOptions{}); err != nil {
		t.Fatal(err)
	}
	path, _ := layout.BoxSessionPath("work", "coder")
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	lock, err := agentsession.LockBox("work", "coder")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	done := make(chan error, 1)
	go func() {
		_, err := readBoxSession(t.Context(), container.New("work"), "work", "coder")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("box session waited for the box lock")
	}
	after, _ := os.ReadFile(path)
	again, _ := os.Stat(path)
	if !bytes.Equal(before, after) || !info.ModTime().Equal(again.ModTime()) {
		t.Fatal("box session changed the record")
	}
}
