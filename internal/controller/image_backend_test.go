package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/imagejob"
	bk "github.com/moby/buildkit/client"
)

func TestPrivateExporterRejectsAliasesAndUnsupportedOptionsBeforeSubmission(t *testing.T) {
	id := strings.Repeat("a", 64)
	phase := ImagePhase{PrivateVersion: 1, JobID: id, PreparedRef: "yoloctl/prepared/" + id, SolveRef: id}
	recipe := recipe("a")
	good, err := privateSolveOptions(phase, t.TempDir(), recipe)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "name-canonical", "push", "push-by-digest", "dangling-name-prefix", "store", "unpack", "compression", "force-compression", "compression-level", "unknown"} {
		t.Run(key, func(t *testing.T) {
			bad, err := privateSolveOptions(phase, t.TempDir(), recipe)
			if err != nil {
				t.Fatal(err)
			}
			bad.Exports[0].Attrs[key] = "unsafe"
			if err := validatePrivateExport(bad, phase); err == nil {
				t.Fatal("accepted unsafe exporter", key)
			}
		})
	}
	if got := good.Exports[0].Attrs["compression"]; got != "uncompressed" {
		t.Fatal("private layers are compressed although they never leave the VM", got)
	}
	if got := good.Exports[0].Attrs["name"]; got != "docker.io/"+phase.PreparedRef+":latest" {
		t.Fatal("exporter name differs from containerd pin lookup", got)
	}
	good.Exports = append(good.Exports, bk.ExportEntry{Type: bk.ExporterLocal})
	if err := validatePrivateExport(good, phase); err == nil {
		t.Fatal("accepted additional exporter")
	}
}
func TestDockerfileBindingPreservesInternalAndExternalStages(t *testing.T) {
	r := recipe("a")
	r.BaseReference = "mutable:latest"
	r.Dockerfile = []byte("ARG BASE=mutable:latest\nFROM --platform=$BUILDPLATFORM ${BASE} AS build\nRUN true\nFROM alpine:3 AS external\nFROM build AS final\nCOPY --from=busybox:1 /bin/true /true\n")
	raw, err := immutableDockerfile(r)
	if err != nil || string(raw) != string(r.Dockerfile) {
		t.Fatal("Dockerfile surface changed", string(raw), err)
	}
	inputs, err := imagejob.DockerfileImages(r)
	if err != nil || len(inputs) != 3 {
		t.Fatal(inputs, err)
	}
	for i := range inputs {
		inputs[i].Digest = r.BaseDigest
	}
	r.Images = inputs
	if _, err := imageSourcePolicy(r); err != nil {
		t.Fatal(err)
	}
}

func TestImageJobRemainingWaiterKeepsBuildAndPin(t *testing.T) {
	m, b := newJobs(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	a := acquire(m, ctx, recipe("shared"))
	phase := take(t, b.started)
	follower := acquire(m, t.Context(), recipe("shared"))
	// Wait for the follower's actual interest, not elapsed wall time.
	for {
		m.mu.Lock()
		interests := m.jobs[phase.JobID].interests
		changed := m.changed
		m.mu.Unlock()
		if interests == 2 {
			break
		}
		select {
		case <-changed:
		case <-t.Context().Done():
			t.Fatal("no follower")
		}
	}
	cancel()
	if got := take(t, a); got.err == nil {
		t.Fatal("cancellation lost")
	}
	b.release <- struct{}{}
	got := take(t, follower)
	if got.err != nil {
		t.Fatal(got.err)
	}
	r, err := m.server.Receipts.Read(phase.JobID)
	if err != nil || r.abandonedImage() || r.Completion == nil {
		t.Fatal(r, err)
	}
	got.image.Release()
}

func TestCustomFeatureStagesReachPreparedBackendUnchanged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	raw := "FROM ubuntu:24.04 AS custom-stage\nRUN touch /fresh\nFROM " + agentboximage.RefAgentbox + "\nCOPY --from=custom-stage /fresh /fresh\n"
	if err := features.SaveUser(features.Feature{Name: "custom-stages", Install: &features.Install{Kind: features.InstallDockerfile, Dockerfile: raw}}); err != nil {
		t.Fatal(err)
	}
	prepared, err := agentboximage.PrepareComposed(t.Context(), agentboximage.RefAgentbox, []string{"go", "custom-stages"}, agentboximage.Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := prepared.BindBase("sha256:"+strings.Repeat("a", 64), "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	df, err := immutableDockerfile(r)
	if err != nil || !strings.Contains(string(df), raw) || !r.NoCache {
		t.Fatal("existing custom stage surface changed", r.NoCache, err)
	}
	r.Images, err = imagejob.DockerfileImages(r)
	if err != nil {
		t.Fatal(err)
	}
	for i := range r.Images {
		r.Images[i].Digest = r.BaseDigest
	}
	id := strings.Repeat("e", 64)
	phase := ImagePhase{PrivateVersion: 1, JobID: id, PreparedRef: "yoloctl/prepared/" + id, SolveRef: id}
	if _, err := privateSolveOptions(phase, t.TempDir(), r); err != nil {
		t.Fatal("prepared custom stage recipe rejected by backend", err)
	}
}
