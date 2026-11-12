package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

var auditRulesValidate = audit.ValidateRuleSet
var auditRulesTools = receiveAuditRuleTools

func auditRulesRevision(cfg *spaceconfig.Config) string {
	// Includes enable/disable and the current built-in/tool dependency. An editor
	// cannot save against an intervening toggle, rules change or build upgrade.
	raw, _ := json.Marshal(struct {
		Desired *spaceconfig.AuditSettings
		Build   string
	}{cfg.Audit, audit.RulesPinsDigest()})
	return audit.Digest(raw)
}

func nativeAuditRules(cfg *spaceconfig.Config) (auditRulesResult, error) {
	var result auditRulesResult
	root, err := layout.AuditDir()
	if err != nil {
		return result, err
	}
	generation := ""
	if cfg.Audit != nil {
		generation = cfg.Audit.RulesGeneration
	}
	result.Rules, err = (audit.RuleStore{Root: root}).Read(generation)
	result.Revision = auditRulesRevision(cfg)
	result.Architecture = runtime.GOARCH
	return result, err
}

func receiveAuditRuleTools(_ context.Context, hash string, in io.Reader) (string, error) {
	pin, err := audit.Pinned(runtime.GOARCH)
	if err != nil {
		return "", err
	}
	if hash != pin.SHA256 {
		return "", errors.New("audit rule tools do not match the guest architecture pin")
	}
	raw := make([]byte, pin.Bytes)
	if _, err := io.ReadFull(in, raw); err != nil {
		return "", err
	}
	root, err := layout.AuditDir()
	if err != nil {
		return "", err
	}
	return audit.Install(root, runtime.GOARCH, raw)
}

// Called only after the outer dispatcher has checked host principal and the
// typed request envelope. There are no box/operator rule-management grants.
func runControllerAuditRules(ctx context.Context, req controller.Request, input auditRequest, in io.Reader, out io.Writer) (result error) {
	if input.Query != (audit.Query{}) {
		return errors.New("rules operations take no history query")
	}
	c := spaceenv.Current()
	if c == nil {
		return errors.New("audit rules require native storage")
	}
	if req.Method == "audit.rules.list" {
		if len(req.Payload) != 0 {
			return errors.New("audit rules list takes no payload")
		}
		cfg, err := spaceconfig.Load(c.Space)
		if err != nil {
			return err
		}
		snapshot, err := nativeAuditRules(cfg)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(snapshot)
	}
	if req.Method != "audit.rules.import" && req.Method != "audit.rules.remove" && req.Method != "audit.rules.edit" {
		return errors.New("unknown audit rules operation")
	}
	if input.Rules == nil || input.Rules.Expected == "" {
		return errors.New("audit rules change requires its expected revision")
	}
	release, err := auditAcquire(ctx, c.Space, true)
	if err != nil {
		return err
	}
	defer release()
	defer completeBeforeUnlock(ctx, &result, nil)
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil {
		return err
	}
	if input.Rules.Expected != auditRulesRevision(cfg) {
		return audit.ErrRuleGenerationChanged
	}
	before, err := nativeAuditRules(cfg)
	if err != nil {
		return err
	}
	tools, err := auditRulesTools(ctx, input.ArchiveSHA256, in)
	if err != nil {
		return err
	}
	root, err := layout.AuditDir()
	if err != nil {
		return err
	}
	change := audit.RuleChange{Kind: strings.TrimPrefix(req.Method, "audit.rules."), Name: input.Rules.Name, SHA256: input.Rules.SHA256, Body: in}
	if change.Kind != "import" && change.Name != "" {
		if change.Kind != "remove" {
			return errors.New("selection edit takes no file name")
		}
	}
	if change.Kind != "remove" && change.SHA256 != "" {
		return errors.New("SHA256 selector is only for removal")
	}
	prepared, validation, changed, err := (audit.RuleStore{Root: root}).Prepare(ctx, before.Rules.Generation, change, tools, layout.AuditMetadataPath(), auditRulesValidate)
	response := auditRulesResult{Architecture: runtime.GOARCH, Revision: before.Revision, Rules: before.Rules, Validation: validation}
	if err != nil {
		// Diagnostics are data on the ordinary response stream, never control or
		// event output. The host escapes them before terminal display.
		if writeErr := json.NewEncoder(out).Encode(response); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return err
	}
	if !changed {
		return json.NewEncoder(out).Encode(response)
	}
	published := false
	defer func() {
		if !published {
			if err := (audit.RuleStore{Root: root}).Discard(prepared); err != nil {
				slog.Warn("Audit rule attempt cleanup deferred", "err", err)
			}
		}
	}()

	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.Audit != nil && cfg.Audit.Enabled {
		if err := auditBackendSupport(ctx, container.New(c.Space)); err != nil {
			return err
		}
		if err := auditKernelSupport(); err != nil {
			return err
		}
	}
	if err := controller.BeginScopedEffects(ctx, controller.EffectIntent{Scope: &controller.Scope{Writes: []controller.Resource{{Kind: controller.ResourceAudit}}}}); err != nil {
		return err
	}
	published = true // cfg.Save may have committed even when publication reports an error.
	if err := selectNativeAuditRules(ctx, req.ID, cfg, prepared); err != nil {
		return err
	}
	response.Revision, response.Rules, response.Changed = auditRulesRevision(cfg), prepared, true
	status, err := nativeAuditStatus(ctx)
	if err != nil {
		return err
	}
	if err := retireNativeAuditRuleGenerations(ctx, before.Rules.Generation, prepared.Generation); err != nil {
		status.RuleCleanupWarning = err.Error()
	}
	response.Status = &status
	return json.NewEncoder(out).Encode(response)
}

// The caller owns native audit serialization and has passed the effect gate.
// A failure after publication is not rolled back or replayed. The matching
// generation is part of service health, separate from validation acceptance.
func selectNativeAuditRules(ctx context.Context, epoch string, cfg *spaceconfig.Config, set audit.RuleSet) error {
	enabled := cfg.Audit != nil && cfg.Audit.Enabled
	cfg.Audit = &spaceconfig.AuditSettings{Enabled: enabled, Version: audit.Version, RulesSHA256: audit.RulesDigest(), RulesGeneration: set.Generation, EffectiveSHA256: set.Fingerprint}
	if err := cfg.Save(); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	root, err := layout.AuditDir()
	if err != nil {
		return err
	}
	client := container.New(cfg.Name)
	state, observeErr := auditServiceState(ctx, client, audit.ServiceName)
	if observeErr != nil || (state != "inactive" && state != "unknown") {
		if err := auditServiceStop(ctx, client, audit.ServiceName); err != nil {
			return &controller.EffectsUncertain{Cause: err}
		}
	}
	service := audit.ServiceConfig{Epoch: epoch, Version: audit.Version, RulesSHA256: audit.RulesDigest(), RulesGeneration: set.Generation, EffectiveSHA256: set.Fingerprint}
	raw, err := json.Marshal(service)
	if err != nil {
		return err
	}
	if err := safepath.WriteFile(root, "service.json", raw, 0600); err != nil {
		return err
	}
	if err := auditServiceStart(ctx, client, auditUnit()); err != nil {
		return &controller.EffectsUncertain{Cause: err}
	}
	return waitAuditReadyFor(ctx, service)
}

func describeAuditRuleValidation(validation audit.Validation, err error) error {
	if err == nil {
		return nil
	}
	// Preserve the native diagnostic as ordinary escaped error text; no new
	// feature diagnostic quota or storage path is introduced.
	return fmt.Errorf("%w; Falco diagnostics: %s", err, audit.DiagnosticText(strings.TrimSpace(validation.Diagnostics)))
}

// Keep this operation's recovery input until a later admitted change, and
// always protect the service reference even when it appears inactive. Cleanup
// ends before completeBeforeUnlock records the existing receipt completion.
func retireNativeAuditRuleGenerations(ctx context.Context, prior, selected string) error {
	root, err := layout.AuditDir()
	if err != nil {
		return err
	}
	store := audit.RuleStore{Root: root}
	keep := []string{prior, selected}
	raw, err := safepath.ReadFile(root, "service.json", 4096)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		var service audit.ServiceConfig
		if err := decodeBoundedJSON(bytes.NewReader(raw), &service, 4096); err != nil {
			return err
		}
		sum, err := hex.DecodeString(service.RulesSHA256)
		if err != nil || len(sum) != 32 || service.Epoch == "" || service.Version == "" {
			return errors.New("audit service reference is incomplete; retaining generations")
		}
		if service.RulesGeneration != "" {
			set, err := store.Read(service.RulesGeneration)
			if err != nil {
				return err
			}
			if set.Fingerprint != service.EffectiveSHA256 {
				return errors.New("service rule fingerprint differs from retained generation")
			}
			keep = append(keep, service.RulesGeneration)
		} else if service.EffectiveSHA256 != "" {
			return errors.New("service rule fingerprint has no generation")
		}
	}
	return store.Retire(ctx, keep...)
}
