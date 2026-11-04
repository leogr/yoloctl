package agentbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// startNativeStoppedGeneration is private until complete lifecycle review.
// Caller holds native space/entity/port admission, has ended its holder and
// withdrawn retained forwards AFTER separate prerequisite qualification. This
// method repeats qualification, preserves CID/private files, and starts only
// the fresh generation it prepares itself. Policy rollback is separate.
func (a LiveApplier) startNativeStoppedGeneration(ctx context.Context, c *Config, policy []byte) error {
	selected, checkpoint, err := a.snapshotNativeGeneration(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = checkpoint.Close() }()
	return a.startNativeStoppedGenerationWithCheckpoint(ctx, c, policy, checkpoint, selected.ContainerID)
}

// The caller retains ONE original checkpoint across an initial policy launch
// and any authorized single rollback. This method never closes/zeros it, never
// replaces its original policy/environment and never selects a different CID.
func (a LiveApplier) startNativeStoppedGenerationWithCheckpoint(ctx context.Context, c *Config, policy []byte, checkpoint *openshell.NativeGeneration, id string) error {
	if checkpoint == nil || !nativeLowerHex(id, 32) {
		return errors.New("native generation Start requires its original checkpoint and fixed CID")
	}
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.Client == nil || a.VerifyStorage == nil || c == nil || c.Privileged {
		return errors.New("native generation Start requires admitted ordinary VM-root infrastructure")
	}
	copy := *c
	copy.Ports = append([]Port(nil), c.Ports...)
	c = &copy
	policy = bytes.Clone(policy)
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return err
	}
	if err := a.VerifyStorage(ctx); err != nil {
		return err
	}
	selected, err := inspectOpenShellRuntime(ctx, a.Client, c.Name, id)
	if err != nil {
		return err
	}
	if selected.Running || selected.PID != 0 {
		return errors.New("stop the native container before fresh generation Start")
	}
	if err := checkpoint.ValidatePolicy(policy); err != nil {
		return err
	}
	var socketDirectory *os.File
	var started *nativeStartedGeneration
	var forwards []ownedNativeForward
	defer func() {
		if started != nil && started.preparation != nil {
			_ = started.preparation.Close()
		}
		if socketDirectory != nil {
			_ = socketDirectory.Close()
		}
	}() // Descriptor release never stops a successful runtime.

	read := func(check context.Context) (OpenShellRuntime, *container.Details, error) {
		if err := a.VerifyStorage(check); err != nil {
			return OpenShellRuntime{}, nil, err
		}
		current, err := inspectOpenShellRuntime(check, a.Client, c.Name, id)
		if err != nil {
			return OpenShellRuntime{}, nil, err
		}
		d, err := a.Client.Inspect(check, id)
		return current, d, err
	}
	verifyOldInactive := func(check context.Context) (openshell.GenerationTarget, error) {
		current, d, err := read(check)
		if err != nil {
			return openshell.GenerationTarget{}, err
		}
		if err := validateNativeStopObservation(selected, current, d, true); err != nil {
			return openshell.GenerationTarget{}, err
		}
		return nativeInactiveGenerationTarget(d, id)
	}
	verifyPrepared := func(check context.Context, g *nativeStartedGeneration, inactive bool) (*container.Details, error) {
		current, d, err := read(check)
		if err != nil {
			return nil, err
		}
		if err := g.verifySelected(check, current); err != nil {
			return nil, err
		}
		if d == nil || d.ID != id || d.AmbiguousTaskStateObserved || current.Running != d.Running || current.PID != d.PID {
			return nil, errors.New("new generation current task observation changed/is ambiguous")
		}
		if inactive {
			if _, err := nativeInactiveGenerationTarget(d, id); err != nil {
				return nil, err
			}
			oldTarget := openshell.ActivationTarget{ContainerID: id, PID: selected.PID, StartedAt: selected.StartedAt}
			if err := validateNativeCleanupState(d, id, oldTarget, true); err != nil {
				return nil, err
			}
			var observed time.Time
			if d.StartedAt != "" {
				observed, err = time.Parse(time.RFC3339Nano, d.StartedAt)
				if err != nil {
					return nil, err
				}
			}
			if !observed.Equal(current.StartedAt) {
				return nil, errors.New("inactive generation history changed between reads")
			}
		} else {
			target, err := selectNativeStartedTarget(d, id, g.target)
			if err != nil {
				return nil, err
			}
			if !current.StartedAt.Equal(target.StartedAt) {
				return nil, errors.New("new generation start observation changed between reads")
			}
		}
		return d, nil
	}
	socketUnchanged := func() error {
		if socketDirectory == nil {
			return errors.New("new generation lacks its retained SSH directory")
		}
		current, err := openForwardSocketDirectory(c.Name)
		if err != nil {
			return err
		}
		defer func() { _ = current.Close() }()
		before, err := socketDirectory.Stat()
		if err != nil {
			return err
		}
		after, err := current.Stat()
		if err != nil || !os.SameFile(before, after) {
			return errors.Join(err, errors.New("new generation SSH directory was replaced"))
		}
		return nil
	}
	actions := nativeGenerationStartActions{
		preflight: func(check context.Context) error {
			if _, err := verifyOldInactive(check); err != nil {
				return err
			}
			if err := a.Client.RequireWorkloadPool(check); err != nil {
				return err
			}
			known, err := port.Inventory(check, a.Client, "")
			if err != nil {
				return err
			}
			for _, service := range known {
				if service.Spec.Agentbox == c.Name {
					return errors.New("withdraw retained native forwarding services before Start")
				}
			}
			published := map[int]bool{}
			for _, pf := range c.Ports {
				spec := portSpec(c, pf)
				if _, err := port.OpenShellUnitName(spec); err != nil {
					return err
				}
				if published[spec.HostPort] {
					return errors.New("duplicate native published port before Start")
				}
				published[spec.HostPort] = true
				if err := noNativeForwardConflict(known, spec); err != nil {
					return err
				}
			}
			if len(c.Ports) > 0 {
				if err := preflightForwardCLI(check); err != nil {
					return err
				}
			}
			if err := checkpoint.QualifyNativeArtifacts(check); err != nil {
				return err
			}
			_, err = verifyOldInactive(check)
			return err
		},
		stopPrevious: func(stop context.Context) error {
			if _, err := verifyOldInactive(stop); err != nil {
				return err
			}
			return a.stopNativeRuntime(stop, c.Name, id)
		},
		prepare: func(prepare context.Context) (*nativeStartedGeneration, error) {
			preparation, err := checkpoint.PrepareNextOwned(prepare, policy, verifyOldInactive)
			if err != nil {
				return nil, err
			}
			started = &nativeStartedGeneration{cid: id, prepared: preparation.Prepared(), preparation: preparation}
			return started, nil
		},
		beforeStart: func(check context.Context, g *nativeStartedGeneration) error {
			if _, err := verifyPrepared(check, g, true); err != nil {
				return err
			}
			var err error
			socketDirectory, err = ensureNativeGenerationSocketDirectory(c.Name)
			if err != nil {
				return err
			}
			if err := requireEmptyNativeGenerationSocket(socketDirectory); err != nil {
				return err
			}
			return socketUnchanged()
		},
		start: func(start context.Context, g *nativeStartedGeneration) error { return a.Client.Start(start, g.cid) },
		activate: func(activate context.Context, g *nativeStartedGeneration) error {
			var err error
			g.activation, err = g.preparation.Activate(activate, func(check context.Context) (openshell.ActivationTarget, error) {
				d, err := verifyPrepared(check, g, false)
				if err != nil {
					return openshell.ActivationTarget{}, err
				}
				target, err := selectNativeStartedTarget(d, g.cid, g.target)
				if err == nil && g.target.ContainerID == "" {
					g.target = target
				}
				return target, err
			})
			return err
		},
		ready: func(ready context.Context, g *nativeStartedGeneration) error {
			return waitNativeCreatedSSH(ready, socketDirectory, g.prepared.Record.SSHHostPublicKey, func(check context.Context) error {
				if _, err := verifyPrepared(check, g, false); err != nil {
					return err
				}
				return socketUnchanged()
			})
		},
		forwards: func(forward context.Context, g *nativeStartedGeneration) error {
			if _, err := verifyPrepared(forward, g, false); err != nil {
				return err
			}
			for _, pf := range c.Ports {
				owned, err := startOwnedNativeOpenShellForward(forward, a.Client, portSpec(c, pf), a.VerifyStorage)
				if err != nil {
					return err
				}
				forwards = append(forwards, owned)
			}
			return nil
		},
		cleanup: func(cleanup context.Context, g *nativeStartedGeneration) error {
			if err := a.VerifyStorage(cleanup); err != nil {
				return err
			}
			for n := len(forwards) - 1; n >= 0; n-- {
				owned := forwards[n]
				if err := cleanupNativeForwardAttempt(cleanup, a.Client, owned.selected, owned.spec, owned.binding, a.VerifyStorage); err != nil {
					return err
				}
			}
			// Unknown Start is rejected by cleanupIdentity before any effect. A known
			// no-Start prefix also uses the original SDK token and fresh Stop ACK;
			// merely observing inactivity cannot leave an origin-less generation.
			return a.stopNativeStartedGeneration(cleanup, c.Name, g)
		},
	}
	return completeNativeGenerationStart(ctx, actions)
}

// This walks every fixed /run component through descriptors before creating
// the single validated box directory. Existing unsafe modes/links are refused,
// never repaired. All names/permissions remain native infrastructure only.
func ensureNativeGenerationSocketDirectory(name string) (*os.File, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("native SSH directory preparation requires admitted VM root")
	}
	if filepath.Base(name) != name || name == "" || name == "." || name == ".." {
		return nil, errors.New("invalid native SSH directory name")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(fd), "/")
	defer func() { _ = parent.Close() }()
	return ensureNativeGenerationSocketAt(parent, []string{"run", "yoloctl", name}, 0, 0)
}

// Owner parameters support ordinary owned-file project fixtures only. Public
// preparation always supplies root and the fixed native absolute anchor.
func ensureNativeGenerationSocketAt(anchor *os.File, components []string, uid, gid uint32) (*os.File, error) {
	if anchor == nil || len(components) == 0 {
		return nil, errors.New("native SSH preparation lacks a trusted parent")
	}
	for _, name := range components {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			return nil, errors.New("invalid native SSH component")
		}
	}
	current, err := unix.Openat(int(anchor.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if current >= 0 {
			_ = unix.Close(current)
		}
	}()
	for index, name := range components {
		// /run itself must already exist; only yoloctl and the final box are owned
		// initialization directories. Neither a host workdir nor account is used.
		next, err := unix.Openat(current, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && index > 0 {
			if err := unix.Mkdirat(current, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				return nil, err
			}
			next, err = unix.Openat(current, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err != nil {
			return nil, err
		}
		var stat unix.Stat_t
		if unix.Fstat(next, &stat) != nil || stat.Uid != uid || stat.Gid != gid || stat.Mode&0o7022 != 0 {
			_ = unix.Close(next)
			return nil, errors.New("native SSH component has untrusted ownership/mode")
		}
		_ = unix.Close(current)
		current = next
	}
	result := os.NewFile(uintptr(current), "native-generation-ssh-directory")
	current = -1
	return result, nil
}
func requireEmptyNativeGenerationSocket(directory *os.File) error {
	if directory == nil {
		return errors.New("native Start lacks its SSH directory")
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), "ssh.sock", &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return errors.Join(err, errors.New("native Start requires an independently retired SSH endpoint"))
	}
	return nil
}

// snapshotNativeGeneration only reads selected metadata/private material. Every
// mutation repeats storage/identity checks under the enclosing native admission.
func (a LiveApplier) snapshotNativeGeneration(ctx context.Context, c *Config) (OpenShellRuntime, *openshell.NativeGeneration, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.Client == nil || a.VerifyStorage == nil || c == nil || c.Privileged {
		return OpenShellRuntime{}, nil, errors.New("native generation snapshot requires admitted ordinary VM-root infrastructure")
	}
	if err := ctx.Err(); err != nil {
		return OpenShellRuntime{}, nil, err
	}
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return OpenShellRuntime{}, nil, err
	}
	if err := a.VerifyStorage(ctx); err != nil {
		return OpenShellRuntime{}, nil, err
	}
	selected, err := inspectOpenShellRuntime(ctx, a.Client, c.Name, c.Name)
	if err != nil {
		return OpenShellRuntime{}, nil, err
	}
	checkpoint, err := openshell.SnapshotNativeGeneration(ctx, selected.Record, selected.Release, selected.ContainerID)
	if err != nil {
		return OpenShellRuntime{}, nil, err
	}
	return selected, checkpoint, nil
}
