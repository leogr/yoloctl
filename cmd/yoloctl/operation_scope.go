package main

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

// Footprints are resolved under the native space lock, from verified
// declarations and runtime facts, never from public request payloads. Any
// fact that cannot be established makes the footprint the whole space: a
// coarse hold is safe, a missed resource is not.
var globalIntent = controller.EffectIntent{Scope: &controller.Scope{Global: true}}

// Actual forwards survive failed reconciliation of an edited declaration.
// Use the same authoritative supervisor inventory as lifecycle teardown.
var footprintForwards = func(ctx context.Context, space, name string) ([]port.Spec, error) {
	return port.List(ctx, container.New(space), name)
}

func boxResource(name string) controller.Resource {
	return controller.Resource{Kind: controller.ResourceBox, Name: name}
}

func accountResource(name string) controller.Resource {
	return controller.Resource{Kind: controller.ResourceAccount, Name: name}
}

var (
	chatDomain   = controller.Resource{Kind: controller.ResourceChat}
	imagesDomain = controller.Resource{Kind: controller.ResourceImages}
	portsDomain  = controller.Resource{Kind: controller.ResourcePorts}
)

// footprint accumulates a scope without duplicates.
type footprint struct {
	writes, reads []controller.Resource
	global        bool
}

func (f *footprint) write(r ...controller.Resource) {
	f.writes = appendResources(f.writes, r...)
}

func (f *footprint) read(r ...controller.Resource) {
	f.reads = appendResources(f.reads, r...)
}

func (f *footprint) readAccount(name string) {
	if name != "" && name != account.None {
		if account.ValidateName(name) != nil {
			f.global = true
			return
		}
		f.read(accountResource(name))
	}
}

func appendResources(list []controller.Resource, rs ...controller.Resource) []controller.Resource {
	for _, r := range rs {
		found := false
		for _, item := range list {
			found = found || item == r
		}
		if !found {
			list = append(list, r)
		}
	}
	return list
}

func (f *footprint) intent(verifier string) controller.EffectIntent {
	if f.global || len(f.writes) == 0 {
		return globalIntent
	}
	return controller.EffectIntent{Scope: &controller.Scope{Writes: f.writes, Reads: f.reads}, Verifier: verifier}
}

// chatRoleBoxes names the boxes whose lifecycle changes the shared room
// itself (its server and human boxes). A missing room has none.
var chatRoleBoxes = func() (map[string]bool, error) {
	store, err := controller.NativeChatStore()
	if err != nil {
		return nil, err
	}
	r, err := store.Load()
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]bool{r.Server.Name: true, r.Human.Name: true}, nil
}

// chatMemberServer reports whether a box name holds an active membership in
// the managed room, and that room's server box. A missing room has none.
var chatMemberServer = func(name string) (string, bool, error) {
	store, err := controller.NativeChatStore()
	if err != nil {
		return "", false, err
	}
	r, err := store.Load()
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, member := activeChatMember(r, name)
	return r.Server.Name, member, nil
}

// boxRequest is what a box mutation itself asks for, beyond the current
// declaration: a selected account and whether it changes published ports.
type boxRequest struct {
	account string
	ports   bool
}

func boxRequestFlags(cmd *cobra.Command) boxRequest {
	var r boxRequest
	if flag := cmd.Flag("account"); flag != nil {
		r.account = flag.Value.String()
	}
	if flag := cmd.Flag("port"); flag != nil {
		r.ports = flag.Changed
	}
	return r
}

// boxLifecycleFootprint covers one box's lifecycle, reconcile or settings
// change: the box, its old/new accounts, and the shared domains it can
// depend on. Image and role-publication writes are added durably when they
// are reached (the build guard and role synchronization).
func boxLifecycleFootprint(ctx context.Context, space, name string, req boxRequest, f *footprint) {
	if validateAgentboxName(name) != nil {
		f.global = true
		return
	}
	f.write(boxResource(name))
	f.read(imagesDomain, chatDomain)
	cfg, err := agentbox.Load(space, name)
	switch {
	case err == nil:
		if ref, _, err := agentbox.ResolvedImage(cfg); err != nil {
			f.global = true
		} else {
			f.read(controller.Resource{Kind: controller.ResourceImages, Name: ref})
		}
		f.readAccount(cfg.Account)
		if len(cfg.Ports) > 0 {
			f.write(portsDomain)
		}
	case errors.Is(err, fs.ErrNotExist):
		if req.account == "" {
			settings, err := spaceconfig.Load(space)
			if err != nil {
				f.global = true
				return
			}
			f.readAccount(settings.DefaultAccount)
		}
	default:
		f.global = true
		return
	}
	f.readAccount(req.account)
	runtimeAccountFootprint(ctx, space, name, f)
	portFootprint(ctx, space, name, f)
	if req.ports {
		f.write(portsDomain)
	}
	roles, err := chatRoleBoxes()
	if err != nil {
		f.global = true
		return
	}
	if roles[name] {
		f.write(chatDomain)
	}
}

func portFootprint(ctx context.Context, space, name string, f *footprint) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	actual, err := footprintForwards(ctx, space, name)
	if err != nil {
		f.global = true
		return
	}
	if len(actual) != 0 {
		f.write(portsDomain)
	}
}

// A completed failed repoint can leave a container on a different account
// from its declaration. Both are dependencies until that container is gone.
// RuntimeInventory establishes absence or fails closed, without session probes.
func runtimeAccountFootprint(ctx context.Context, space, name string, f *footprint) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := newBoxStateResolver().ResolveOne(ctx, space, name, boxstate.RuntimeInventory)
	if err != nil {
		f.global = true
		return
	}
	if b.Runtime == boxstate.RuntimeMissing && b.Container == nil {
		return
	}
	if b.Container == nil || b.Runtime != boxstate.RuntimeRunning && b.Runtime != boxstate.RuntimeStopped && b.Runtime != boxstate.RuntimeOrphaned {
		f.global = true
		return
	}
	labels := parseLabels(b.Container.Labels)
	agent, actual := labels[agentbox.LabelAgent], labels[agentbox.LabelAccount]
	if b.Container.ID == "" || b.Container.Name != name || labels[agentbox.LabelSpace] != space || labels[agentbox.LabelAgentbox] != name || layout.ValidateName("agent-profile", agent) != nil {
		f.global = true
		return
	}
	// Agentless containers intentionally omit the account label. An explicit
	// agent=none plus no conflicting account is positive runtime evidence.
	if agent == "none" {
		if actual != "" && actual != account.None {
			f.global = true
		}
		return
	}
	if account.ValidateName(actual) != nil {
		f.global = true
		return
	}
	f.readAccount(actual)
}

// spaceBoxNames is every declared or present box of the space: a superset of
// any target set a bulk operation can select before it starts.
func spaceBoxNames(ctx context.Context, space string) ([]string, error) {
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(boxes))
	for _, b := range boxes {
		if b.Runtime == boxstate.RuntimeUnknown || b.Runtime == boxstate.RuntimeUnavailable {
			return nil, errors.New("runtime inventory is incomplete")
		}
		names = append(names, b.Name)
	}
	return names, nil
}

// accountConsumers is every box that selects the account by declaration or
// runtime label. A box whose runtime account is unknown counts as one.
func accountConsumers(ctx context.Context, space, name string) ([]string, error) {
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range boxes {
		if b.Runtime == boxstate.RuntimeUnknown || b.Runtime == boxstate.RuntimeUnavailable || b.Declaration == boxstate.DeclarationInvalid {
			return nil, errors.New("runtime inventory is incomplete")
		}
		selected := b.Config != nil && b.Config.Account == name
		if b.Container != nil {
			actual, known := parseLabels(b.Container.Labels)[agentbox.LabelAccount]
			selected = selected || actual == name || !known && parseLabels(b.Container.Labels)[agentbox.LabelAgent] != "none"
		}
		if selected {
			names = append(names, b.Name)
		}
	}
	return names, nil
}

// spaceOperationIntent resolves a wrapped mutation's footprint. It runs
// under the native space lock, before entity locks and the first effect.
func spaceOperationIntent(cmd *cobra.Command, path, space string, args []string) controller.EffectIntent {
	ctx := cmd.Context()
	var f footprint
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	verifier := ""
	switch path {
	case "box create", "box start", "box stop", "box restart", "box destroy", "box recreate", "box reconcile", "box set", "box attach", "attach":
		if len(args) == 0 {
			return globalIntent
		}
		boxLifecycleFootprint(ctx, space, arg(0), boxRequestFlags(cmd), &f)
		if keep, _ := cmd.Flags().GetBool("keep-manifest"); path == "box destroy" && !keep {
			// Destroy revokes the box's own active membership, which changes
			// the room and reloads its server, exactly as chat revoke does.
			server, member, err := chatMemberServer(arg(0))
			switch {
			case err != nil:
				f.global = true
			case member:
				f.write(chatDomain, boxResource(server))
			}
		}
		if path == "box set" {
			if cfg, err := agentbox.Load(space, arg(0)); err == nil {
				if v, err := cmd.Flags().GetString("image"); err == nil && v != "" {
					cfg.Image = v
				}
				plural, _ := cmd.Flags().GetStringSlice("features")
				singular, _ := cmd.Flags().GetStringArray("feature")
				options, _ := cmd.Flags().GetStringArray("feature-option")
				priv, _ := cmd.Flags().GetBool("privileged")
				if _, err := applySetOptions(cfg, setOptions{Features: selectedFeatureFlag(cmd, plural, singular), FeatureOptions: options, Privileged: boolFlag(cmd, "privileged", priv)}); err != nil {
					f.global = true
				} else {
					f.readDesiredImage(cfg)
				}
			}
		}
	case "box copy":
		if len(args) != 2 {
			return globalIntent
		}
		boxLifecycleFootprint(ctx, space, arg(1), boxRequestFlags(cmd), &f)
		if snapshot, crossSpace := ctx.Value(copySourceKey{}).(*agentbox.Config); crossSpace {
			if snapshot != nil && len(snapshot.Ports) != 0 {
				f.write(portsDomain)
			}
		} else {
			if validateAgentboxName(arg(0)) != nil {
				return globalIntent
			}
			f.read(boxResource(arg(0)))
			source, err := agentbox.Load(space, arg(0))
			if err != nil {
				return globalIntent
			}
			f.readAccount(source.Account)
			runtimeAccountFootprint(ctx, space, arg(0), &f)
			if len(source.Ports) != 0 {
				f.write(portsDomain)
			}
		}
	case "port forward", "port unforward":
		if len(args) == 0 {
			return globalIntent
		}
		boxLifecycleFootprint(ctx, space, arg(0), boxRequestFlags(cmd), &f)
		f.write(portsDomain)
	case "account create", "account secret set", "account secret unset", "account observe", "account destroy":
		if account.ValidateName(arg(0)) != nil {
			return globalIntent
		}
		f.write(accountResource(arg(0)))
		switch path {
		case "account create", "account secret set", "account secret unset":
			verifier = controller.VerifierAccountDocument
		case "account destroy":
			consumers, err := accountConsumers(ctx, space, arg(0))
			if err != nil {
				return globalIntent
			}
			for _, name := range consumers {
				f.write(boxResource(name))
				portFootprint(ctx, space, name, &f)
			}
		}
	case "image build":
		if cmd.Flag("tag") == nil {
			return globalIntent
		}
		f.write(controller.Resource{Kind: controller.ResourceImages, Name: cmd.Flag("tag").Value.String()})
	case "image pull", "image refresh", "image prune":
		f.write(imagesDomain)
	case "agent update":
		if flag := cmd.Flag("agentbox"); flag != nil && flag.Value.String() != "" {
			if validateAgentboxName(flag.Value.String()) != nil {
				return globalIntent
			}
			f.write(boxResource(flag.Value.String()))
			break
		}
		names, err := spaceBoxNames(ctx, space)
		if err != nil {
			return globalIntent
		}
		containers, err := container.New(space).List(ctx, agentbox.LabelAgentbox)
		if err != nil {
			return globalIntent
		}
		for _, c := range containers {
			names = append(names, c.Name)
		}
		for _, name := range names {
			if validateAgentboxName(name) != nil {
				return globalIntent
			}
			f.write(boxResource(name))
		}
	default:
		return globalIntent
	}
	if state, ok := ctx.Value(warmKey{}).(*imageWarmState); ok && state.desired != nil {
		f.readDesiredImage(state.desired)
	}
	return f.intent(verifier)
}

// beginOperationEffects is the effect gate of a wrapped controller mutation.
// Outside a worker there is no controller request to gate.
func beginOperationEffects(cmd *cobra.Command, path, space string, args []string) error {
	if controllerRequestID(cmd.Context()) == "" {
		return nil
	}
	return controller.BeginScopedEffects(cmd.Context(), spaceOperationIntent(cmd, path, space, args))
}

// wrapperGatesEffects reports a wrapped command whose controller method is
// gated at the wrapper. box exec gates in its handler, after selecting the
// endpoint, so a lost result keeps its command-uncertain meaning.
func wrapperGatesEffects(path string) bool {
	if path == "box exec" {
		return false
	}
	method := strings.ReplaceAll(path, " ", ".")
	if path == "attach" {
		method = "box.attach"
	}
	return controllerOperations[method].EffectGated
}

// boxEditIntent covers an edited declaration's old and new references.
func boxEditIntent(ctx context.Context, space string, edited *agentbox.Config) controller.EffectIntent {
	var f footprint
	boxLifecycleFootprint(ctx, space, edited.Name, boxRequest{account: edited.Account, ports: len(edited.Ports) > 0}, &f)
	f.readDesiredImage(edited)
	return f.intent("")
}

// accountDocumentIntent is the footprint of a single verified account
// declaration write.
func accountDocumentIntent(name string, verified bool) controller.EffectIntent {
	if account.ValidateName(name) != nil {
		return globalIntent
	}
	var f footprint
	f.write(accountResource(name))
	if verified {
		return f.intent(controller.VerifierAccountDocument)
	}
	return f.intent("")
}

// chatIntent covers the shared room plus boxes the operation can change.
func chatIntent(verifier string, boxes ...string) controller.EffectIntent {
	var f footprint
	f.write(chatDomain)
	for _, name := range boxes {
		if name == "" {
			continue
		}
		if validateAgentboxName(name) != nil {
			return globalIntent
		}
		f.write(boxResource(name))
	}
	return f.intent(verifier)
}

// execIntent is an authorized exec's footprint: its box. The remote
// command's own effects are untracked; a lost result stays command-uncertain.
func execIntent(name string) controller.EffectIntent {
	if validateAgentboxName(name) != nil {
		return globalIntent
	}
	var f footprint
	f.write(boxResource(name))
	return f.intent("")
}

func (f *footprint) readDesiredImage(cfg *agentbox.Config) {
	ref, _, err := agentbox.ResolvedImage(cfg)
	if err != nil {
		f.global = true
		return
	}
	f.read(controller.Resource{Kind: controller.ResourceImages, Name: ref})
	f.readAccount(cfg.Account)
}
func addDesiredImageIntent(ctx context.Context, intent controller.EffectIntent) controller.EffectIntent {
	state, _ := ctx.Value(warmKey{}).(*imageWarmState)
	if state == nil || state.desired == nil || intent.Scope == nil || intent.Scope.Global {
		return intent
	}
	f := footprint{writes: intent.Scope.Writes, reads: intent.Scope.Reads}
	f.readDesiredImage(state.desired)
	return f.intent(intent.Verifier)
}
