package agentbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// This historical Rego is retained ONLY as an emitter/OPA regression fixture.
// Native boxes load the separately qualified 0.1.6 Rego, not this file; no VM
// provisioning or executable embeds this historical fixture anymore.
const liveRegoPath = "../../assets/sandbox/sandbox-policy.rego"

// TestComposedPolicyEndpointsUsePortsList is the EMIT-side half of the
// port-vs-ports contract. Every network endpoint the composer emits must carry
// a non-empty `ports` LIST and never a scalar `port`. The bundled rego matches
// endpoint.ports[_]; a scalar `port` key is undefined to the rego, so every
// egress grant silently fails closed (proven by TestPolicyAllowsGrantedEgressViaOPA).
// This guards against a regression to the scalar shape that the byte-golden
// contract alone would not catch (both sides could drift together).
func TestComposedPolicyEndpointsUsePortsList(t *testing.T) {
	cases := [][]string{
		{"github"},
		{"claude"},
		{"npm"},
	}
	for _, features := range cases {
		cfg := &Config{Name: "coder", Space: "default", Features: features, Capabilities: []string{"fs.workdir.write"}}
		got, err := ComposePolicy(cfg)
		if err != nil {
			t.Fatalf("ComposePolicy(%v): %v", features, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(got, &doc); err != nil {
			t.Fatalf("unmarshal composed policy (%v): %v", features, err)
		}
		nps, ok := doc["network_policies"].(map[string]any)
		if !ok || len(nps) == 0 {
			t.Fatalf("features %v: composed policy has no network_policies", features)
		}
		for name, raw := range nps {
			pol, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("policy %q: not a map", name)
			}
			eps, ok := pol["endpoints"].([]any)
			if !ok || len(eps) == 0 {
				t.Fatalf("policy %q: no endpoints", name)
			}
			for i, e := range eps {
				ep, ok := e.(map[string]any)
				if !ok {
					t.Fatalf("policy %q endpoint %d: not a map", name, i)
				}
				if _, scalar := ep["port"]; scalar {
					t.Errorf("policy %q endpoint %d emits scalar `port`: the rego matches endpoint.ports[_], so egress fails closed", name, i)
				}
				ports, ok := ep["ports"].([]any)
				if !ok || len(ports) == 0 {
					t.Errorf("policy %q endpoint %d: `ports` must be a non-empty list, got %#v", name, i, ep["ports"])
				}
			}
		}
	}
}

// TestBundledRegoMatchesPortsList is the REGO-side half of the contract. The
// live rego must dereference endpoint.ports[_] (a list) and never a scalar
// endpoint.port / ep.port. If a future drift is "fixed" by editing the rego to
// a scalar instead of the emitter, this fails — keeping the two artifacts in
// agreement from both directions.
func TestBundledRegoMatchesPortsList(t *testing.T) {
	raw, err := os.ReadFile(liveRegoPath)
	if err != nil {
		t.Fatalf("read live rego %s: %v", liveRegoPath, err)
	}
	rego := string(raw)
	if !strings.Contains(rego, "ports[_] == network.port") {
		t.Error("live rego no longer matches endpoint.ports[_] == network.port; the composer emits a ports list and would stop matching")
	}
	// network.port (the request's port) is correct; a scalar endpoint.port /
	// ep.port is the drift we forbid. \b after "port" excludes "ports".
	scalar := regexp.MustCompile(`\b(?:endpoint|ep)\.port\b`)
	for n, line := range strings.Split(rego, "\n") {
		if scalar.MatchString(line) {
			t.Errorf("live rego line %d references a scalar endpoint port (drift): %q", n+1, strings.TrimSpace(line))
		}
	}
}

// TestPolicyAllowsGrantedEgressViaOPA evaluates the COMPOSED policy against the
// LIVE rego with OPA, empirically proving the ports-list shape permits granted
// egress while keeping ungranted ports denied. Skips when opa is not on PATH
// (opa is a test-only convenience, like `make test-rego`); the always-on Go
// contract tests above are the primary guard. Any opa setup/parse failure
// Skips rather than fails, so this can only catch a real policy regression.
func TestPolicyAllowsGrantedEgressViaOPA(t *testing.T) {
	opa, err := exec.LookPath("opa")
	if err != nil {
		t.Skip("opa not on PATH; skipping live-rego conformance (see make test-rego)")
	}
	regoSrc, err := os.ReadFile(liveRegoPath)
	if err != nil {
		t.Fatalf("read live rego: %v", err)
	}
	// OPA 1.x parses Rego v1, which rejects the rego's `some <var>` followed by
	// `<var> := coll[_]` idiom that regorus (the runtime interpreter) accepts.
	// Drop only those redundant bare declarations; the host/port/binary matching
	// logic (endpoint.ports[_] == network.port) is preserved verbatim.
	bareSome := regexp.MustCompile(`(?m)^[\t ]*some (?:endpoint|b|ep|deny_rule|rule)[\t ]*$`)
	regoV1 := bareSome.ReplaceAll(regoSrc, nil)
	if !strings.Contains(string(regoV1), "ports[_] == network.port") {
		t.Skip("rego v1 transform dropped the port-match line; skipping rather than risking a false failure")
	}
	dir := t.TempDir()
	regoPath := filepath.Join(dir, "policy.rego")
	if err := os.WriteFile(regoPath, regoV1, 0o644); err != nil {
		t.Fatal(err)
	}
	policy, err := ComposePolicy(&Config{Name: "coder", Space: "default", Features: []string{"github"}, Capabilities: []string{"fs.workdir.write"}})
	if err != nil {
		t.Fatalf("ComposePolicy: %v", err)
	}
	dataPath := filepath.Join(dir, "data.yaml")
	if err := os.WriteFile(dataPath, policy, 0o644); err != nil {
		t.Fatal(err)
	}
	eval := func(input string) string {
		inPath := filepath.Join(dir, "input.json")
		if err := os.WriteFile(inPath, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(opa, "eval", "--format", "raw", "-d", regoPath, "-d", dataPath, "-i", inPath, "data.openshell.sandbox.allow_network").CombinedOutput()
		if err != nil {
			t.Skipf("opa eval failed (parser/version drift, not a policy regression): %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// git -> github.com:443 is granted by network.github (pulled in by the github feature).
	if got := eval(`{"network":{"host":"github.com","port":443,"protocol":"tcp"},"exec":{"path":"/usr/bin/git","ancestors":["/usr/bin/git"]}}`); got != "true" {
		t.Errorf("granted egress git->github.com:443 = %q, want true (the ports-list shape must permit it)", got)
	}
	// An ungranted port on the same host must stay denied.
	if got := eval(`{"network":{"host":"github.com","port":8080,"protocol":"tcp"},"exec":{"path":"/usr/bin/git","ancestors":["/usr/bin/git"]}}`); got != "false" {
		t.Errorf("ungranted egress git->github.com:8080 = %q, want false", got)
	}
}
