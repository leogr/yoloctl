package agentbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// createNativeContainer supplies real SDK/engine callbacks to the reviewed
// transaction. It is private until final release/default/lifecycle integration;
// it must be called under existing native space/entity/port admission. image
// is the immutable runtime selected by main's existing image preparation path.
// This function neither builds images nor ends a holder/session. Replacement
// callers must qualify artifacts BEFORE destructive teardown, separately.
func (a LiveApplier) createNativeContainer(ctx context.Context, c *Config, image string, policy []byte, release openshell.QualifiedRelease, rego []byte) error {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || a.Client == nil || a.VerifyStorage == nil || c == nil || c.Privileged {
		return errors.New("native creation requires admitted ordinary VM-root infrastructure")
	}
	if err := spaceenv.RequireSpace(a.Client.Env()); err != nil {
		return err
	}
	// Chat/observation option assembly below can prepare native directories.
	// Refuse image wrappers before those effects as well as before the later
	// artifact/reservation/create transaction. Replacement callers also run
	// this image preflight before ending an existing holder/session.
	if err := a.VerifyStorage(ctx); err != nil {
		return err
	}
	if err := a.PreflightNativeImage(ctx, image); err != nil {
		return err
	}
	current := spaceenv.Current()
	options, plan, operatorEnabled, err := planNativeCreation(ctx, c, image, *current, release, policy)
	if err != nil {
		return err
	}
	if err := addChatRunOptions(c, &options); err != nil {
		return err
	}
	if err := plan.retainChatEnvironment(c); err != nil {
		return err
	}
	if err := addObservationRunOptions(c, &options); err != nil {
		return err
	}
	var reservation *openshell.NativeReservation
	var activation *openshell.NativeActivation
	var socketDirectory *os.File
	defer func() {
		if activation != nil {
			_ = activation.Close()
		}
		if reservation != nil {
			_ = reservation.Close()
		}
		if socketDirectory != nil {
			_ = socketDirectory.Close()
		}
	}()
	var endpoint operator.Endpoint
	var id string
	var input openshell.BootstrapInput
	var prepared openshell.PreparedRuntime
	var target openshell.ActivationTarget
	var forwards []ownedNativeForward
	inspect := func(check context.Context) (*container.Details, error) {
		if err := a.VerifyStorage(check); err != nil {
			return nil, err
		}
		if reservation == nil {
			return nil, errors.New("native creation lacks its continuous reservation")
		}
		if err := reservation.Verify(); err != nil {
			return nil, err
		}
		d, err := a.Client.Inspect(check, id)
		if err != nil {
			return nil, err
		}
		if d.ID != id {
			return nil, errors.New("engine observation differs from the create acknowledgement")
		}
		if err := plan.validateContainer(d); err != nil {
			return nil, err
		}
		if operatorEnabled {
			if d.Labels[operator.IncarnationLabel] != endpoint.Incarnation || endpoint.Box != c.Name || endpoint.Incarnation == "" {
				return nil, errors.New("created operator projection differs from this call's endpoint")
			}
			matches := 0
			for _, mount := range d.Mounts {
				if mount.Destination == operator.Mount {
					matches++
					if mount.Source != endpoint.Directory() || !mount.ReadOnly {
						return nil, errors.New("created operator projection bind changed")
					}
				}
			}
			if matches != 1 {
				return nil, errors.New("created container lacks its single operator projection")
			}
		} else if d.Labels[operator.IncarnationLabel] != "" {
			return nil, errors.New("created container unexpectedly has an operator incarnation")
		}

		return d, nil
	}
	running := func(check context.Context) (openshell.ActivationTarget, error) {
		d, err := inspect(check)
		if err != nil {
			return openshell.ActivationTarget{}, err
		}
		return selectNativeStartedTargetWithDiagnostic(check, d, id, target, a.Client.StartupFailureDiagnostics)
	}
	actions := nativeCreateActions{
		preflight: func(check context.Context) error {
			if err := a.VerifyStorage(check); err != nil {
				return err
			}
			if err := a.PreflightNativeImage(check, image); err != nil {
				return err
			}
			if err := a.Client.RequireWorkloadPool(check); err != nil {
				return err
			}
			if _, err := openshell.InstallNativeRelease(check, release, rego); err != nil {
				return err
			}
			if _, err := openshell.InstallNativeHelper(check, release); err != nil {
				return err
			}
			known, err := port.Inventory(check, a.Client, "")
			if err != nil {
				return fmt.Errorf("matching-init forward inventory: %w", err)
			}
			for _, service := range known {
				if service.Spec.Agentbox == c.Name {
					return errors.New("new container still has retained forwarding services")
				}
			}
			for _, pf := range c.Ports {
				if err := noNativeForwardConflict(known, portSpec(c, pf)); err != nil {
					return err
				}
			}
			if len(c.Ports) > 0 {
				return preflightForwardCLI(check)
			}
			return nil
		},
		reserve: func(check context.Context) error {
			var err error
			reservation, err = openshell.ReserveOwnedNativeContainer(check, plan.binding, release, plan.launch.HelperSHA256)
			if err != nil {
				return err
			}
			if reservation.Directory() != plan.controlRoot {
				return errors.New("reserved native bind differs from the selected plan")
			}
			// The directory is root infrastructure, not an account/workdir. Preserve
			// existing safe modes; refuse a retained endpoint rather than unlink it.
			if err := unix.Mkdir(sandbox.SocketDirHost(c.Name), 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				return err
			}
			socketDirectory, err = openForwardSocketDirectory(c.Name)
			if err != nil {
				return err
			}
			var present unix.Stat_t
			if err := unix.Fstatat(int(socketDirectory.Fd()), "ssh.sock", &present, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
				return errors.New("new container requires an independently retired public SSH endpoint")
			}
			if err := stagePolicyOnVM(check, a.Client, c, policy); err != nil {
				return err
			}
			if operatorEnabled {
				// The registration carries this creation's planned incarnation and
				// release, so the container label and its mount agree with it.
				identity := operator.RegistrationIdentity{
					Endpoint:       operator.Endpoint{Box: c.Name, Incarnation: plan.binding.Incarnation},
					Mode:           operator.RegistrationOpenShellNative,
					HelperProtocol: operator.NativeRegistrationHelperProtocol,
					Qualification:  release.Identity(),
				}
				endpoint, err = operator.ProvisionNative(check, identity, a.VerifyStorage)
				if err != nil {
					return err
				}
				options.Labels[operator.IncarnationLabel] = endpoint.Incarnation
				options.Mounts = append(options.Mounts,
					container.Mount{Source: endpoint.Directory(), Destination: operator.Mount, ReadOnly: true},
					container.Mount{Source: operator.HelperMount, Destination: operator.HelperMount, ReadOnly: true},
					container.Mount{Source: operator.HelperMount + "/skill", Destination: operator.CodexSkill, ReadOnly: true},
					container.Mount{Source: operator.HelperMount + "/skill", Destination: operator.ClaudeSkill, ReadOnly: true},
					container.Mount{Source: operator.HelperMount + "/skill", Destination: operator.GeminiSkill, ReadOnly: true})
			}
			return nil
		},
		create: func(create context.Context) (string, error) {
			if err := a.VerifyStorage(create); err != nil {
				return "", err
			}
			var err error
			id, err = a.Client.Create(create, options)
			return id, err
		},
		inspect: func(check context.Context) (string, error) {
			d, err := inspect(check)
			if err != nil {
				return "", err
			}
			input, err = plan.bootstrapForCreated(d)
			return d.ID, err
		},
		prepare: func(prepare context.Context) error {
			d, err := inspect(prepare)
			if err != nil {
				return err
			}
			input, err = plan.bootstrapForCreated(d)
			if err != nil {
				return err
			}
			prepared, err = openshell.PrepareNative(prepare, input, release, plan.launch.HelperSHA256, plan.unconfined, policy, rego)
			input.Environment = nil // release this decoded environment after preparation
			return err
		},
		start: func(start context.Context) error {
			d, err := inspect(start)
			if err != nil {
				return err
			}
			if _, err := plan.bootstrapForCreated(d); err != nil {
				return err
			}
			return a.Client.Start(start, id)
		},
		activate: func(activate context.Context) error {
			var err error
			activation, err = openshell.ActivateNativeOwned(activate, prepared, func(check context.Context) (openshell.ActivationTarget, error) {
				observed, err := running(check)
				if err == nil && target.ContainerID == "" {
					target = observed
				}
				return observed, err
			})
			return err
		},
		ready: func(ready context.Context) error {
			raw, _ := json.Marshal(prepared.Record)
			return waitNativeCreatedSSH(ready, socketDirectory, prepared.Record.SSHHostPublicKey, func(check context.Context) error {
				if _, err := running(check); err != nil {
					return err
				}
				observed, err := inspectOpenShellRuntime(check, a.Client, c.Name, id)
				if err != nil {
					return err
				}
				actual, _ := json.Marshal(observed.Record)
				if observed.ContainerID != id || observed.ControlRoot != plan.controlRoot || observed.Launch != plan.launch || !bytes.Equal(actual, raw) {
					return errors.New("native startup generation changed before readiness")
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
					return errors.New("native public SSH directory changed")
				}
				return nil
			})
		},
		forwards: func(forward context.Context) error {
			for _, pf := range c.Ports {
				owned, err := startOwnedNativeOpenShellForward(forward, a.Client, portSpec(c, pf), a.VerifyStorage)
				if err != nil {
					return err
				}
				forwards = append(forwards, owned)
			}
			return nil
		},
		cleanup: func(cleanup context.Context, progress nativeCreateProgress) error {
			if err := a.VerifyStorage(cleanup); err != nil {
				return err
			}
			if reservation == nil {
				return errors.New("startup cleanup lacks its fresh reservation")
			}
			if err := reservation.Verify(); err != nil {
				return err
			}
			if progress.Created {
				if progress.ContainerID != id {
					return errors.New("cleanup CID differs from this call's acknowledgement")
				}
				verifyRemaining := func(check context.Context) error {
					if err := a.VerifyStorage(check); err != nil {
						return err
					}
					if err := reservation.Verify(); err != nil {
						return err
					}
					if progress.Prepared {
						if err := openshell.VerifyPreparedNativeRecord(check, prepared); err != nil {
							return err
						}
					}
					if socketDirectory != nil {
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
							return errors.New("native cleanup public socket directory changed")
						}
					}
					return nil
				}
				checks := nativeCreatedCleanupChecks{inspect: inspect, remaining: verifyRemaining, id: id, target: target, started: progress.Started}
				if err := checks.owned(cleanup); err != nil {
					return err
				}
				for i := len(forwards) - 1; i >= 0; i-- {
					owned := forwards[i]
					if err := cleanupNativeForwardAttempt(cleanup, a.Client, owned.selected, owned.spec, owned.binding, a.VerifyStorage); err != nil {
						return err
					}
				}
				if err := cleanupNativeCreatedContainerID(cleanup, checks, activation,
					func(stop context.Context) error { return a.Client.Stop(stop, id) },
					func(remove context.Context, selected string) error {
						return a.Client.RemoveContainerID(remove, selected)
					},
				); err != nil {
					return err
				}

			}
			// No name-based operator removal and no deletion before CID removal ACK.
			if endpoint.Incarnation != "" {
				if err := operator.RemoveEndpoint(endpoint); err != nil {
					return err
				}
			}
			if socketDirectory != nil {
				if err := removeOwnedNativeSSHSocket(socketDirectory); err != nil {
					return err
				}
			}
			return reservation.RemoveAfterContainerRemoval(cleanup)
		},
	}
	return completeNativeContainerCreation(ctx, actions)
}

func selectNativeStartedTarget(d *container.Details, id string, selected openshell.ActivationTarget) (openshell.ActivationTarget, error) {
	if d == nil || d.ID != id || !d.Running || d.PID <= 1 {
		return openshell.ActivationTarget{}, errors.New("native startup requires its acknowledged running container")
	}
	started, err := time.Parse(time.RFC3339Nano, d.StartedAt)
	if err != nil || started.IsZero() {
		return openshell.ActivationTarget{}, errors.New("native startup lacks its real start identity")
	}
	observed := openshell.ActivationTarget{ContainerID: id, PID: d.PID, StartedAt: started}
	if selected.ContainerID != "" && (observed.ContainerID != selected.ContainerID || observed.PID != selected.PID || !observed.StartedAt.Equal(selected.StartedAt)) {
		return openshell.ActivationTarget{}, errors.New("native startup process was replaced or restarted")
	}
	return observed, nil
}

func selectNativeStartedTargetWithDiagnostic(ctx context.Context, d *container.Details, id string, selected openshell.ActivationTarget, diagnostic func(context.Context, string) string) (openshell.ActivationTarget, error) {
	observed, err := selectNativeStartedTarget(d, id, selected)
	if err == nil || diagnostic == nil {
		return observed, err
	}
	// Keep this failure's owned CID alive until the bounded read completes.
	// The transaction then performs its original cleanup and outcome handling.
	return openshell.ActivationTarget{}, fmt.Errorf("%w; %s", err, diagnostic(ctx, id))
}

var errNativeStartupHostKey = errors.New("native startup SSH host key differs from its prepared generation")

// This read-only handshake opens no SSH session or forwarding channel. The
// fixed helper publishes this relay only after upstream attach/confirm/ready.
func waitNativeCreatedSSH(ctx context.Context, directory *os.File, authorizedKey string, verify func(context.Context) error) error {
	if directory == nil || verify == nil {
		return errors.New("native readiness requires its retained socket directory and verifier")
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 {
		return errors.New("native readiness lacks its prepared SSH key")
	}
	ctx, cancel := context.WithTimeout(ctx, SandboxSocketTimeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verify(ctx); err != nil {
			return err
		}
		if err := verifyForwardSocket(directory); err == nil {
			ready, err := probeNativeCreatedSSH(ctx, directory, key)
			if err != nil {
				return err
			}
			if ready {
				if err := verifyForwardSocket(directory); err != nil {
					return err
				}
				return verify(ctx)
			}
		} else if !errors.Is(err, unix.ENOENT) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func probeNativeCreatedSSH(ctx context.Context, directory *os.File, key ssh.PublicKey) (bool, error) {
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	path := fmt.Sprintf("/proc/self/fd/%d/ssh.sock", directory.Fd())
	conn, err := (&net.Dialer{}).DialContext(probe, "unix", path)
	if err != nil {
		return false, nil
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := probe.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return false, err
	}
	stop := context.AfterFunc(probe, func() { _ = conn.Close() })
	defer stop()
	transport, _, _, err := ssh.NewClientConn(conn, path, &ssh.ClientConfig{User: "sandbox", HostKeyCallback: func(_ string, _ net.Addr, actual ssh.PublicKey) error {
		if !bytes.Equal(actual.Marshal(), key.Marshal()) {
			return errNativeStartupHostKey
		}
		return nil
	}})
	if err != nil {
		if errors.Is(err, errNativeStartupHostKey) {
			return false, err
		}
		return false, nil
	}
	defer func() { _ = transport.Close() }()
	if err := probe.Err(); err != nil {
		return false, nil
	}
	return true, nil
}
func removeOwnedNativeSSHSocket(directory *os.File) error {
	if err := verifyForwardSocket(directory); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if err := unix.Unlinkat(int(directory.Fd()), "ssh.sock", 0); err != nil {
		return err
	}
	return directory.Sync()
}
