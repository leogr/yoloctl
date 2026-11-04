package openshell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeMountLaunch() LaunchConfig {
	in := bootstrapInput()
	in.Binding.Qualification = strings.Repeat("b", 64)
	return LaunchConfig{BootstrapSchema, in.Binding, in.UID, in.GID, in.Workdir, true, false, strings.Repeat("a", 64)}
}

func TestRuntimeMountProofRequiresOpenedIdentityAndExactWriteMode(t *testing.T) {
	launch := runtimeMountLaunch()
	mounts := []runtimeMount{{1, HelperDirectory, true}, {2, ArtifactProjection, true}, {3, NativeProjection, false}, {4, launch.Workdir, true}}
	selected := map[string]uint64{HelperDirectory: 1, ArtifactProjection: 2, NativeProjection: 3, launch.Workdir: 4}
	if err := validateRuntimeMounts(launch, mounts, selected); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]runtimeMount, map[string]uint64) []runtimeMount{
		func(m []runtimeMount, _ map[string]uint64) []runtimeMount { m[0].readOnly = false; return m },
		func(m []runtimeMount, _ map[string]uint64) []runtimeMount { m[1].point += "/other"; return m },
		func(m []runtimeMount, _ map[string]uint64) []runtimeMount { m[2].readOnly = true; return m },
		func(m []runtimeMount, _ map[string]uint64) []runtimeMount { m[3].readOnly = false; return m },
		func(m []runtimeMount, _ map[string]uint64) []runtimeMount { m[3].point = "/"; return m },
		func(m []runtimeMount, s map[string]uint64) []runtimeMount { s[ArtifactProjection] = 99; return m },
		func(m []runtimeMount, s map[string]uint64) []runtimeMount { delete(s, NativeProjection); return m },
		func(m []runtimeMount, _ map[string]uint64) []runtimeMount {
			return append(m, runtimeMount{5, ArtifactProjection + "/openshell-sandbox", false})
		},
	} {
		copy := append([]runtimeMount(nil), mounts...)
		ids := map[string]uint64{}
		for name, id := range selected {
			ids[name] = id
		}
		if err := validateRuntimeMounts(launch, mutate(copy, ids), ids); err == nil {
			t.Fatal("unproved or writable infrastructure mount accepted")
		}
	}
	// A stacked mount at the same pathname is resolved by the opened FD's
	// kernel mount ID, not first/last textual order or path spelling alone.
	stacked := append([]runtimeMount{{99, ArtifactProjection, false}}, mounts...)
	if err := validateRuntimeMounts(launch, stacked, selected); err != nil {
		t.Fatal(err)
	}
	launch.WorkspaceReadOnly = false
	if err := validateRuntimeMounts(launch, mounts, selected); err == nil {
		t.Fatal("writable launch accepted a read-only project bind")
	}
	mounts[3].readOnly = false
	if err := validateRuntimeMounts(launch, mounts, selected); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeMountInfoDecodesKernelEscapesAndRejectsAmbiguity(t *testing.T) {
	valid := `12 1 0:2 / /workdir/a\040project ro,nosuid,nodev shared:1 - ext4 /dev/vdb rw` + "\n"
	mounts, err := parseRuntimeMounts([]byte(valid))
	if err != nil || len(mounts) != 1 || mounts[0].point != "/workdir/a project" || !mounts[0].readOnly {
		t.Fatal(mounts, err)
	}
	for _, raw := range []string{"", valid + valid, strings.Replace(valid, "ro,nosuid", "ro,rw,nosuid", 1), strings.Replace(valid, "ro,nosuid", "nosuid", 1), strings.Replace(valid, "\\040", "\\000", 1), strings.Replace(valid, " - ext4", " ext4", 1), strings.Replace(valid, "12 1", "0 1", 1), strings.Replace(valid, "/workdir/a", "relative/a", 1), valid + "\x00"} {
		if _, err := parseRuntimeMounts([]byte(raw)); err == nil {
			t.Fatal("ambiguous mountinfo accepted", raw)
		}
	}
}

func TestRuntimeDirectoryWalkRejectsSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "actual"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if file, err := openRuntimeDirectory(filepath.Join(root, "alias")); err == nil {
		_ = file.Close()
		t.Fatal("runtime directory followed a symlink")
	}
	file, err := openRuntimeDirectory(filepath.Join(root, "actual"))
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}
