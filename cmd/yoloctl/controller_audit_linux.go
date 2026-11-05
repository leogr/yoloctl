package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/internal/vmservice"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

var auditAcquire = spacelock.Acquire
var auditRuntimeDir = layout.AuditRunDir
var auditKernelSupport = audit.ProbeSupport
var auditBackendSupport = vmservice.AuditSupported
var auditServiceState = vmservice.State
var auditServiceStart = vmservice.Start
var auditServiceStop = vmservice.StopAcknowledged

func nativeAuditDesired(cfg *spaceconfig.Config) audit.Desired {
	if cfg.Audit == nil {
		return audit.Desired{}
	}
	return audit.Desired(*cfg.Audit)
}

func nativeAuditStatus(ctx context.Context) (audit.Status, error) {
	var status audit.Status
	c := spaceenv.Current()
	if c == nil {
		return status, errors.New("audit requires the native controller")
	}
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil {
		return status, err
	}
	status.Desired, status.Architecture, status.Driver = nativeAuditDesired(cfg), runtime.GOARCH, "unloaded"
	root, err := layout.AuditDir()
	if err != nil {
		return status, err
	}
	summary, err := (audit.Store{Root: filepath.Join(root, "logs")}).Counters()
	if err != nil {
		return status, err
	}
	status.Drops, status.MetadataGaps = summary.Drops, summary.MetadataGaps
	if err := auditBackendSupport(ctx, container.New(c.Space)); err != nil {
		status.State, status.Error = "unsupported", audit.Clean(err.Error())
		return status, nil
	}
	if c.ServiceManager == "microvm" {
		if err := auditKernelSupport(); err != nil {
			status.State, status.Error = "unsupported", audit.Clean(err.Error())
			return status, nil
		}
	}
	readCtx, stopRead := context.WithTimeout(ctx, 5*time.Second)
	defer stopRead()
	state, err := auditServiceState(readCtx, container.New(c.Space), audit.ServiceName)
	status.State = state
	if err != nil {
		status.State, status.Error = "unavailable", "could not observe the audit service"
		return status, nil
	}
	var service audit.ServiceConfig
	serviceRaw, serviceErr := safepath.ReadFile(root, "service.json", 4096)
	var health audit.Health
	healthRaw, healthErr := safepath.ReadFile(auditRuntimeDir(), "health.json", 4096)
	if serviceErr == nil && healthErr == nil && json.Unmarshal(serviceRaw, &service) == nil && json.Unmarshal(healthRaw, &health) == nil && health.Epoch == service.Epoch {
		status.Error = audit.Clean(health.Error)
		status.DriverDrops = health.DriverDrops
		if health.Loaded && health.State == "running" && state == "active" && health.Version == service.Version && health.RulesSHA256 == service.RulesSHA256 {
			status.ActualVersion = health.Version
			status.Driver = "modern_ebpf"
		}
		if health.State == "failed" {
			status.State = "failed"
		}
	}
	return status, nil
}

func runControllerAudit(ctx context.Context, req controller.Request, in io.Reader, out io.Writer) (_ bool, result error) {
	if !strings.HasPrefix(req.Method, "audit.") {
		return false, nil
	}
	if !currentControllerPrincipal(ctx).Admin() {
		return true, errors.New("audit is restricted to the host controller endpoint")
	}
	if len(req.Args) != 0 || len(req.Flags) != 0 {
		return true, errors.New("audit accepts only its typed request")
	}
	var input auditRequest
	if len(req.Payload) != 0 {
		if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &input, 4096); err != nil {
			return true, err
		}
	}
	if input.Query.Box != "" {
		if err := layout.ValidateName("agentbox", input.Query.Box); err != nil {
			return true, err
		}
	}
	switch req.Method {
	case "audit.status":
		if len(req.Payload) != 0 {
			return true, errors.New("audit status takes no payload")
		}
		status, err := nativeAuditStatus(ctx)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(status)
	case "audit.logs", "audit.export":
		if input.ArchiveSHA256 != "" {
			return true, errors.New("audit logs do not accept an archive")
		}
		if req.Method == "audit.logs" && input.Query.Limit == 0 {
			input.Query.Limit = 500
		}
		if req.Method == "audit.export" && input.Query.Limit != 0 {
			return true, errors.New("audit export does not take a page limit")
		}
		root, err := layout.AuditDir()
		if err != nil {
			return true, err
		}
		encoder := json.NewEncoder(out)
		summary, err := (audit.Store{Root: filepath.Join(root, "logs")}).Scan(input.Query, func(event audit.Event) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return encoder.Encode(audit.Frame{Type: "event", Event: &event})
		})
		if err != nil {
			return true, err
		}
		return true, encoder.Encode(audit.Frame{Type: "summary", Summary: &summary})
	case "audit.enable", "audit.disable":
		if input.Query != (audit.Query{}) {
			return true, errors.New("audit toggle takes no history query")
		}
	default:
		return true, errors.New("unknown audit operation")
	}
	c := spaceenv.Current()
	if c == nil {
		return true, errors.New("audit requires native storage")
	}
	if err := auditBackendSupport(ctx, container.New(c.Space)); err != nil {
		return true, err
	}
	enable := req.Method == "audit.enable"
	var raw []byte
	if enable {
		pin, err := audit.Pinned(runtime.GOARCH)
		if err != nil || input.ArchiveSHA256 != pin.SHA256 {
			return true, errors.New("audit enable archive does not match the guest architecture pin")
		}
		raw, err = io.ReadAll(io.LimitReader(in, audit.MaxArchive+1))
		if err != nil {
			return true, err
		}
		if err := pin.Verify(raw); err != nil {
			return true, err
		}
		if err := auditKernelSupport(); err != nil {
			return true, err
		}
	} else if input.ArchiveSHA256 != "" {
		return true, errors.New("audit disable takes no archive")
	}
	release, err := auditAcquire(ctx, c.Space, true)
	if err != nil {
		return true, err
	}
	defer release()
	defer completeBeforeUnlock(ctx, &result, nil)
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil {
		return true, err
	}
	if err := controller.BeginScopedEffects(ctx, controller.EffectIntent{Scope: &controller.Scope{Writes: []controller.Resource{{Kind: controller.ResourceAudit}}}}); err != nil {
		return true, err
	}
	root, err := layout.AuditDir()
	if err != nil {
		return true, err
	}
	if err := safepath.WriteFile(c.DiskMount, "audit/.format", []byte("yoloctl-audit/v1\n"), 0600); err != nil {
		return true, err
	}
	client := container.New(c.Space)
	state, stateErr := auditServiceState(ctx, client, audit.ServiceName)
	if stateErr != nil || (state != "inactive" && state != "unknown") {
		if err := auditServiceStop(ctx, client, audit.ServiceName); err != nil {
			return true, &controller.EffectsUncertain{Cause: err}
		}
	}
	if !enable {
		if cfg.Audit != nil {
			cfg.Audit.Enabled = false
		} else {
			cfg.Audit = &spaceconfig.AuditSettings{}
		}
		if err := cfg.Save(); err != nil {
			return true, err
		}
	} else {
		tools, err := audit.Install(root, runtime.GOARCH, raw)
		if err != nil {
			return true, err
		}
		configuration, err := audit.Configuration(root, tools, layout.AuditMetadataPath())
		if err != nil {
			return true, err
		}
		if err := safepath.WriteFile(root, "rules.yaml", []byte(audit.Rules), 0600); err != nil {
			return true, err
		}
		if err := safepath.WriteFile(root, "falco.json", configuration, 0600); err != nil {
			return true, err
		}
		service := audit.ServiceConfig{Epoch: req.ID, Version: audit.Version, RulesSHA256: audit.RulesDigest()}
		doc, err := json.Marshal(service)
		if err != nil {
			return true, err
		}
		if err := safepath.WriteFile(root, "service.json", doc, 0600); err != nil {
			return true, err
		}
		cfg.Audit = &spaceconfig.AuditSettings{Enabled: true, Version: audit.Version, RulesSHA256: audit.RulesDigest()}
		if err := cfg.Save(); err != nil {
			return true, err
		}
		if err := auditServiceStart(ctx, client, auditUnit()); err != nil {
			return true, &controller.EffectsUncertain{Cause: err}
		}
		if err := waitAuditReady(ctx, service.Epoch); err != nil {
			return true, err
		}
	}
	status, err := nativeAuditStatus(ctx)
	if err != nil {
		return true, err
	}
	return true, json.NewEncoder(out).Encode(status)
}

func auditUnit() vmservice.Unit {
	return vmwire.AuditUnit()
}

func waitAuditReady(ctx context.Context, epoch string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := safepath.ReadFile(auditRuntimeDir(), "health.json", 4096)
		var health audit.Health
		if err == nil && json.Unmarshal(raw, &health) == nil && health.Epoch == epoch {
			if health.State == "running" && health.Loaded && health.Version == audit.Version && health.RulesSHA256 == audit.RulesDigest() {
				return nil
			}
			if health.State == "failed" {
				return fmt.Errorf("audit failed: %s", audit.Clean(health.Error))
			}
		}

		state, observeErr := auditServiceState(ctx, container.New(spaceenv.Current().Space), audit.ServiceName)
		if observeErr == nil && (state == "inactive" || state == "failed" || state == "unknown") {
			return errors.New("audit service stopped before reporting a verified modern eBPF load")
		}
		select {
		case <-ctx.Done():
			return &controller.EffectsUncertain{Cause: errors.New("audit service did not report a verified load result; inspect its operation receipt")}
		case <-ticker.C:
		}
	}
}

func collectNativeAudit(ctx context.Context) error {
	// This dedicated service process keeps any incidental child output private.
	unix.Umask(0077)
	c := spaceenv.Current()
	if c == nil {
		return errors.New("audit collector needs native storage")
	}
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil {
		return err
	}
	desired := nativeAuditDesired(cfg)
	if !desired.Enabled {
		return errors.New("auditing is disabled")
	}
	if err := desired.Validate(); err != nil {
		return err
	}
	root, err := layout.AuditDir()
	if err != nil {
		return err
	}
	var service audit.ServiceConfig
	raw, err := safepath.ReadFile(root, "service.json", 4096)
	if err != nil {
		return err
	}
	if err := decodeBoundedJSON(bytes.NewReader(raw), &service, 4096); err != nil {
		return err
	}
	pin, err := audit.Pinned(runtime.GOARCH)
	if err != nil {
		return err
	}
	tools, err := pin.InstalledDir(root)
	if err != nil {
		return err
	}
	if err := pin.VerifyInstalled(tools); err != nil {
		return err
	}
	return audit.Collect(ctx, root, auditRuntimeDir(), tools, service)
}

// Startup reconciliation does not retry an interrupted request. Any reservation
// or uncertainty in the audit domain prevents it from applying desired state.
func reconcileNativeAudit(ctx context.Context) error {
	c := spaceenv.Current()
	if c == nil {
		return errors.New("audit reconciliation needs native storage")
	}
	release, err := auditAcquire(ctx, c.Space, true)
	if err != nil {
		return err
	}
	defer release()
	receipts := controller.Receipts{Dir: filepath.Join(c.DiskMount, ".controller", "receipts")}
	if err := receipts.CheckAuditReconcile(c.Space, c.DiskID); err != nil {
		return err
	}
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil || cfg.Audit == nil || !cfg.Audit.Enabled {
		return err
	}
	if err := auditBackendSupport(ctx, container.New(c.Space)); err != nil {
		return err
	}
	state, err := auditServiceState(ctx, container.New(c.Space), audit.ServiceName)
	if err != nil {
		return err
	}
	if state == "active" || state == "activating" {
		return nil
	}
	return auditServiceStart(ctx, container.New(c.Space), auditUnit())
}

func startAuditRetention(parent context.Context, server *controller.Server) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		startup, stopStartup := context.WithTimeout(ctx, 5*time.Second)
		_, startupErr := server.RunMaintenance(startup, reconcileNativeAudit)
		stopStartup()
		if startupErr != nil && ctx.Err() == nil {
			_, _ = fmt.Fprintln(os.Stderr, "audit reconciliation deferred")
		}
		// One owner prevents metadata refresh from taking retention's admission.
		// Keep expiry due after a busy/failed attempt instead of skipping a minute.
		nextExpiry := time.Now().Add(time.Minute)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			expiryDue := !time.Now().Before(nextExpiry)
			checked := false
			_, err := server.RunMaintenance(ctx, func(ctx context.Context) error {
				var err error
				checked, err = maintainNativeAudit(ctx, expiryDue)
				return err
			})
			if checked {
				nextExpiry = time.Now().Add(time.Minute)
			}
			if err != nil && ctx.Err() == nil {
				_, _ = fmt.Fprintln(os.Stderr, "audit maintenance deferred")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

// pruneInactiveAudit owns the writer only after the native lock, receipt hold
// check and service observation exclude an active collector. Desired enablement
// is independent: a failed collector must still expire its retained events.
func pruneInactiveAudit(ctx context.Context) error {
	c := spaceenv.Current()
	release, err := auditAcquire(ctx, c.Space, true)
	if err != nil {
		return err
	}
	defer release()
	if err := (controller.Receipts{Dir: filepath.Join(c.DiskMount, ".controller", "receipts")}).CheckAuditReconcile(c.Space, c.DiskID); err != nil {
		return err
	}
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil {
		return err
	}
	if cfg.Audit == nil {
		return nil
	}
	state, err := auditServiceState(ctx, container.New(c.Space), audit.ServiceName)
	if err != nil {
		return err
	}
	if state != "inactive" && state != "failed" {
		return nil
	}
	root, err := layout.AuditDir()
	if err != nil {
		return err
	}
	logs := filepath.Join(root, "logs")
	if _, err := os.Lstat(logs); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	writer, err := audit.OpenWriter(audit.Store{Root: logs})
	if err != nil {
		return err
	}
	defer func() { _ = writer.Close() }()
	return writer.Prune(ctx)
}
