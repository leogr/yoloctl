package ocifeature

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func archive(t *testing.T, entries []*tar.Header, bodies []string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		h.Size = int64(len(bodies[i]))
		if e := tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write([]byte(bodies[i])); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e := gz.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func featureArchive(t *testing.T, meta string) []byte {
	return archive(t, []*tar.Header{{Name: "devcontainer-feature.json", Mode: 0644}, {Name: "install.sh", Mode: 0755}}, []string{meta, "#!/bin/sh\necho hello\n"})
}

const testRef = "registry.example/features/tool:1"
const testMetadata = `{"id":"tool","version":"1.0.0","name":"Tool","options":{"version":{"type":"string","default":"latest","proposals":["latest","stable"]},"moby":{"type":"boolean","default":true},"choice":{"type":"string","default":"a","enum":["a","b"]}},"containerEnv":{"PATH":"/opt/tool/bin:${containerEnv:PATH}"}}`

func fixture(t *testing.T, metadata string) (*Registry, Lock, map[string][]byte) {
	t.Helper()
	blob := featureArchive(t, metadata)
	m := manifest{SchemaVersion: 2, Config: Descriptor{MediaType: "application/vnd.devcontainers"}, Layers: []Descriptor{{MediaType: layerType, Size: int64(len(blob)), Digest: digest(blob)}}}
	raw, _ := json.Marshal(m)
	l := Lock{Ref: testRef, Digest: digest(raw), Layer: m.Layers[0], Metadata: metadata}
	files := map[string][]byte{"/v2/features/tool/manifests/1": raw, "/v2/features/tool/manifests/" + l.Digest: raw, "/v2/features/tool/blobs/" + l.Layer.Digest: blob}
	r := &Registry{Client: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		body, ok := files[req.URL.Path]
		code := 200
		if !ok {
			code = 404
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return r, l, files
}
func TestRegistryResolutionAndIntegrity(t *testing.T) {
	r, l, files := fixture(t, testMetadata)
	got, e := r.Resolve(t.Context(), testRef)
	if e != nil {
		t.Fatal(e)
	}
	if got.Digest != l.Digest {
		t.Fatal("digest mismatch")
	}
	pinned := strings.Split(testRef, ":")[0] + "@" + l.Digest
	if _, e := r.Resolve(t.Context(), pinned); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"blob", "metadata", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			copy := l
			switch kind {
			case "blob":
				files["/v2/features/tool/blobs/"+l.Layer.Digest] = []byte("wrong")
				defer func() { files["/v2/features/tool/blobs/"+l.Layer.Digest] = featureArchive(t, testMetadata) }()
			case "metadata":
				copy.Metadata = `{"id":"other","version":"1"}`
			case "manifest":
				copy.Layer.Digest = "sha256:" + strings.Repeat("a", 64)
			}
			if _, e := r.Files(t.Context(), copy); e == nil {
				t.Fatal("accepted invalid artifact")
			}
		})
	}
}
func TestUnsafeArchives(t *testing.T) {
	for _, name := range []string{"../evil", "/absolute", "x/../evil", "x\\evil"} {
		t.Run(name, func(t *testing.T) {
			raw := archive(t, []*tar.Header{{Name: name, Mode: 0644}}, []string{"bad"})
			if _, e := unpack(raw); e == nil {
				t.Fatal("accepted unsafe path")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeChar} {
		raw := archive(t, []*tar.Header{{Name: "link", Typeflag: kind, Linkname: "/etc/passwd"}}, []string{""})
		if _, e := unpack(raw); e == nil {
			t.Fatal("accepted link/device")
		}
	}
	raw := archive(t, []*tar.Header{{Name: "install.sh"}, {Name: "install.sh"}}, []string{"a", "b"})
	if _, e := unpack(raw); e == nil {
		t.Fatal("duplicate accepted")
	}
	for _, names := range [][]string{{"tools", "tools/helper"}, {"tools/helper", "tools"}} {
		raw := archive(t, []*tar.Header{{Name: names[0]}, {Name: names[1]}}, []string{"a", "b"})
		if _, e := unpack(raw); e == nil || !strings.Contains(e.Error(), "also") {
			t.Fatalf("file/directory collision: %v", e)
		}
	}
}

func TestPlainAndCompressedTar(t *testing.T) {
	compressed := featureArchive(t, testMetadata)
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(gz)
	_ = gz.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{compressed, plain} {
		files, err := unpack(data)
		if err != nil || string(files["devcontainer-feature.json"]) != testMetadata || len(files["install.sh"]) == 0 {
			t.Fatalf("archive decode: %v", err)
		}
	}
}

func TestPinnedReferenceAndCancellation(t *testing.T) {
	r, l, _ := fixture(t, testMetadata)
	l.Ref = "registry.example/features/tool@sha256:" + strings.Repeat("c", 64)
	if _, err := r.Files(t.Context(), l); err == nil {
		t.Fatal("accepted a lock conflicting with its reference digest")
	}
	if _, err := Resolve(t.Context(), nil, []string{l.Ref}, nil, []Lock{l}); err == nil {
		t.Fatal("offline planning accepted a conflicting reference digest")
	}
	l.Ref = testRef
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Resolve(ctx, r, []string{testRef}, nil, []Lock{l}); err != context.Canceled {
		t.Fatalf("canceled resolution: %v", err)
	}
	other := l
	other.Digest = "sha256:" + strings.Repeat("c", 64)
	if _, err := Resolve(t.Context(), nil, []string{testRef}, nil, []Lock{l, other}); err == nil {
		t.Fatal("accepted conflicting saved locks")
	}
}
func TestTypedOptionsAndNoRuntimeAuthority(t *testing.T) {
	_, l, _ := fixture(t, testMetadata)
	m, e := l.Definition()
	if e != nil {
		t.Fatal(e)
	}
	for _, values := range []map[string]string{{"moby": "yes"}, {"choice": "c"}, {"unknown": "value"}, {"version": "bad\nRUN evil"}} {
		if _, e := m.Values(values); e == nil {
			t.Fatalf("accepted %v", values)
		}
	}
	for _, key := range []string{"privileged", "capAdd", "securityOpt", "mounts", "entrypoint", "postStartCommand", "init"} {
		copy := l
		copy.Metadata = strings.TrimSuffix(l.Metadata, "}") + fmt.Sprintf(",%q:true}", key)
		if e := copy.Compatible(); e == nil {
			t.Fatalf("accepted authority %s", key)
		}
	}
	opts, e := ApplyOptions([]string{testRef}, nil, []string{"tool.moby=false", "tool.version=1.2"})
	if e != nil || opts[testRef]["moby"] != "false" {
		t.Fatalf("%v %v", opts, e)
	}
	if _, e := ApplyOptions([]string{testRef, "other.example/feature/tool:1"}, nil, []string{"tool.moby=false"}); e == nil {
		t.Fatal("ambiguous alias accepted")
	}
}
func TestOfflinePlanAndOptionDrift(t *testing.T) {
	_, l, _ := fixture(t, testMetadata)
	plan, e := Resolve(context.Background(), nil, []string{testRef}, nil, []Lock{l})
	if e != nil {
		t.Fatal(e)
	}
	a, e := plan.Dockerfile()
	if e != nil {
		t.Fatal(e)
	}
	plan, e = Resolve(context.Background(), nil, []string{testRef}, map[string]map[string]string{testRef: {"version": "1.3", "moby": "false"}}, []Lock{l})
	if e != nil {
		t.Fatal(e)
	}
	b, e := plan.Dockerfile()
	if e != nil {
		t.Fatal(e)
	}
	if a == b || !strings.Contains(b, "MOBY='false'") || !strings.Contains(b, "${PATH}") {
		t.Fatal(b)
	}
	if _, e := Resolve(context.Background(), nil, []string{testRef}, nil, nil); e == nil {
		t.Fatal("missing lock silently ignored")
	}
	empty, e := Resolve(context.Background(), nil, nil, nil, []Lock{l})
	if e != nil || len(empty.Locks) != 0 {
		t.Fatal("stale lock retained")
	}
}
func TestDependenciesAndOrder(t *testing.T) {
	_, a, _ := fixture(t, testMetadata)
	b := a
	b.Ref = "registry.example/features/dependency:1"
	b.Digest = "sha256:" + strings.Repeat("b", 64)
	b.Metadata = `{"id":"dependency","version":"1.0.0"}`
	a.Metadata = `{"id":"tool","version":"1.0.0","dependsOn":{"registry.example/features/dependency:1":{}}}`
	p, e := Resolve(t.Context(), nil, []string{a.Ref}, nil, []Lock{a, b})
	if e != nil || len(p.Locks) != 2 || p.Locks[0].Ref != b.Ref {
		t.Fatalf("%+v %v", p, e)
	}
	b.Metadata = `{"id":"dependency","version":"1.0.0","installsAfter":["registry.example/features/tool"]}`
	if _, e := Resolve(t.Context(), nil, []string{a.Ref}, nil, []Lock{a, b}); e == nil {
		t.Fatal("soft cycle accepted")
	}
	b.Metadata = `{"id":"dependency","version":"1.0.0","dependsOn":{"registry.example/features/tool:1":{}}}`
	if _, e := Resolve(t.Context(), nil, []string{a.Ref}, nil, []Lock{a, b}); e == nil {
		t.Fatal("hard cycle accepted")
	}
}

func TestAliasesInstallOnceAndRemainLockedOffline(t *testing.T) {
	_, a, _ := fixture(t, testMetadata)
	b := a
	b.Ref = "registry.example/features/tool:latest"
	for _, previous := range [][]Lock{{a, b}, {b, a}} {
		plan, err := Resolve(t.Context(), nil, []string{a.Ref, b.Ref}, nil, previous)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Locks) != 2 || len(plan.installations()) != 1 {
			t.Fatalf("aliases lost or installed twice: %+v", plan)
		}
		loaded, err := Resolve(t.Context(), nil, []string{a.Ref, b.Ref}, nil, plan.Locks)
		if err != nil || len(loaded.installations()) != 1 {
			t.Fatalf("alias pins did not survive reload: %+v %v", loaded, err)
		}
		changed, err := Resolve(t.Context(), nil, []string{a.Ref, b.Ref}, map[string]map[string]string{b.Ref: {"version": "2"}}, plan.Locks)
		if err != nil || len(changed.installations()) != 2 {
			t.Fatalf("distinct options incorrectly deduplicated: %+v %v", changed, err)
		}
	}
}
func TestBearerAndNoDowngrade(t *testing.T) {
	calls := 0
	r := NewRegistry()
	r.Client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		h := make(http.Header)
		code := 200
		body := "data"
		if req.URL.Host == "auth.example" {
			body = `{"token":"test-token"}`
			if req.Header.Get("Authorization") != "" {
				t.Error("credentials sent to token endpoint")
			}
		} else if req.Header.Get("Authorization") == "" {
			code = 401
			h.Set("WWW-Authenticate", `Bearer realm="https://auth.example/token",service="registry.example",scope="repository:features/tool:pull"`)
		} else if req.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong token")
		}
		return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	data, e := r.get(t.Context(), "https://registry.example/blob", 20)
	if e != nil || string(data) != "data" || calls != 3 {
		t.Fatalf("%q %v %d", data, e, calls)
	}
	req, _ := http.NewRequest("GET", "http://registry.example/plain", nil)
	if r.Client.CheckRedirect(req, []*http.Request{req}) == nil {
		t.Fatal("HTTPS downgrade permitted")
	}
}
