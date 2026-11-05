package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestAuditAuthorizationIsHostOnly(t *testing.T) {
	for _, method := range []string{"audit.status", "audit.enable", "audit.disable", "audit.logs", "audit.export"} {
		request := controller.Request{ID: "audit-test-0123456789abcdef", Space: "test", Method: method, Dash: -1}
		if err := controller.Authorize(controller.Principal{}, request, "test", controllerOperations); err != nil {
			t.Fatal(method, err)
		}
		if err := controller.Authorize(controller.Principal{Box: "operator"}, request, "test", controllerOperations); err == nil {
			t.Fatal("operator received audit authority", method)
		}
	}
	target := controllerTarget{Operator: true, Call: func(context.Context, controller.Request, controller.ClientIO) error {
		t.Fatal("contacted operator endpoint")
		return nil
	}}
	if err := callAudit(t.Context(), target, "audit.status", nil, nil, io.Discard); err == nil {
		t.Fatal("frontend accepted operator routing")
	}
}

func TestAuditHostMarkerRefusesBeforeSelection(t *testing.T) {
	t.Setenv("OPENSHELL_SANDBOX", "1")
	cmd := newAuditCmd()
	root := newClientRootCmd()
	for _, registered := range root.Commands() {
		if registered.Name() == "audit" {
			t.Fatal("registered host audit inside a box")
		}
	}
	cmd.SetArgs([]string{"status"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "require the host") {
		t.Fatal(err)
	}
}

func TestAuditScanCompletionAndPrivateExport(t *testing.T) {
	event := audit.Event{Cursor: 1, Time: time.Now().UTC(), Rule: "test", Severity: "Notice"}
	for _, scenario := range []string{"complete", "missing-footer", "wrong-count", "remote-failure", "after-footer"} {
		t.Run(scenario, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "audit.jsonl")
			target := controllerTarget{Space: "test", Call: func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
				if req.Method != "audit.export" {
					t.Fatal(req.Method)
				}
				encoder := json.NewEncoder(streams.Out)
				if err := encoder.Encode(audit.Frame{Type: "event", Event: &event}); err != nil {
					return err
				}
				if scenario == "missing-footer" {
					return nil
				}
				summary := audit.Summary{First: 1, Last: 1, Upper: 1, Count: 1, Until: event.Time}
				if scenario == "wrong-count" {
					summary.Count = 2
				}
				if err := encoder.Encode(audit.Frame{Type: "summary", Summary: &summary}); err != nil {
					return err
				}
				if scenario == "remote-failure" {
					return errors.New("reply lost")
				}
				if scenario == "after-footer" {
					return encoder.Encode(audit.Frame{Type: "event", Event: &event})
				}
				return nil
			}}
			_, err := publishAuditExport(output, func(w io.Writer) (audit.Summary, error) {
				return scanAudit(t.Context(), target, "audit.export", audit.Query{}, func(e audit.Event) error { return json.NewEncoder(w).Encode(e) })
			})
			if scenario == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(output)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal(info, err)
				}
				raw, err := os.ReadFile(output)
				if err != nil || !bytes.Contains(raw, []byte(`"rule":"test"`)) {
					t.Fatal(string(raw), err)
				}
				original := append([]byte(nil), raw...)
				if _, err := publishAuditExport(output, func(io.Writer) (audit.Summary, error) {
					t.Fatal("wrote over existing export")
					return audit.Summary{}, nil
				}); err == nil {
					t.Fatal("overwrote export")
				}
				raw, err = os.ReadFile(output)
				if err != nil || !bytes.Equal(raw, original) {
					t.Fatal("changed existing export", err)
				}
			} else {
				if err == nil {
					t.Fatal("published unproved export")
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("published partial export", err)
				}
			}
			entries, err := os.ReadDir(filepath.Dir(output))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".yoloctl-audit-") {
					t.Fatal("retained private partial output", entry.Name())
				}
			}
		})
	}
}

func TestAuditStatusDecodesAndFiniteQuery(t *testing.T) {
	target := controllerTarget{Space: "test", Call: func(_ context.Context, _ controller.Request, s controller.ClientIO) error {
		return json.NewEncoder(s.Out).Encode(audit.Status{Architecture: "arm64", State: "inactive"})
	}}
	status, err := fetchAuditStatus(t.Context(), target)
	if err != nil || status.Architecture != "arm64" || status.State != "inactive" {
		t.Fatal(status, err)
	}
	for _, args := range [][]string{{"--since", "nonsense"}, {"--since", "2026-11-05T00:00:00Z", "--until", "2026-11-04T00:00:00Z"}, {"--box", "../escape"}} {
		cmd := newAuditCmd()
		logs, _, _ := cmd.Find([]string{"logs"})
		if err := logs.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		if _, err := auditQuery(logs); err == nil {
			t.Fatal("accepted invalid history query", args)
		}
	}
}

func TestAuditRegistrationOnHostAndInContainer(t *testing.T) {
	stubOperatorClient(t, nil)
	t.Setenv("OPENSHELL_SANDBOX", "")
	if err := os.Unsetenv("OPENSHELL_SANDBOX"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(spaceenv.ContextEnv, "")
	old := auditContainerSignal
	t.Cleanup(func() { auditContainerSignal = old })
	for _, signal := range []string{"", "container marker"} {
		auditContainerSignal = func() string { return signal }
		root := newClientRootCmd()
		found := false
		for _, cmd := range root.Commands() {
			found = found || cmd.Name() == "audit"
		}
		if found != (signal == "") {
			t.Fatal("host-only registration differs", signal, found)
		}
	}
}
