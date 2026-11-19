package ocifeature

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Upstream shapes: go:1 asks init + capAdd + securityOpt, rust:1 capAdd +
// securityOpt only.
const goRuntime = `,"init":true,"capAdd":["SYS_PTRACE"],"securityOpt":["seccomp=unconfined"]`
const rustRuntime = `,"capAdd":["SYS_PTRACE"],"securityOpt":["seccomp=unconfined"]`

func lockAs(base Lock, ref string, digestChar string, metadata string) Lock {
	l := base
	l.Ref = ref
	l.Digest = "sha256:" + strings.Repeat(digestChar, 64)
	l.Metadata = metadata
	return l
}

func withRuntime(metadata, runtime string) string {
	return strings.TrimSuffix(metadata, "}") + runtime + "}"
}

func TestRuntimeRequestsAreIgnoredAndListed(t *testing.T) {
	_, plain, _ := fixture(t, testMetadata)
	plainPlan, err := Resolve(t.Context(), nil, []string{testRef}, nil, []Lock{plain})
	if err != nil || len(plainPlan.Ignored) != 0 {
		t.Fatalf("plain Feature reported ignored requests: %+v %v", plainPlan.Ignored, err)
	}
	plainDockerfile, err := plainPlan.Dockerfile()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		runtime string
		want    []RuntimeRequest
	}{
		"go":   {goRuntime, []RuntimeRequest{{"capAdd", `["SYS_PTRACE"]`}, {"init", "true"}, {"securityOpt", `["seccomp=unconfined"]`}}},
		"rust": {rustRuntime, []RuntimeRequest{{"capAdd", `["SYS_PTRACE"]`}, {"securityOpt", `["seccomp=unconfined"]`}}},
	} {
		t.Run(name, func(t *testing.T) {
			l := plain
			l.Metadata = withRuntime(testMetadata, tc.runtime)
			plan, err := Resolve(t.Context(), nil, []string{testRef}, nil, []Lock{l})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Ignored) != 1 || plan.Ignored[0].Ref != testRef || !slices.Equal(plan.Ignored[0].Requests, tc.want) {
				t.Fatalf("ignored = %+v", plan.Ignored)
			}
			got, err := plan.Dockerfile()
			if err != nil {
				t.Fatal(err)
			}
			// Same locked inputs, same installer: runtime keys are never rendered.
			if got != plainDockerfile {
				t.Fatalf("runtime requests changed the image:\n%s\nvs\n%s", got, plainDockerfile)
			}
		})
	}
}

func TestNoEffectRuntimeValuesAndUnknownProperties(t *testing.T) {
	_, l, _ := fixture(t, testMetadata)
	for _, runtime := range []string{`,"init":false,"privileged":false,"capAdd":[],"securityOpt":[],"mounts":[]`, ",\"init\": false,\"capAdd\":[ ],\"securityOpt\":[\n],\"mounts\":[\t ]"} {
		l.Metadata = withRuntime(testMetadata, runtime)
		plan, err := Resolve(t.Context(), nil, []string{testRef}, nil, []Lock{l})
		if err != nil || len(plan.Ignored) != 0 {
			t.Fatalf("no-effect values listed as ignored (%q): %+v %v", runtime, plan.Ignored, err)
		}
	}
	l.Metadata = withRuntime(testMetadata, `,"bogus":true`)
	if _, err := Resolve(t.Context(), nil, []string{testRef}, nil, []Lock{l}); err == nil || !strings.Contains(err.Error(), `unsupported Feature property "bogus"`) {
		t.Fatalf("a property outside the closed list was accepted: %v", err)
	}
	if _, err := (Plan{Locks: []Lock{l}}).Dockerfile(); err == nil {
		t.Fatal("renderer accepted an unknown property")
	}
	long := `,"capAdd":[` + strings.Repeat(`"CAP_LONG_NAME",`, 20) + `"LAST"]`
	l.Metadata = withRuntime(testMetadata, long)
	plan, err := Resolve(t.Context(), nil, []string{testRef}, nil, []Lock{l})
	if err != nil || len(plan.Ignored) != 1 || !strings.HasSuffix(plan.Ignored[0].Requests[0].Value, "…(truncated)") ||
		len(plan.Ignored[0].Requests[0].Value) > runtimeValueLimit+len("…(truncated)") {
		t.Fatalf("long value not cut and marked: %+v %v", plan.Ignored, err)
	}
}

func TestSharedDependencyRequestsListedOnce(t *testing.T) {
	_, base, _ := fixture(t, testMetadata)
	const (
		refA = "registry.example/features/a:1"
		refB = "registry.example/features/b:1"
		refX = "registry.example/features/x:1"
		refY = "registry.example/features/y:1"
	)
	locks := []Lock{
		lockAs(base, refA, "a", fmt.Sprintf(`{"id":"a","version":"1.0.0","dependsOn":{%q:{}}}`, refX)),
		lockAs(base, refB, "b", fmt.Sprintf(`{"id":"b","version":"1.0.0","dependsOn":{%q:{}}}`, refX)),
		lockAs(base, refX, "c", fmt.Sprintf(`{"id":"x","version":"1.0.0","dependsOn":{%q:{}}}`, refY)),
		lockAs(base, refY, "d", `{"id":"y","version":"1.0.0","init":true}`),
	}
	for _, roots := range [][]string{{refA, refB}, {refB, refA}} {
		plan, err := Resolve(t.Context(), nil, roots, nil, locks)
		if err != nil || len(plan.Ignored) != 1 || plan.Ignored[0].Ref != refY {
			t.Fatalf("roots %v: %+v %v", roots, plan.Ignored, err)
		}
	}
}

func TestAliasOrderCannotHideRuntimeRequests(t *testing.T) {
	_, plain, _ := fixture(t, testMetadata)
	asking := plain
	asking.Ref = "registry.example/features/tool:latest"
	asking.Metadata = withRuntime(testMetadata, `,"init":true`)
	for _, roots := range [][]string{{plain.Ref, asking.Ref}, {asking.Ref, plain.Ref}} {
		plan, err := Resolve(t.Context(), nil, roots, nil, []Lock{plain, asking})
		if err != nil || len(plan.Ignored) != 1 || len(plan.Ignored[0].Requests) != 1 || plan.Ignored[0].Requests[0].Key != "init" {
			t.Fatalf("roots %v: alias requests lost: %+v %v", roots, plan.Ignored, err)
		}
	}
}
