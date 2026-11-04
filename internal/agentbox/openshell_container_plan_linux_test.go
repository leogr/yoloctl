package agentbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func nativeContainerPlanFixture(t *testing.T) (*Config, spaceenv.Context, openshell.QualifiedRelease) {
	t.Helper()
	current, _, _, manifest := nativeRuntimeFixture(t)
	hash := sha256.Sum256(manifest)
	release, err := openshell.LoadQualifiedRelease(manifest, hex.EncodeToString(hash[:]))
	if err != nil {
		t.Fatal(err)
	}
	c := &Config{Name: "fixture", Space: current.Space, Agent: "none", Account: "none", Image: "custom/image", Workdir: Workdir{Mode: WorkdirModeHost, Source: "/host/project", Canonical: "/host/project"}}
	return c, current, release
}

func createdNativePlanDetails(options container.RunOptions) *container.Details {
	privileged := false
	d := &container.Details{ID: strings.Repeat("a", 64), Name: options.Name, Status: "created", Privileged: &privileged, InactiveTaskFieldsObserved: true,
		Labels: maps.Clone(options.Labels), Command: slices.Clone(options.Command), Env: append([]string{"PATH=/image/tools:/usr/bin", openshellUserEnvironment + `={"IMAGE_PRIVATE":"image-value"}`}, options.Env...)}
	for _, mount := range options.Mounts {
		d.Mounts = append(d.Mounts, container.DetailMount(mount))
	}
	return d
}

func TestNativeContainerPlanKeepsActualWorkdirIntentAndExactIndependentHelper(t *testing.T) {
	for _, host := range []bool{false, true} {
		for _, writable := range []bool{false, true} {
			for _, unconfined := range []bool{false, true} {
				t.Run(fmt.Sprintf("host%v/write%v/unconfined%v", host, writable, unconfined), func(t *testing.T) {
					c, current, release := nativeContainerPlanFixture(t)
					if !host {
						c.Workdir = NewEphemeralWorkdir(c.Name)
					}
					if writable {
						c.Capabilities = []string{capabilities.WorkdirWriteCapability}
					}
					c.UnconfinedNetwork = unconfined
					policy, err := ComposePolicy(c)
					if err != nil {
						t.Fatal(err)
					}
					options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
					if err != nil {
						t.Fatal(err)
					}
					d := createdNativePlanDetails(options)
					input, err := plan.bootstrapForCreated(d)
					if err != nil {
						t.Fatal(err)
					}
					if input.Workdir != c.Workdir.ContainerPath() || input.ReadOnlyWorkdir == writable || input.Binding.ControllerGeneration != current.Generation || input.Environment["IMAGE_PRIVATE"] != "image-value" {
						t.Fatal("new bootstrap changed cwd, intent, private channel or generation")
					}
					if _, exists := input.Environment["PATH"]; exists {
						t.Fatal("image ENV was broadened into private SSH override map")
					}
					if options.Privileged || len(options.Sysctl) != 0 || options.Command[0] != sandbox.EntrypointShimPath || options.Command[1] != openshell.HelperBinary || ClassifyRuntime(d).Mode != RuntimeModeConfined {
						t.Fatal("native plan lost ordinary mode, shim or separate helper")
					}
					for _, mount := range options.Mounts {
						if mount.Destination == sandbox.BinaryDirContainerPath || mount.Destination == sandbox.PolicyContainerRegoPath {
							t.Fatal("new plan retained mutable legacy runtime bind")
						}
					}
					// nerdctl's fresh-created zero timestamp is also accepted;
					// previously-started stopped containers are refused below.
					d.StartedAt = "0001-01-01T00:00:00Z"
					if _, err := plan.bootstrapForCreated(d); err != nil {
						t.Fatal("valid unstarted zero timestamp refused", err)
					}
				})
			}
		}
	}
}

func TestNativeContainerPlanKeepsAccountOverridesPrivateAndPreservesPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, current, release := nativeContainerPlanFixture(t)
	c.Agent, c.Account = "claude", account.Default
	c.Env = map[string]EnvVar{openshellUserEnvironment: {Literal: `{"LITERAL_PRIVATE":"literal-value"}`}, "PUBLIC": {Literal: "public-value"}, "ANTHROPIC_API_KEY": {Literal: "overridden-literal"}}
	acct := account.New(c.Space, c.Account)
	acct.SetSecret("ANTHROPIC_API_KEY", "unit-account-private-value")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{strings.Join(options.Env, "\n"), strings.Join(options.Command, "\n"), fmt.Sprint(options.Labels), fmt.Sprintf("%v %#v", plan, plan)} {
		if strings.Contains(text, "unit-account-private-value") {
			t.Fatal("private account override leaked to argv/options/labels/formatting")
		}
	}
	input, err := plan.bootstrapForCreated(createdNativePlanDetails(options))
	if err != nil || len(input.Environment) != 2 || input.Environment["ANTHROPIC_API_KEY"] != "unit-account-private-value" || input.Environment["PUBLIC"] != "public-value" {
		t.Fatal("account channel did not replace image/literal map privately", err)
	}
	// No injected account value: preserve the effective literal-over-image
	// private channel without merging other image ENV into later SSH.
	c.Agent, c.Account = "none", "none"
	options, plan, err = planNativeContainer(c, c.Image, current, release, strings.Repeat("e", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	input, err = plan.bootstrapForCreated(createdNativePlanDetails(options))
	if err != nil || len(input.Environment) != 3 || input.Environment["LITERAL_PRIVATE"] != "literal-value" || input.Environment["PUBLIC"] != "public-value" || input.Environment["ANTHROPIC_API_KEY"] != "overridden-literal" {
		t.Fatal("literal-over-image private channel precedence changed", err)
	}
}

func TestNativeContainerPlanRefusesDifferentOrPreviouslyStartedContainer(t *testing.T) {
	c, current, release := nativeContainerPlanFixture(t)
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*container.Details){
		"running":             func(d *container.Details) { d.Running, d.PID = true, 42 },
		"stopped":             func(d *container.Details) { d.Status = "exited" },
		"used-before":         func(d *container.Details) { d.StartedAt = "2026-10-31T01:00:00Z" },
		"bad-time":            func(d *container.Details) { d.StartedAt = "unknown" },
		"other-container":     func(d *container.Details) { d.Name = "other" },
		"privileged":          func(d *container.Details) { *d.Privileged = true },
		"different-network":   func(d *container.Details) { d.Labels[LabelNetworkUnconfined] = "true" },
		"invalid-private-map": func(d *container.Details) { d.Env = []string{openshellUserEnvironment + `={"API_KEY":1}`} },
		"invalid-private-key": func(d *container.Details) { d.Env = []string{openshellUserEnvironment + `={"bad=name":"value"}`} },
	} {
		t.Run(name, func(t *testing.T) {
			d := createdNativePlanDetails(options)
			mutate(d)
			if _, err := plan.bootstrapForCreated(d); err == nil {
				t.Fatal("different or previously-started container accepted")
			}
		})
	}
	// Launch/bind differences are warnings, not refusals (parity with the old
	// path; engines such as Lima's may report binds differently).
	for name, mutate := range map[string]func(*container.Details){
		"other-incarnation": func(d *container.Details) { d.Labels[openshell.LabelIncarnation] = strings.Repeat("e", 32) },
		"unknown-privilege": func(d *container.Details) { d.Privileged = nil },
		"wrong-workdir": func(d *container.Details) {
			for i := range d.Mounts {
				if d.Mounts[i].Destination == plan.launch.Workdir {
					d.Mounts[i].Source = "/other/project"
				}
			}
		},
		"writable-helper": func(d *container.Details) {
			for i := range d.Mounts {
				if d.Mounts[i].Destination == openshell.HelperDirectory {
					d.Mounts[i].ReadOnly = false
				}
			}
		},
	} {
		t.Run("warn-"+name, func(t *testing.T) {
			var notices []error
			old := nativeCreateNotice
			nativeCreateNotice = func(_ string, err error) { notices = append(notices, err) }
			defer func() { nativeCreateNotice = old }()
			d := createdNativePlanDetails(options)
			mutate(d)
			if _, err := plan.bootstrapForCreated(d); err != nil || len(notices) != 1 {
				t.Fatal("launch/bind difference refused instead of warned", err, notices)
			}
		})
	}
}

func TestNativeContainerPlanRefusesInfrastructureAliasesAndPolicyMismatch(t *testing.T) {
	for _, target := range []string{"/.openshell", "/.openshell/native/current.json", "/opt", openshell.HelperDirectory, "/tmp/../.openshell/runtime"} {
		t.Run(target, func(t *testing.T) {
			c, current, release := nativeContainerPlanFixture(t)
			c.Mounts = []Mount{{Source: "/unit/extra", Target: target}}
			policy, err := ComposePolicy(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy); err == nil {
				t.Fatal("mount alias could mask fixed runtime infrastructure")
			}
		})
	}
	c, current, release := nativeContainerPlanFixture(t)
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Capabilities = []string{capabilities.WorkdirWriteCapability}
	if _, _, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy); err == nil {
		t.Fatal("policy/workdir intent mismatch accepted before create")
	}
	c.Capabilities = nil
	c.Mounts = []Mount{{Source: "/unit/extra", Target: "/tools", ReadOnly: true}, {Source: "/unit/old-path-data", Target: sandbox.BinaryDirContainerPath, ReadOnly: true}}
	options, _, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("e", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range c.Mounts {
		if !slices.Contains(options.Mounts, container.Mount{Source: mount.Source, Destination: mount.Target, ReadOnly: mount.ReadOnly}) {
			t.Fatal("ordinary extra mount was silently removed")
		}
	}
}

// Mismatched native runtime records route natively with a named warning (old
// path parity); a privileged report still refuses.
func TestNativeRuntimeRouteWarnsOnMixedEvidence(t *testing.T) {
	c, current, release := nativeContainerPlanFixture(t)
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	var notices []error
	old := nativeRouteNotice
	nativeRouteNotice = func(_ string, err error) { notices = append(notices, err) }
	defer func() { nativeRouteNotice = old }()
	d := createdNativePlanDetails(options)
	for i := range d.Mounts {
		if d.Mounts[i].Destination == openshell.HelperDirectory {
			d.Mounts[i].Source = "/lima/resolved/helper"
		}
	}
	if native, err := NativeRuntimeRoute(d); !native || err != nil || len(notices) != 1 {
		t.Fatal("mixed evidence refused instead of warned", native, err, notices)
	}
	*d.Privileged = true
	if _, err := NativeRuntimeRoute(d); err == nil {
		t.Fatal("privileged report routed natively")
	}
	_ = plan
}
