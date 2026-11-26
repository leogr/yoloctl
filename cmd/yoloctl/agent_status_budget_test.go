package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func TestAgentStatusAllSpacesKeepsInstalledAndResponsiveLatest(t *testing.T) {
	for _, test := range []string{"silent registry", "silent controller", "cancel", "offline"} {
		t.Run(test, func(t *testing.T) {
			first := imageUpdateFixture(t)
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			identity, err := controller.ExecutableIdentity()
			if err != nil {
				t.Fatal(err)
			}
			first.Bundle = identity
			second := *first
			second.Name, second.DiskID = "healthy", strings.Repeat("2", 32)
			for _, boot := range []*spaceconfig.Bootstrap{first, &second} {
				if err := boot.Save(); err != nil {
					t.Fatal(err)
				}
			}
			oldLookup, oldCall := controllerSpaceLookup, callControllerView
			oldLatest, oldInstalled := doAgentRegistryRequest, agentStatusInstalledVersion
			t.Cleanup(func() {
				controllerSpaceLookup, callControllerView = oldLookup, oldCall
				doAgentRegistryRequest, agentStatusInstalledVersion = oldLatest, oldInstalled
			})
			controllerSpaceLookup = func(_ context.Context, name string) (vmhost.Instance, bool, error) {
				return vmhost.Instance{Name: name, Status: "Running"}, true, nil
			}
			agentStatusInstalledVersion = func(context.Context, *container.Client, string, *agentprofile.Profile) (string, error) {
				return "1.0.0", nil
			}
			synctest.Test(t, func(t *testing.T) {
				defer bindCLITestLogger(t, io.Discard)()
				var silentLookups, responsiveLookups atomic.Int32
				silentEntered := make(chan struct{})
				doAgentRegistryRequest = func(req *http.Request) (*http.Response, error) {
					if strings.Contains(req.URL.Path, "codex") {
						if silentLookups.Add(1) == 1 {
							close(silentEntered)
						}
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					responsiveLookups.Add(1)
					if test == "silent registry" {
						// Require both lookups to overlap regardless of map iteration order.
						select {
						case <-silentEntered:
						case <-req.Context().Done():
						}
					}
					if err := req.Context().Err(); err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"dist-tags":{"latest":"1.0.1"}}`)), Header: http.Header{}}, nil
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var calls []string
				callControllerView = func(requestCtx context.Context, boot *spaceconfig.Bootstrap, req controller.Request, streams controller.ClientIO) error {
					calls = append(calls, boot.Name)
					if req.Method != "agent.status" {
						t.Fatal(req.Method)
					}
					deadline, ok := requestCtx.Deadline()
					if !ok || time.Until(deadline) != 2*time.Minute {
						t.Fatalf("per-space allowance at request: %v (has deadline %t)", time.Until(deadline), ok)
					}
					if boot.Name == first.Name {
						switch test {
						case "silent controller":
							<-requestCtx.Done()
							return requestCtx.Err()
						case "cancel":
							cancel()
							return requestCtx.Err()
						}
					}
					boxes := []agentboxRow{{
						Space: boot.Name, Container: container.Container{Name: "a-fast", Status: "Up", Labels: agentbox.LabelAgent + "=claude"},
					}}
					if boot.Name == first.Name {
						// Both rows for the silent distribution share one lookup.
						for _, name := range []string{"b-slow", "c-slow"} {
							boxes = append(boxes, agentboxRow{Space: boot.Name, Container: container.Container{
								Name: name, Status: "Up", Labels: agentbox.LabelAgent + "=codex",
							}})
						}
					}
					rows := buildAgentStatusRows(requestCtx, boxes, agentStatusOptions{CheckLatest: req.Flags["no-check-latest"][0] != "true"})
					return writeAgentStatusJSON(streams.Out, rows)
				}
				var out bytes.Buffer
				cmd := newAgentStatusCmd()
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				cmd.SetArgs([]string{"--all-spaces", "--json"})
				// Use the same aggregation route the host dispatches to.
				cmd.RunE = func(cmd *cobra.Command, _ []string) error { return controllerAllSpaces(cmd, "agent.status") }
				if test == "offline" {
					cmd.SetArgs([]string{"--all-spaces", "--json", "--no-check-latest"})
				}
				cmd.SetOut(&out)
				cmd.SetErr(io.Discard)
				start := time.Now()
				err := cmd.ExecuteContext(ctx)
				if test == "cancel" {
					if !errors.Is(err, context.Canceled) || len(calls) != 1 || out.Len() != 0 {
						t.Fatal(calls, err, out.String())
					}
					return
				}
				if len(calls) != 2 {
					t.Fatal(calls)
				}
				var got []agentStatusRow
				if decodeErr := json.Unmarshal(out.Bytes(), &got); decodeErr != nil {
					t.Fatal(decodeErr, out.String())
				}
				if test == "silent controller" {
					if err == nil || !strings.Contains(err.Error(), "1 space(s) unavailable") || len(got) != 1 || time.Since(start) != 2*time.Minute {
						t.Fatal(err, got, time.Since(start))
					}
					return
				}
				if err != nil || len(got) != 4 {
					t.Fatal(err, got)
				}
				for _, row := range got {
					if row.InstalledVersion != "1.0.0" {
						t.Fatal("known installed version lost", row)
					}
					switch {
					case test == "offline":
						if row.LatestVersion != "" || row.Error != "" {
							t.Fatal(row)
						}
					case row.Agent == "codex":
						if row.LatestVersion != "" || !strings.Contains(row.Error, "query latest:") {
							t.Fatal(row)
						}
					default:
						if row.LatestVersion != "1.0.1" || row.Error != "" {
							t.Fatal("responsive distribution lost", row)
						}
					}
				}
				if test == "offline" {
					if silentLookups.Load() != 0 || responsiveLookups.Load() != 0 {
						t.Fatal(silentLookups.Load(), responsiveLookups.Load())
					}
				} else if silentLookups.Load() != 1 || responsiveLookups.Load() != 1 || time.Since(start) != time.Minute {
					t.Fatal(silentLookups.Load(), responsiveLookups.Load(), time.Since(start))
				}
			})
		})
	}
}
