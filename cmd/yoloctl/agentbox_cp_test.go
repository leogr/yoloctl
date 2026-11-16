package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestBoxCPParsingAndTypedHostOnlyAdmission(t *testing.T) {
	for _, s := range []string{"", "box", "box:", ":/tmp", "../bad:/tmp", "box:/x\x00"} {
		if _, err := parseBoxFilesSource(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{"box:relative", "box:/home/sandbox/.secrets", "box:/var/lib/yoloctl/account-state", "box:../relative", "box:/a:b"} {
		if _, err := parseBoxFilesSource(s); err != nil {
			t.Errorf("source filtered %q: %v", s, err)
		}
	}
	payload := []byte(`{"box":"work","source":"/tmp"}`)
	req := controller.Request{ID: "0123456789abcdef0123456789abcdef", Method: "box.files.export", Space: "test", Payload: payload}
	if _, err := decodeBoxFilesRequest(req); err != nil {
		t.Fatal(err)
	}
	op, ok := controllerOperations[req.Method]
	if !ok || op.Mutates || op.BoxAllowed || op.EffectGated {
		t.Fatal(op)
	}
	if _, ok := controllerOperations["box.cp"]; ok {
		t.Fatal("public command was routed")
	}
	if err := controller.Authorize(controller.Principal{}, req, "test", controllerOperations); err != nil {
		t.Fatal(err)
	}
	if err := controller.Authorize(controller.Principal{Box: "operator", Incarnation: strings.Repeat("a", 32)}, req, "test", controllerOperations); err == nil {
		t.Fatal("operator RPC authorized")
	}
	for name, edit := range map[string]func(*controller.Request){"args": func(r *controller.Request) { r.Args = []string{"sh"} }, "flags": func(r *controller.Request) { r.Flags = map[string][]string{"overwrite": {"true"}} }, "destination": func(r *controller.Request) { r.Payload = []byte(`{"box":"work","source":"/tmp","dest":"/tmp"}`) }, "view": func(r *controller.Request) { r.View = true }, "cwd": func(r *controller.Request) { r.Cwd = "/tmp" }, "shell": func(r *controller.Request) { r.Payload = []byte(`{"box":"work","source":""}`) }} {
		bad := req
		edit(&bad)
		if _, err := decodeBoxFilesRequest(bad); err == nil {
			t.Error(name, "accepted")
		}
	}
}
func TestBoxCPHostRefusesNativeAndSandbox(t *testing.T) {
	for _, key := range []string{spaceenv.ContextEnv, "OPENSHELL_SANDBOX"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "fixture")
			if err := boxCPHostAllowed(); err == nil {
				t.Fatal("in-box allowed")
			}
		})
	}
}
func TestBoxCPAdmissionPrecedesHostAndTransportAndRawStreamIsPure(t *testing.T) {
	previousAllowed, previousStream := boxCPAllowed, boxCPStream
	t.Cleanup(func() { boxCPAllowed, boxCPStream = previousAllowed, previousStream })
	calls := 0
	boxCPStream = func(context.Context, string, boxFilesRequest, io.Writer, io.Writer) error { calls++; return nil }
	boxCPAllowed = func() error { return errors.New("operator denied") }
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	cmd := newAgentboxCPCmd()
	cmd.SetArgs([]string{"work:/data", dest, "--space", "test"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(t.Context()); err == nil || calls != 0 {
		t.Fatal(err, calls)
	}
	names, _ := os.ReadDir(root)
	if len(names) != 0 {
		t.Fatal("host touched before admission")
	}
	boxCPAllowed = func() error { return nil }
	raw := []byte("unchanged\x00tar bytes")
	failure := errors.New("late transport failure")
	boxCPStream = func(_ context.Context, space string, r boxFilesRequest, out, errw io.Writer) error {
		calls++
		if space != "test" || r.Source != "/home/sandbox/.secrets" {
			t.Fatal(space, r)
		}
		_, _ = errw.Write([]byte("notice"))
		_, _ = out.Write(raw)
		return failure
	}
	var out, errout bytes.Buffer
	cmd = newAgentboxCPCmd()
	cmd.SetArgs([]string{"work:/home/sandbox/.secrets", "-", "--space", "test"})
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), raw) || !strings.Contains(errout.String(), "notice") {
		t.Fatal(out.String(), errout.String())
	}
}

type fileContainerFixture struct {
	d       *container.Details
	calls   []string
	inspect func(string) (*container.Details, error)
	copied  string
}

func (f *fileContainerFixture) Inspect(_ context.Context, target string) (*container.Details, error) {
	f.calls = append(f.calls, target)
	if f.inspect != nil {
		return f.inspect(target)
	}
	d := *f.d
	return &d, nil
}
func (f *fileContainerFixture) CopyArchive(_ context.Context, cid, source string, out io.Writer) error {
	f.copied = cid + ":" + source
	_, err := io.WriteString(out, "archive")
	return err
}
func retainedFixture() *container.Details {
	return &container.Details{ID: strings.Repeat("a", 64), Name: "work", Status: "exited", InactiveTaskFieldsObserved: true, Labels: map[string]string{agentbox.LabelSpace: "test", agentbox.LabelAgentbox: "work", agentbox.LabelKind: agentbox.KindAgentbox}}
}
func TestBoxCPRetainsCIDAcrossReplacementAndNeverStarts(t *testing.T) {
	for _, running := range []bool{false, true} {
		d := retainedFixture()
		if running {
			d.Running = true
			d.Status = "running"
			d.PID = 42
		}
		f := &fileContainerFixture{d: d}
		verify := 0
		var out bytes.Buffer
		if err := exportRetainedBox(t.Context(), "test", boxFilesRequest{"work", "/data"}, f, func(context.Context, *container.Details) error { verify++; return nil }, &out); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.calls, []string{"work", d.ID}) || f.copied != d.ID+":/data" || verify != 2 || out.String() != "archive" {
			t.Fatal(f, verify)
		}
	}
	for _, replacement := range []bool{false, true} {
		d := retainedFixture()
		f := &fileContainerFixture{d: d}
		f.inspect = func(target string) (*container.Details, error) {
			if target == "work" {
				return d, nil
			}
			if !replacement {
				return nil, os.ErrNotExist
			}
			wrong := *d
			wrong.ID = strings.Repeat("b", 64)
			return &wrong, nil
		}
		if err := exportRetainedBox(t.Context(), "test", boxFilesRequest{"work", "/data"}, f, func(context.Context, *container.Details) error { return nil }, io.Discard); err == nil || f.copied != "" {
			t.Fatal("replacement/deletion adopted", err)
		}
	}
}
func TestBoxCPUnknownOwnershipStateAndRecordNeverReachProducer(t *testing.T) {
	for _, edit := range []func(*container.Details){func(d *container.Details) { d.ID = "prefix" }, func(d *container.Details) { d.Name = "other" }, func(d *container.Details) { d.Labels[agentbox.LabelSpace] = "other" }, func(d *container.Details) { d.Labels[agentbox.LabelKind] = "other" }, func(d *container.Details) { d.Status = "unknown" }, func(d *container.Details) { d.InactiveTaskFieldsObserved = false }, func(d *container.Details) { d.AmbiguousTaskStateObserved = true }} {
		d := retainedFixture()
		edit(d)
		f := &fileContainerFixture{d: d}
		if err := exportRetainedBox(t.Context(), "test", boxFilesRequest{"work", "/data"}, f, func(context.Context, *container.Details) error { return nil }, io.Discard); err == nil || f.copied != "" {
			t.Fatal("uncertain export", err)
		}
	}
	f := &fileContainerFixture{d: retainedFixture()}
	if err := exportRetainedBox(t.Context(), "test", boxFilesRequest{"work", "/data"}, f, func(context.Context, *container.Details) error { return errors.New("record unavailable") }, io.Discard); err == nil || f.copied != "" {
		t.Fatal(err)
	}
}
func TestBoxCPFilesystemReceivesValidTarBeforeLateFailure(t *testing.T) {
	previousAllowed, previousStream := boxCPAllowed, boxCPStream
	t.Cleanup(func() { boxCPAllowed, boxCPStream = previousAllowed, previousStream })
	boxCPAllowed = func() error { return nil }
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "file", Size: 4, Mode: 0644}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("data"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	boxCPStream = func(_ context.Context, _ string, _ boxFilesRequest, out, errw io.Writer) error {
		_, err := out.Write(raw.Bytes())
		return errors.Join(err, errors.New("producer failed"))
	}
	dest := filepath.Join(t.TempDir(), "out")
	cmd := newAgentboxCPCmd()
	cmd.SetArgs([]string{"work:relative", dest, "--space", "test"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("late failure accepted")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatal("published", err)
	}
}
func TestBoxCPRequestHasNoHostDestination(t *testing.T) {
	b, _ := json.Marshal(boxFilesRequest{"work", "relative"})
	if string(b) != `{"box":"work","source":"relative"}` {
		t.Fatal(string(b))
	}
}

func TestBoxCPOperatorWithExplicitSpaceStillRefusesBeforeDestination(t *testing.T) {
	previousLoad, previousAllowed, previousStream := loadOperatorClient, boxCPAllowed, boxCPStream
	t.Cleanup(func() { loadOperatorClient, boxCPAllowed, boxCPStream = previousLoad, previousAllowed, previousStream })
	// Exercise actual operator detection, independent of the transport seam.
	t.Setenv(spaceenv.ContextEnv, "")
	t.Setenv("OPENSHELL_SANDBOX", "")
	// LookupEnv intentionally refuses even an empty sandbox marker. Clear it
	// just for the operator route; testing restores the original environment.
	if err := os.Unsetenv("OPENSHELL_SANDBOX"); err != nil {
		t.Fatal(err)
	}
	loadOperatorClient = func() (*controller.OperatorClient, error) { return &controller.OperatorClient{Space: "test"}, nil }
	boxCPAllowed = boxCPHostAllowed
	boxCPStream = func(context.Context, string, boxFilesRequest, io.Writer, io.Writer) error {
		t.Fatal("operator contacted export")
		return nil
	}
	root := t.TempDir()
	cmd := newAgentboxCPCmd()
	cmd.SetArgs([]string{"work:/etc", filepath.Join(root, "out"), "--space", "test"})
	cmd.SetErr(io.Discard)
	cmd.SetOut(io.Discard)
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "operators") {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("operator touched destination")
	}
}

func TestBoxCPContentsSelectionMatchesProducerPathBase(t *testing.T) {
	previousAllowed, previousStream := boxCPAllowed, boxCPStream
	t.Cleanup(func() { boxCPAllowed, boxCPStream = previousAllowed, previousStream })
	boxCPAllowed = func() error { return nil }
	for _, source := range []string{".", "/tree/.", "/tree/./", "tree/.", "tree/./"} {
		for _, existing := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "/new", true: "/existing"}[existing], func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, "out")
				if existing {
					if err := os.Mkdir(dest, 0700); err != nil {
						t.Fatal(err)
					}
				}
				boxCPStream = func(_ context.Context, _ string, r boxFilesRequest, out, errw io.Writer) error {
					if r.Source != source {
						t.Fatal("source rewritten", r.Source)
					}
					tw := tar.NewWriter(out)
					for _, file := range []string{"./one", "./two"} {
						if err := tw.WriteHeader(&tar.Header{Name: file, Size: 4, Mode: 0600}); err != nil {
							return err
						}
						if _, err := io.WriteString(tw, "data"); err != nil {
							return err
						}
					}
					return tw.Close()
				}
				cmd := newAgentboxCPCmd()
				cmd.SetArgs([]string{"work:" + source, dest, "--space", "test"})
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				if err := cmd.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				for _, file := range []string{"one", "two"} {
					b, err := os.ReadFile(filepath.Join(dest, file))
					if err != nil || string(b) != "data" {
						t.Fatal(file, string(b), err)
					}
				}
			})
		}
	}
}
