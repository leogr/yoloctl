package agentbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/port"
)

func nativeForwardFixture(t *testing.T) (OpenShellRuntime, port.Spec, port.ForwardBinding) {
	t.Helper()
	current, details, raw, manifest := nativeRuntimeFixture(t)
	observed, err := validateOpenShellRuntime(current, "fixture", details, raw, manifest)
	if err != nil {
		t.Fatal(err)
	}
	spec := port.Spec{Agentbox: "fixture", HostPort: 8080, ContainerPort: 80}
	return observed, spec, observed.ForwardBinding(strings.Repeat("a", 32))
}

func TestNativeForwardBindingDoesNotAdoptAnotherContainerOrGeneration(t *testing.T) {
	observed, _, binding := nativeForwardFixture(t)
	if !observed.matchesForward(binding) {
		t.Fatal("matching live observations rejected")
	}
	for name, mutate := range map[string]func(*OpenShellRuntime){
		"stopped":    func(r *OpenShellRuntime) { r.Running = false },
		"pid":        func(r *OpenShellRuntime) { r.PID = 0 },
		"container":  func(r *OpenShellRuntime) { r.ContainerID = strings.Repeat("b", 64) },
		"generation": func(r *OpenShellRuntime) { r.Record.Generation = "g" + strings.Repeat("b", 48) },
		"record":     func(r *OpenShellRuntime) { r.RecordSHA256 = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := observed
			mutate(&changed)
			if changed.matchesForward(binding) {
				t.Fatal("foreign runtime adopted")
			}
		})
	}
}

func TestForwardStatusOnlyUpdatesItsOwnDescriptorAndRefusesForeignCleanup(t *testing.T) {
	_, spec, binding := nativeForwardFixture(t)
	directory := forwardStatusTestDirectory(t)
	uid, gid := os.Geteuid(), os.Getegid()
	first, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{111, "11100"}, forwardListenerIdentity{77, 333}, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{222, "22200"}, forwardListenerIdentity{77, 333}, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("new service process was not recorded")
	}
	foreign := spec
	foreign.ContainerPort++
	if _, err := publishForwardStatus(context.Background(), directory, foreign, binding, forwardProcessIdentity{333, "33300"}, forwardListenerIdentity{77, 333}, uid, gid); err == nil {
		t.Fatal("foreign descriptor overwrote owned status")
	}
	name := binding.Attempt + ".json"
	if err := removeForwardStatus(context.Background(), directory, name, first, uid, gid); err == nil {
		t.Fatal("old process removed replacement status")
	}
	if err := removeForwardStatus(context.Background(), directory, name, second, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
		t.Fatal("owned status retained", err)
	}
}

func TestForwardStatusRefusesMalformedDataAndLinksWithoutRepair(t *testing.T) {
	_, spec, binding := nativeForwardFixture(t)
	for _, kind := range []string{"duplicate", "unknown", "links", "symlink", "mode"} {
		t.Run(kind, func(t *testing.T) {
			directory := forwardStatusTestDirectory(t)
			uid, gid := os.Geteuid(), os.Getegid()
			raw, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{111, "11100"}, forwardListenerIdentity{77, 333}, uid, gid)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(directory, binding.Attempt+".json")
			switch kind {
			case "duplicate":
				raw = []byte(strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1))
				if err := os.WriteFile(name, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
				if err := os.WriteFile(name, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "links":
				if err := os.Link(name, name+".other"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".original", name); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(name, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{222, "22200"}, forwardListenerIdentity{77, 333}, uid, gid); err == nil {
				t.Fatal("bad status was silently repaired/adopted")
			}
		})
	}
}

func TestForwardStatusInterleavingCleanupCannotDeleteReplacement(t *testing.T) {
	_, spec, binding := nativeForwardFixture(t)
	directory := forwardStatusTestDirectory(t)
	uid, gid := os.Geteuid(), os.Getegid()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := publishForwardStatus(ctx, directory, spec, binding, forwardProcessIdentity{111, "11100"}, forwardListenerIdentity{77, 333}, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	name := binding.Attempt + ".json"
	compared := make(chan struct{})
	resume := make(chan struct{})
	cleanupDone := make(chan error, 1)
	old := forwardStatusStore{directory: directory, uid: uid, gid: gid}
	go func() {
		cleanupDone <- old.transaction(ctx, name, func() error {
			observed, _, err := readForwardStatusLocked(directory, name, uid, gid)
			if err != nil || !bytes.Equal(observed, first) {
				return errors.New("old status not recognized")
			}
			close(compared)
			select {
			case <-resume:
			case <-ctx.Done():
				return ctx.Err()
			}
			return old.removeLocked(name, first)
		})
	}()
	select {
	case <-compared:
	case <-ctx.Done():
		t.Fatal("cleanup did not recognize its status")
	}
	blocked := make(chan struct{})
	replacementDone := make(chan error, 1)
	replacement := forwardStatusStore{directory: directory, uid: uid, gid: gid, contended: func() { close(blocked) }}
	go func() {
		_, err := replacement.publish(ctx, spec, binding, forwardProcessIdentity{222, "22200"}, forwardListenerIdentity{77, 333})
		replacementDone <- err
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("replacement did not contend on the cleanup transaction")
	}
	close(resume)
	if err := <-cleanupDone; err != nil {
		t.Fatal(err)
	}
	if err := <-replacementDone; err != nil {
		t.Fatal(err)
	}
	_, final, err := readForwardStatus(ctx, directory, name, uid, gid)
	if err != nil || final.PID != 222 {
		t.Fatalf("replacement removed: %+v %v", final, err)
	}
	// Completed cleanup kept the stable lock inode for every later reader/worker.
	if info, err := os.Lstat(filepath.Join(directory, binding.Attempt+".lock")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stable lock missing: %v", err)
	}
}

func TestForwardStatusRefusesChangedLockWithoutRepair(t *testing.T) {
	_, spec, binding := nativeForwardFixture(t)
	for _, kind := range []string{"symlink", "links", "mode", "data"} {
		t.Run(kind, func(t *testing.T) {
			directory := forwardStatusTestDirectory(t)
			uid, gid := os.Geteuid(), os.Getegid()
			if _, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{111, "11100"}, forwardListenerIdentity{77, 333}, uid, gid); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(directory, binding.Attempt+".lock")
			switch kind {
			case "symlink":
				if err := os.Rename(lock, lock+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(lock+".old", lock); err != nil {
					t.Fatal(err)
				}
			case "links":
				if err := os.Link(lock, lock+".other"); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(lock, 0o644); err != nil {
					t.Fatal(err)
				}
			case "data":
				if err := os.WriteFile(lock, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{222, "22200"}, forwardListenerIdentity{77, 333}, uid, gid); err == nil {
				t.Fatal("changed lock accepted/repaired")
			}
		})
	}
}

func forwardStatusTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	// testing.TempDir's numbered child is not specified to be mode0700. Match
	// the production root-private status directory explicitly, without relaxing
	// the ownership/mode verifier.
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestForwardStatusRefusesNonPrivateDirectory(t *testing.T) {
	_, spec, binding := nativeForwardFixture(t)
	directory := forwardStatusTestDirectory(t)
	uid, gid := os.Geteuid(), os.Getegid()
	first, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{111, "11100"}, forwardListenerIdentity{77, 333}, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{222, "22200"}, forwardListenerIdentity{77, 333}, uid, gid); err == nil {
		t.Fatal("nonprivate directory accepted")
	}
	got, err := os.ReadFile(filepath.Join(directory, binding.Attempt+".json"))
	if err != nil || !bytes.Equal(first, got) {
		t.Fatal("rejected status was repaired or replaced", err)
	}
}

func TestForwardStatusBindsProcessStartIdentity(t *testing.T) {
	_, spec, binding := nativeForwardFixture(t)
	directory := forwardStatusTestDirectory(t)
	uid, gid := os.Geteuid(), os.Getegid()
	raw, err := publishForwardStatus(context.Background(), directory, spec, binding, forwardProcessIdentity{111, "222"}, forwardListenerIdentity{77, 333}, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{"0", "0222", "+222", "-1", "", "not-a-tick"} {
		changed := bytes.Replace(raw, []byte(`"start_ticks":"222"`), []byte(`"start_ticks":"`+start+`"`), 1)
		if _, err := decodeForwardStatus(changed); err == nil {
			t.Fatalf("invalid start identity %q accepted", start)
		}
	}
}
