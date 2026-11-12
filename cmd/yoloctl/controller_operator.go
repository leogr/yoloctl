package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"golang.org/x/sys/unix"
)

func operatorRuntime(ctx context.Context, p controller.Principal) (*container.Details, error) {
	if p.Admin() {
		return nil, nil
	}
	endpoint := operator.Endpoint{Box: p.Box, Incarnation: p.Incarnation}
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	space := spaceenv.Current().Space
	cfg, err := agentbox.Load(space, p.Box)
	if err != nil {
		return nil, errors.New("operator declaration is unavailable")
	}
	enabled, err := agentbox.OperatorEnabled(cfg)
	if err != nil || !enabled {
		return nil, errors.New("box operator grant has been revoked")
	}
	details, err := container.New(space).Inspect(ctx, p.Box)
	if err != nil {
		return nil, errors.New("operator container is unavailable")
	}
	if err := validateOperatorRuntime(space, p, details); err != nil {
		return nil, err
	}
	return details, nil
}

func validateOperatorRuntime(space string, p controller.Principal, d *container.Details) error {
	if d == nil || !d.Running || d.Name != p.Box ||
		d.Labels[agentbox.LabelAgentbox] != p.Box || d.Labels[agentbox.LabelSpace] != space ||
		d.Labels[operator.IncarnationLabel] != p.Incarnation {
		return errors.New("operator container is stopped or has been replaced")
	}
	e := operator.Endpoint{Box: p.Box, Incarnation: p.Incarnation}
	for _, mount := range d.Mounts {
		if mount.Destination == operator.Mount && mount.Source == e.Directory() && mount.ReadOnly {
			return nil
		}
	}
	return errors.New("operator endpoint does not belong to this container")
}

// Recreate volatile source directories before stopped containers can start.
// Containerd's immutable incarnation label survives an ordinary VM reboot.
// A box whose registration cannot be restored gets a notice and stays
// unserved; it never stops the controller from starting.
func restoreOperatorEndpoints(ctx context.Context) error {
	c := spaceenv.Current()
	boxes, err := container.New(c.Space).List(ctx, operator.IncarnationLabel)
	if err != nil {
		return err
	}
	client := container.New(c.Space)
	for _, box := range boxes {
		labels := parseLabels(box.Labels)
		endpoint := operator.Endpoint{Box: box.Name, Incarnation: labels[operator.IncarnationLabel]}
		err := errors.New("operator container identity is inconsistent")
		if labels[agentbox.LabelSpace] == c.Space && labels[agentbox.LabelAgentbox] == box.Name {
			err = restoreOperatorEndpoint(ctx, c.Space, endpoint, client.Inspect,
				func(e operator.Endpoint) error { return e.Prepare() }, operator.RestoreNative)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			logging.L().Warn("Operator endpoint was not restored; in-box yoloctl is unavailable until the box is recreated", "actor", logging.Controller, "box", box.Name, "err", err)
		}
	}
	return nil
}

// restoreOperatorEndpoint restores one box's registration: a native box's
// native identity (its incarnation and qualification labels), as the native
// create published it, or the existing legacy branch for any other box.
func restoreOperatorEndpoint(ctx context.Context, space string, endpoint operator.Endpoint, inspect func(context.Context, string) (*container.Details, error), prepare func(operator.Endpoint) error, restoreNative func(context.Context, operator.RegistrationIdentity) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := endpoint.Validate(); err != nil {
		return err
	}
	if inspect == nil || restoreNative == nil {
		return errors.New("operator restoration lacks actual runtime/registration callbacks")
	}
	d, err := inspect(ctx, endpoint.Box)
	if err != nil {
		return err
	}
	if d == nil || !nativeOperatorRuntimeSignals(d) {
		return restoreLegacyOperatorEndpoint(ctx, space, endpoint, func(context.Context, string) (*container.Details, error) { return d, nil }, prepare)
	}
	if err := checkRestoredOperatorRuntime(d, space, endpoint); err != nil {
		return err
	}
	identity := operator.RegistrationIdentity{Endpoint: endpoint, Mode: operator.RegistrationOpenShellNative,
		HelperProtocol: operator.NativeRegistrationHelperProtocol, Qualification: d.Labels[openshell.LabelQualification]}
	if err := identity.Validate(); err != nil {
		return err
	}
	return restoreNative(ctx, identity)
}

func checkRestoredOperatorRuntime(d *container.Details, space string, endpoint operator.Endpoint) error {
	if d == nil || len(d.ID) != 64 || d.Name != endpoint.Box || d.Labels[agentbox.LabelSpace] != space || d.Labels[agentbox.LabelAgentbox] != endpoint.Box || d.Labels[operator.IncarnationLabel] != endpoint.Incarnation {
		return errors.New("operator restoration actual runtime identity changed")
	}
	for _, c := range d.ID {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return errors.New("operator restoration lacks a complete canonical CID")
		}
	}
	return nil
}

// nativeOperatorRuntimeSignals matches every positive native signal checked by
// hasOpenShellRuntimeSignals.
func nativeOperatorRuntimeSignals(d *container.Details) bool {
	for _, key := range []string{openshell.LabelProtocol, openshell.LabelQualification, openshell.LabelHelperSHA256, openshell.LabelIncarnation, openshell.LabelControlRoot} {
		if _, present := d.Labels[key]; present {
			return true
		}
	}
	for _, arg := range d.Command {
		if arg == openshell.HelperBinary || arg == openshell.HelperCommand {
			return true
		}
	}
	for _, mount := range d.Mounts {
		if mount.Destination == openshell.NativeProjection || mount.Destination == openshell.ArtifactProjection || mount.Destination == openshell.HelperDirectory {
			return true
		}
	}
	return false
}

// This is the existing legacy restoration branch, not a native classifier or
// native restoration implementation (restoreOperatorEndpoint routes native
// boxes away first). Native and ambiguous evidence must never be rewritten to
// legacy identity bytes after a reboot erased the directory.
func restoreLegacyOperatorEndpoint(ctx context.Context, space string, endpoint operator.Endpoint, inspect func(context.Context, string) (*container.Details, error), prepare func(operator.Endpoint) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := endpoint.Validate(); err != nil {
		return err
	}
	if inspect == nil || prepare == nil {
		return errors.New("operator restoration lacks actual runtime/registration callbacks")
	}
	d, err := inspect(ctx, endpoint.Box)
	if err != nil {
		return err
	}
	if err := checkRestoredOperatorRuntime(d, space, endpoint); err != nil {
		return err
	}
	// Partial/conflicting native signals refuse too. Their absence alone is not
	// used here as missing-runtime/consumer/history evidence: an actual existing
	// legacy container and its original supported routing remain required.
	if nativeOperatorRuntimeSignals(d) {
		return operator.ErrNativeRegistration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return prepare(endpoint)
}

// A directory bind lets fresh helper invocations see an atomic binary upgrade.
// Never bind an individual executable inode into a container.
func installOperatorHelper() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	for _, path := range []string{"/opt/yoloctl", operator.HelperMount, operator.HelperMount + "/skill"} {
		if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 0 || st.Mode&0o022 != 0 {
			return fmt.Errorf("unsafe operator helper directory: %s", path)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
	}
	input, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.CreateTemp(operator.HelperMount, ".yoloctl-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(output.Name()) }()
	defer func() { _ = output.Close() }()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Chmod(0o755); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(output.Name(), filepath.Join(operator.HelperMount, "yoloctl")); err != nil {
		return err
	}
	if err := atomicfile.SyncDir(operator.HelperMount); err != nil {
		return err
	}
	skill := filepath.Join(operator.HelperMount, "skill")
	if err := os.Mkdir(skill, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if st, err := os.Lstat(skill); err != nil || !st.IsDir() {
		return errors.New("unsafe operator skill directory")
	}
	if err := os.Chmod(skill, 0o755); err != nil {
		return err
	}
	return safepath.WriteFile(skill, "SKILL.md", []byte(assets.BoxOperatorSkill), 0o644)
}

// Only root infrastructure can publish registrations. The socket alone never
// authorizes a caller: ServeConn rechecks the live grant and incarnation.
func serveOperatorEndpoints(ctx context.Context, server *controller.Server) error {
	ctx, cancel := context.WithCancel(ctx)
	listeners := map[string]net.Listener{}
	native := map[string]bool{} // served, or refused for good, by their native listener
	held := map[string]bool{}   // refused while an unresolved receipt holds the box; retried
	var clients sync.WaitGroup
	defer func() {
		cancel()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		// Native listeners outlive the scan context; retire them on stop too.
		stop, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
		if err := server.CloseNativeOperators(stop); err != nil {
			logging.L().Warn("Native operator endpoints did not close cleanly", "actor", logging.Controller, "err", err)
		}
		cancelStop()
		clients.Wait()
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, err := os.ReadDir(operator.Root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		present := make(map[string]bool, len(entries))
		for _, entry := range entries {
			present[entry.Name()] = true
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return errors.New("unsafe operator registration")
			}
			if _, exists := listeners[entry.Name()]; exists || native[entry.Name()] {
				continue
			}
			raw, err := safepath.ReadFile(operator.Root, filepath.Join(entry.Name(), "identity.json"), 4096)
			// A provisioner may still be publishing this directory.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			identity, err := scannedOperatorRegistration(entry.Name(), raw)
			if err != nil {
				return err
			}
			endpoint := identity.Endpoint
			// Prepare publishes identity before routing metadata. Wait for both;
			// never recreate a registration concurrently retired by lifecycle.
			if _, err := safepath.ReadFile(endpoint.Directory(), "client.json", 4096); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			if identity.Mode == operator.RegistrationOpenShellNative {
				// The native listener lives on its claim until the box's removal
				// retires it. A refusal (a receipt holding the box, for example)
				// leaves this registration unserved, not the controller stopped.
				if err := server.ServeNativeOperator(ctx, identity); errors.Is(err, controller.ErrNativeOperatorPublicationHeld) {
					// Retried on every pass, so serving resumes once the host
					// acknowledges the outcome; reported once.
					if !held[entry.Name()] {
						held[entry.Name()] = true
						logging.L().Warn("Native operator endpoint not served yet", "actor", logging.Controller, "box", endpoint.Box, "err", err)
					}
					continue
				} else if err != nil {
					logging.L().Warn("Native operator endpoint not served", "actor", logging.Controller, "box", endpoint.Box, "err", err)
				}
				native[entry.Name()] = true
				delete(held, entry.Name())
				continue
			}
			listener, err := server.ListenLegacyOperator(ctx, endpoint, func() (net.Listener, error) {
				return controller.Listen(filepath.Join(endpoint.Directory(), "control.sock"), 0o666)
			})
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			listeners[entry.Name()] = listener
			principal := controller.Principal{Box: endpoint.Box, Incarnation: endpoint.Incarnation}
			clients.Add(1)
			go func() {
				defer clients.Done()
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					clients.Add(1)
					go func() { defer clients.Done(); server.ServeConn(ctx, conn, principal) }()
				}
			}()
		}
		for incarnation, listener := range listeners {
			if !present[incarnation] {
				_ = listener.Close()
				delete(listeners, incarnation)
			}
		}
		for incarnation := range native {
			if !present[incarnation] {
				delete(native, incarnation)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// scannedOperatorRegistration decodes one registration's identity for the
// scanner. A native registration is never read as legacy authority or served
// by the generic legacy listener: the caller serves it through its native one.
func scannedOperatorRegistration(name string, raw []byte) (operator.RegistrationIdentity, error) {
	identity, err := operator.DecodeRegistrationIdentity(raw)
	if err != nil {
		return operator.RegistrationIdentity{}, err
	}
	if identity.Incarnation != name {
		return operator.RegistrationIdentity{}, errors.New("operator registration identity mismatch")
	}
	switch identity.Mode {
	case operator.RegistrationOpenShellNative, operator.RegistrationLegacy:
		return identity, nil
	}
	return operator.RegistrationIdentity{}, errors.New("unsupported operator registration mode")
}

func checkOperatorPrincipal(ctx context.Context, p controller.Principal) error {
	_, err := operatorRuntime(ctx, p)
	if err != nil {
		return fmt.Errorf("box operator denied: %w", err)
	}
	return nil
}

// revalidateOperatorFn is replaced by tests.
var revalidateOperatorFn = revalidateOperator

// revalidateOperator rechecks an operator's live grant and incarnation once
// the native mutation lock is held, before any effect. Admission checked it
// before the lock, and the listener's periodic recheck can lag a revocation or
// replacement that happened while the request waited.
func revalidateOperator(ctx context.Context) error {
	p := currentControllerPrincipal(ctx)
	if p.Admin() {
		return nil
	}
	return checkOperatorPrincipal(ctx, p)
}

const operatorAuthorityNotice = "full box authority in this space: other existing accounts and their credentials, granted host projects, stronger capabilities, delegation, workload destruction, and managed chat administration. The managed chat server's human identity and interactive access remain host-only. Revocation does not undo previously created boxes."

func warnBoxOperator(cfg *agentbox.Config) {
	if enabled, err := agentbox.OperatorEnabled(cfg); err == nil && enabled {
		logging.L().LogAttrs(context.Background(), slog.LevelWarn, fmt.Sprintf("box %s has operator authority", cfg.Name), prettylog.Actor(logging.Box), slog.String(prettylog.KeyErr, operatorAuthorityNotice))
	}
}

// mapOperatorPath uses actual bind destinations, not a claimed box name or
// project directory. Canonicalization and live-grant checks still run in the
// shared box service after translation.
func mapOperatorPath(raw, cwd string, mounts []container.DetailMount, grants *spaceconfig.Config) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, "~") {
		return "", errors.New("expand ~ in the calling shell before passing an operator path")
	}
	path := raw
	if !filepath.IsAbs(path) {
		if !filepath.IsAbs(cwd) {
			return "", errors.New("relative operator path requires an absolute working directory")
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	var best *container.DetailMount
	for i := range mounts {
		m := &mounts[i]
		if _, granted := grants.GrantFor(m.Source); !granted {
			continue
		}
		if path != m.Destination && !strings.HasPrefix(path, m.Destination+"/") {
			continue
		}
		if best == nil || len(m.Destination) > len(best.Destination) {
			best = m
		}
	}
	if best != nil {
		rel, err := filepath.Rel(best.Destination, path)
		if err != nil {
			return "", err
		}
		return filepath.Join(best.Source, rel), nil
	}
	if filepath.IsAbs(raw) {
		if _, granted := grants.GrantFor(path); granted {
			return path, nil
		}
	}
	return "", fmt.Errorf("operator path %q is not mapped to a granted host directory (ephemeral and account paths cannot be shared)", raw)
}

func translateOperatorRequest(ctx context.Context, p controller.Principal, req *controller.Request) error {
	if p.Admin() {
		return nil
	}
	details, err := operatorRuntime(ctx, p)
	if err != nil {
		return err
	}
	grants, err := spaceconfig.Load(req.Space)
	if err != nil {
		return err
	}
	for i, raw := range req.Flags["workdir"] {
		path, err := mapOperatorPath(raw, req.Cwd, details.Mounts, grants)
		if err != nil {
			return err
		}
		req.Flags["workdir"][i] = path
	}
	for i, raw := range req.Flags["mount"] {
		if raw == "" {
			continue
		}
		source, rest, ok := strings.Cut(raw, ":")
		if !ok {
			return errors.New("mount requires source:target[:ro]")
		}
		path, err := mapOperatorPath(source, req.Cwd, details.Mounts, grants)
		if err != nil {
			return err
		}
		req.Flags["mount"][i] = path + ":" + rest
	}
	return nil
}
