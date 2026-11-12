package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
)

func auditLogFixture(t *testing.T, priorities []string, remoteError error) (*cobra.Command, controllerTarget, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("verbose", false, "")
	addLogFlags(cmd)
	cmd.Flags().AddFlagSet(cmd.PersistentFlags())
	target := controllerTarget{Space: "test", Call: func(_ context.Context, _ controller.Request, streams controller.ClientIO) error {
		encoder := json.NewEncoder(streams.Out)
		for i, priority := range priorities {
			event := audit.Event{Cursor: uint64(i + 1), Time: time.Unix(100, 0).UTC(), Rule: "rule-" + priority, Severity: priority, Container: "host", Output: "original retained message", OutputFields: map[string]string{"user.name": "retained"}}
			if err := encoder.Encode(audit.Frame{Type: "event", Event: &event}); err != nil {
				return err
			}
		}
		if remoteError != nil {
			return remoteError
		}
		summary := audit.Summary{Count: len(priorities), First: 1, Last: uint64(len(priorities)), Upper: uint64(len(priorities))}
		return encoder.Encode(audit.Frame{Type: "summary", Summary: &summary})
	}}
	return cmd, target, &stdout, &stderr
}

func TestAuditLogsUseGlobalSettingsButJSONRemainsUnfiltered(t *testing.T) {
	t.Setenv("YOLOCTL_LOG", "info")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	for _, fixture := range []struct {
		name, flag, value string
		wantNotice        bool
		wantWarning       bool
		color, json       bool
	}{
		{"default pipe", "", "", true, true, false, false},
		{"quiet", "quiet", "true", false, true, false, false},
		{"error", "log-level", "error", false, false, false, false},
		{"forced color pipe", "color", "always", true, true, true, false},
		{"json at error", "log-level", "error", true, true, false, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			cmd, target, out, _ := auditLogFixture(t, []string{"Notice", "Warning", "Error"}, nil)
			if fixture.flag != "" {
				if err := cmd.Flags().Set(fixture.flag, fixture.value); err != nil {
					t.Fatal(err)
				}
			}
			if fixture.json {
				_ = cmd.Flags().Set("json", "true")
			}
			if err := streamAuditLogs(cmd, target, audit.Query{Tail: true}, false, nil); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if strings.Contains(text, "rule-Notice") != fixture.wantNotice || strings.Contains(text, "rule-Warning") != fixture.wantWarning || !strings.Contains(text, "rule-Error") || strings.Contains(text, "\x1b") != fixture.color {
				t.Fatal("global setting ignored", text)
			}
			if fixture.json {
				lines := strings.Split(strings.TrimSpace(text), "\n")
				if len(lines) != 3 {
					t.Fatal(text)
				}
				for i, line := range lines {
					var event audit.Event
					if err := json.Unmarshal([]byte(line), &event); err != nil || event.Cursor != uint64(i+1) || event.Output != "original retained message" || event.OutputFields["user.name"] != "retained" {
						t.Fatal("JSON changed", line, err)
					}
				}
			}
		})
	}
}

func TestAuditLogsDrainPendingRepeatOnRemoteFailure(t *testing.T) {
	want := errors.New("read failed")
	cmd, target, out, _ := auditLogFixture(t, []string{"Notice", "Notice"}, want)
	if err := streamAuditLogs(cmd, target, audit.Query{}, false, nil); err == nil || !strings.Contains(err.Error(), "incomplete or oversized audit response") {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "rule-Notice") != 1 || strings.Count(out.String(), "repeated 1 more time") != 1 {
		t.Fatal("failure lost or duplicated final fold summary", out.String())
	}
}
