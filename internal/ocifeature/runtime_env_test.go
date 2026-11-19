package ocifeature

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envByKey(entries []EnvEntry) map[string]EnvEntry {
	out := map[string]EnvEntry{}
	for _, e := range entries {
		out[e.Key] = e
	}
	return out
}

func TestEnvironmentFollowsDockerOrder(t *testing.T) {
	_, base, _ := fixture(t, testMetadata)
	t.Setenv("HOSTONLY", "from-host")
	first := lockAs(base, "registry.example/features/a:1", "a",
		`{"id":"a","version":"1","containerEnv":{"A_HOME":"/opt/a","B_BIN":"${containerEnv:A_HOME}/bin","EARLY":"kept","HOSTVAR":"${HOSTONLY}","LD_LIBRARY_PATH":"/opt/a/lib","XDG_DATA_HOME":"/opt/a/share"}}`)
	second := lockAs(base, "registry.example/features/b:1", "b",
		`{"id":"b","version":"1","dependsOn":{"registry.example/features/a:1":{}},"containerEnv":{"EARLY":"${MISSING}","LATE":"${A_HOME}:${B_BIN}"}}`)
	plan, err := Resolve(t.Context(), nil, []string{second.Ref}, nil, []Lock{first, second})
	if err != nil {
		t.Fatal(err)
	}
	got := envByKey(plan.Environment())
	if got["B_BIN"].Value != "/opt/a/bin" || got["LATE"].Value != "/opt/a:/opt/a/bin" {
		t.Fatalf("references not resolved in Dockerfile order: %+v", got)
	}
	if e := got["EARLY"]; e.Value != "" || !strings.Contains(e.Skip, "MISSING") || e.Ref != second.Ref {
		t.Fatalf("a later unresolved assignment kept the earlier value: %+v", e)
	}
	if e := got["HOSTVAR"]; e.Value != "" || e.Skip == "" {
		t.Fatalf("expanded against the host environment: %+v", e)
	}
	for _, key := range []string{"LD_LIBRARY_PATH", "XDG_DATA_HOME"} {
		if e := got[key]; e.Skip != "reserved" || e.Value != "" {
			t.Fatalf("%s not reserved: %+v", key, e)
		}
	}
}

func TestPathCandidatesClassifyFolders(t *testing.T) {
	_, base, _ := fixture(t, testMetadata)
	goLock := lockAs(base, "ghcr.io/devcontainers/features/go:1", "a",
		`{"id":"go","version":"1","containerEnv":{"GOROOT":"/usr/local/go","GOPATH":"/go","PATH":"/usr/local/go/bin:/go/bin:${PATH}"}}`)
	rustLock := lockAs(base, "ghcr.io/devcontainers/features/rust:1", "b",
		`{"id":"rust","version":"1","containerEnv":{"CARGO_HOME":"/usr/local/cargo","RUSTUP_HOME":"/usr/local/rustup","CONFIG":"/opt/rust/config.toml","PATH":"/usr/local/cargo/bin:${PATH}"}}`)
	plan, err := Resolve(t.Context(), nil, []string{goLock.Ref, rustLock.Ref}, nil, []Lock{goLock, rustLock})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range plan.PathCandidates() {
		got[c.Path] = c.ReadWrite
	}
	want := map[string]bool{"/go": true, "/go/bin": false, "/usr/local/go": true, "/usr/local/go/bin": false,
		"/usr/local/cargo": true, "/usr/local/cargo/bin": false, "/usr/local/rustup": false, "/opt/rust/config.toml": false}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v", got)
	}
	for path, rw := range want {
		if v, ok := got[path]; !ok || v != rw {
			t.Errorf("%s: read-write=%v present=%v, want %v", path, v, ok, rw)
		}
	}
	dockerfile, err := plan.Dockerfile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dockerfile, "RUN for p in '/go' '/go/bin'") || !strings.Contains(dockerfile, "resolves to protected") {
		t.Fatalf("link guard missing:\n%s", dockerfile)
	}
}

func TestNoLinkGuardWithoutFolders(t *testing.T) {
	_, l, _ := fixture(t, `{"id":"tool","version":"1.0.0"}`)
	plan, err := Resolve(t.Context(), nil, []string{testRef}, nil, []Lock{l})
	if err != nil {
		t.Fatal(err)
	}
	if dockerfile, _ := plan.Dockerfile(); strings.Contains(dockerfile, "readlink") {
		t.Fatalf("guard emitted for a Feature without folders:\n%s", dockerfile)
	}
}

func TestProtectedPath(t *testing.T) {
	for p, protected := range map[string]bool{
		"/": true, "/etc": true, "/etc/x": true, "/opt": true, "/opt/openshell": true, "/opt/yoloctl/bin": true,
		"/usr": true, "/usr/bin": true, "/usr/local": true, "/usr/local/bin": true, "/usr/local/lib": true,
		"/usr/local/lib/yoloctl/x": true, "/usr/local/share/airc": true, "/home": true, "/home/sandbox/go": true,
		"/workdir": true, "/run/x": true, "/var/lib/x": true,
		"/go": false, "/opt/tool": false, "/usr/local/go": false, "/usr/local/cargo": false,
		"/usr/local/share/nvm": false, "/usr/local/lib/node": false,
	} {
		if got := ProtectedPath(p); got != protected {
			t.Errorf("ProtectedPath(%q) = %v, want %v", p, got, protected)
		}
	}
}

func TestLinkGuardRefusesLinksIntoProtectedPlaces(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the guard runs in the Linux image build")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "tool")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	script := func(paths ...string) string { return guardScript(linkGuard(paths, nil)) }
	if out, err := exec.Command("sh", "-c", script("/opt/yoloctl-test-missing-feature-folder")).CombinedOutput(); err != nil {
		t.Fatalf("a missing folder outside protected places failed the guard: %v %s", err, out)
	}
	if out, err := exec.Command("sh", "-c", script(link)).CombinedOutput(); err == nil || !strings.Contains(string(out), "resolves to protected /etc") {
		t.Fatalf("a link into /etc passed the guard: %v %s", err, out)
	}
	// A dangling link into a protected place (for example a mount destination
	// that exists only at runtime) must fail too.
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink("/run/yoloctl/not-there-yet", dangling); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", script(dangling)).CombinedOutput(); err == nil || !strings.Contains(string(out), "resolves to protected /run/yoloctl/not-there-yet") {
		t.Fatalf("a dangling link into /run passed the guard: %v %s", err, out)
	}
}

func TestUnbracedReferencesAndExpandedPathEntries(t *testing.T) {
	_, base, _ := fixture(t, testMetadata)
	l := lockAs(base, "registry.example/features/tool:2", "a",
		`{"id":"tool","version":"1","containerEnv":{"A_HOME":"/opt/tool","B_BIN":"$A_HOME/bin","C_ODD":"cost $5","PATH":"${A_HOME}/bin:${PATH}"}}`)
	plan, err := Resolve(t.Context(), nil, []string{l.Ref}, nil, []Lock{l})
	if err != nil {
		t.Fatal(err)
	}
	got := envByKey(plan.Environment())
	// Keys render sorted per installation, so A_HOME is set before B_BIN and PATH.
	if got["B_BIN"].Value != "/opt/tool/bin" {
		t.Fatalf("unbraced reference not expanded: %+v", got["B_BIN"])
	}
	if e := got["C_ODD"]; e.Value != "" || e.Skip == "" {
		t.Fatalf("an unexpanded $ form was delivered: %+v", e)
	}
	rw := map[string]bool{}
	for _, c := range plan.PathCandidates() {
		rw[c.Path] = c.ReadWrite
	}
	if !rw["/opt/tool"] {
		t.Fatalf("a home whose PATH entry is written with a reference stayed read-only: %v", rw)
	}
}

func guardScript(guard string) string {
	i := strings.Index(guard, "RUN ")
	return guard[i+len("RUN "):]
}

func TestLinkGuardChecksWritableTargetsOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the guard runs in the Linux image build")
	}
	dir := t.TempDir()
	toGo := filepath.Join(dir, "home")
	if err := os.Symlink("/usr/local/go", toGo); err != nil {
		t.Fatal(err)
	}
	benign := filepath.Join(dir, "current")
	if err := os.Symlink("/usr/local/python/3.12", benign); err != nil {
		t.Fatal(err)
	}
	run := func(paths, writable []string) (string, error) {
		out, err := exec.Command("sh", "-c", guardScript(linkGuard(paths, writable))).CombinedOutput()
		return string(out), err
	}
	// A versioned link: fine read-only and writable.
	if out, err := run([]string{benign}, []string{benign}); err != nil {
		t.Fatalf("a benign link failed: %v %s", err, out)
	}
	// Read-only access to the Go toolchain through a link is fine.
	if out, err := run([]string{toGo}, nil); err != nil {
		t.Fatalf("a read-only link to a network-tool folder failed: %v %s", err, out)
	}
	// Writable through a link into the Go toolchain is refused.
	if out, err := run([]string{toGo}, []string{toGo}); err == nil || !strings.Contains(out, "/usr/local/go/bin/go") {
		t.Fatalf("a writable link into the Go toolchain passed: %v %s", err, out)
	}
}

func TestGoRustGuardAllowsTheOrdinaryBuild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the guard runs in the Linux image build")
	}
	_, base, _ := fixture(t, testMetadata)
	goLock := lockAs(base, "ghcr.io/devcontainers/features/go:1", "a",
		`{"id":"go","version":"1","containerEnv":{"GOROOT":"/usr/local/go","GOPATH":"/go","PATH":"/usr/local/go/bin:/go/bin:${PATH}"}}`)
	rustLock := lockAs(base, "ghcr.io/devcontainers/features/rust:1", "b",
		`{"id":"rust","version":"1","containerEnv":{"CARGO_HOME":"/usr/local/cargo","RUSTUP_HOME":"/usr/local/rustup","PATH":"/usr/local/cargo/bin:${PATH}"}}`)
	plan, err := Resolve(t.Context(), nil, []string{goLock.Ref, rustLock.Ref}, nil, []Lock{goLock, rustLock})
	if err != nil {
		t.Fatal(err)
	}
	paths, writable := plan.GrantablePaths()
	if strings.Join(writable, " ") != "/go /usr/local/cargo" {
		t.Fatalf("writable folders checked by the guard: %v", writable)
	}
	if out, err := exec.Command("sh", "-c", guardScript(linkGuard(paths, writable))).CombinedOutput(); err != nil {
		t.Fatalf("the ordinary Go/Rust guard failed: %v %s", err, out)
	}
}

func TestExpansionIsBounded(t *testing.T) {
	oldValue, oldTotal := maxExpandedValue, maxExpandedTotal
	t.Cleanup(func() { maxExpandedValue, maxExpandedTotal = oldValue, oldTotal })
	maxExpandedValue, maxExpandedTotal = 16, 35
	_, base, _ := fixture(t, testMetadata)
	l := lockAs(base, "registry.example/features/big:1", "a",
		`{"id":"big","version":"1","containerEnv":{"A":"0123456789","B":"${A}${A}","C":"$A","D":"abcdefghij","E":"klmnopqrst","F":"x"}}`)
	plan, err := Resolve(t.Context(), nil, []string{l.Ref}, nil, []Lock{l})
	if err != nil {
		t.Fatal(err)
	}
	got := envByKey(plan.Environment())
	if got["A"].Value != "0123456789" || got["C"].Value != "0123456789" {
		t.Fatalf("values within the budget lost: %+v", got)
	}
	if e := got["B"]; e.Value != "" || e.Skip != "expanded value is too large" {
		t.Fatalf("a value over the per-value budget was delivered: %+v", e)
	}
	// A (10) + C (10) + D (10) = 30; E would reach 40 > 35, so it stops.
	if e := got["E"]; e.Value != "" || e.Skip != "expanded value is too large" {
		t.Fatalf("the total budget did not stop E: %+v", e)
	}
}

func TestDeliveredFeatureVariablesAreCounted(t *testing.T) {
	_, base, _ := fixture(t, testMetadata)
	for _, n := range []int{maxDeliveredEnv, maxDeliveredEnv + 1} {
		keys := make([]string, n)
		for i := range keys {
			keys[i] = fmt.Sprintf("%q:\"v\"", fmt.Sprintf("K%04d", i))
		}
		l := lockAs(base, "registry.example/features/many:1", "a", `{"id":"many","version":"1","containerEnv":{`+strings.Join(keys, ",")+`}}`)
		plan, err := Resolve(t.Context(), nil, []string{l.Ref}, nil, []Lock{l})
		if err != nil {
			t.Fatal(err)
		}
		delivered, skipped := 0, 0
		for _, e := range plan.Environment() {
			switch e.Skip {
			case "":
				delivered++
			case "too many Feature variables":
				skipped++
			}
		}
		if delivered != maxDeliveredEnv || skipped != n-maxDeliveredEnv {
			t.Fatalf("%d variables: delivered %d, skipped %d", n, delivered, skipped)
		}
	}
}
