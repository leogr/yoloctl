package openshell

import (
	"strings"
	"testing"
)

func TestWorkspacePolicyRejectsImplicitWritesAndLaunchDisagreement(t *testing.T) {
	in := bootstrapInput()
	in.Binding.Qualification = strings.Repeat("b", 64)
	c := LaunchConfig{BootstrapSchema, in.Binding, in.UID, in.GID, in.Workdir, true, false, strings.Repeat("a", 64)}
	readOnly := "filesystem_policy:\n  include_workdir: false\n  read_only: [/usr, /workdir]\n  read_write: [/tmp, /run, /home/sandbox]\n"
	if err := c.ValidateWorkspacePolicy([]byte(readOnly)); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{
		strings.Replace(readOnly, "include_workdir: false", "include_workdir: true", 1),
		strings.Replace(readOnly, "  include_workdir: false\n", "", 1),
		strings.Replace(readOnly, "/workdir", "/other", 1),
		strings.Replace(readOnly, "[/tmp, /run, /home/sandbox]", "[/workdir]", 1),
		strings.Replace(readOnly, "[/tmp, /run, /home/sandbox]", "[/workdir/project/cache]", 1),
		strings.Replace(readOnly, "[/tmp, /run, /home/sandbox]", "[/]", 1),
		strings.Replace(readOnly, "include_workdir: false", "include_workdir: true\n  include_workdir: false", 1),
	} {
		if err := c.ValidateWorkspacePolicy([]byte(policy)); err == nil {
			t.Fatal("unsafe read-only policy accepted", policy)
		}
	}
	c.WorkspaceReadOnly = false
	if err := c.ValidateWorkspacePolicy([]byte(readOnly)); err == nil {
		t.Fatal("writable bootstrap accepted a read-only policy")
	}
	writable := "filesystem_policy:\n  include_workdir: true\n  read_only: [/usr]\n  read_write: [/tmp, /run, /home/sandbox, /workdir]\n"
	if err := c.ValidateWorkspacePolicy([]byte(writable)); err != nil {
		t.Fatal(err)
	}
}
