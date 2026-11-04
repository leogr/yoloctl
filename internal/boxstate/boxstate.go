package boxstate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

type Depth string

const (
	RuntimeSummary Depth = "runtime_summary"
	// RuntimeInventory is an authoritative lifecycle snapshot. It fails when
	// runtime cannot be listed and omits session probes (cleanup stops holders).
	RuntimeInventory Depth = "runtime_inventory"
	FullDiff         Depth = "full_diff"
)

type DeclarationState string

const (
	DeclarationMissing  DeclarationState = "missing"
	DeclarationDeclared DeclarationState = "declared"
	DeclarationInvalid  DeclarationState = "invalid"
)

type RuntimeState string

const (
	RuntimeUnknown     RuntimeState = "unknown"
	RuntimeUnavailable RuntimeState = "unavailable"
	RuntimeMissing     RuntimeState = "missing"
	RuntimeStopped     RuntimeState = "stopped"
	RuntimeRunning     RuntimeState = "running"
	RuntimeOrphaned    RuntimeState = "orphaned"
)

type SessionState string

const (
	SessionUnknown SessionState = "unknown"
	SessionNone    SessionState = "none"
	SessionRunning SessionState = "running"
	SessionExited  SessionState = "exited"
	SessionLost    SessionState = "lost"
)

// SessionBuild reports whether a running session's holder speaks this exact
// build and terminal core, as its own stats handshake answers. Views of this
// build cannot attach to an "other" session until its box is restarted.
type SessionBuild string

const (
	SessionBuildCurrent SessionBuild = "current"
	SessionBuildOther   SessionBuild = "other"
	SessionBuildUnknown SessionBuild = "unknown"
)

type DriftState string

const (
	DriftUnknown       DriftState = "unknown"
	DriftInSync        DriftState = "in_sync"
	DriftNeedsCreate   DriftState = "needs_create"
	DriftNeedsRecreate DriftState = "needs_recreate"
	DriftNeedsRestart  DriftState = "needs_restart"
	DriftLiveOnly      DriftState = "live_only"
	DriftError         DriftState = "error"
)

type DisplayState string

const (
	DisplayConfigured  DisplayState = "configured"
	DisplayStopped     DisplayState = "stopped"
	DisplayRunning     DisplayState = "running"
	DisplayExited      DisplayState = "exited"
	DisplayLost        DisplayState = "lost"
	DisplayUnavailable DisplayState = "unavailable"
	DisplayDrifted     DisplayState = "drifted"
	DisplayOrphaned    DisplayState = "orphaned"
	DisplayInvalid     DisplayState = "invalid"
)

type ReasonCode string

const (
	ReasonDeclarationInvalid ReasonCode = "declaration_invalid"
	ReasonRuntimeUnavailable ReasonCode = "runtime_unavailable"
	ReasonRuntimeProbeFailed ReasonCode = "runtime_probe_failed"
	ReasonContainerMissing   ReasonCode = "container_missing"
	ReasonContainerStopped   ReasonCode = "container_stopped"
	ReasonContainerRunning   ReasonCode = "container_running"
	ReasonRuntimeOrphaned    ReasonCode = "runtime_orphaned"
	ReasonSessionRunning     ReasonCode = "session_running"
	ReasonSessionExited      ReasonCode = "session_exited"
	ReasonSessionLost        ReasonCode = "session_lost"
	ReasonDriftNeedsCreate   ReasonCode = "drift_needs_create"
	ReasonDriftNeedsRecreate ReasonCode = "drift_needs_recreate"
	ReasonDriftNeedsRestart  ReasonCode = "drift_needs_restart"
	ReasonDriftLiveOnly      ReasonCode = "drift_live_only"
	ReasonDriftError         ReasonCode = "drift_error"
)

type Reason struct {
	Code    ReasonCode `json:"code"`
	Message string     `json:"message,omitempty"`
}

type SessionInfo struct {
	State SessionState `json:"state"`
	// Build and Holder are set only for a running session probed beside its
	// holder. Holder names that holder incarnation from the marker it wrote
	// at startup, empty when unreadable.
	Build    SessionBuild `json:"build,omitempty"`
	Holder   string       `json:"holder,omitempty"`
	ExitCode int          `json:"exit_code,omitempty"`
	ExitedAt time.Time    `json:"exited_at,omitempty"`
	// LaunchToken is the launch correlation token in the running holder's
	// marker, empty when tokenless or unread. It binds the session to a
	// conversation record; it is not shown.
	LaunchToken string `json:"-"`
}

type Box struct {
	Space       string           `json:"space"`
	Name        string           `json:"name"`
	State       DisplayState     `json:"state"`
	Declaration DeclarationState `json:"declaration"`
	Runtime     RuntimeState     `json:"runtime"`
	Session     SessionInfo      `json:"session"`
	// Conversation is the agent's conversation identity (internal/agentsession),
	// resolved natively beside the records; nil for agentless boxes or when
	// the view does not probe sessions.
	Conversation *agentsession.Status `json:"conversation,omitempty"`
	Drift        DriftState           `json:"drift"`
	Reasons      []Reason             `json:"reasons,omitempty"`

	Config      *agentbox.Config     `json:"-"`
	ConfigError string               `json:"config_error,omitempty"`
	Container   *container.Container `json:"-"`
	RuntimeText string               `json:"runtime_status,omitempty"`
	DiffPlan    *agentbox.DiffPlan   `json:"-"`
	DriftError  string               `json:"drift_error,omitempty"`
}

type Resolver struct {
	ListSpaces     func(context.Context) ([]vmhost.Instance, error)
	ListContainers func(context.Context, string) ([]container.Container, error)
	ProbeSession   func(context.Context, string, string, bool) SessionInfo
	InspectActual  func(context.Context, *agentbox.Config) (agentbox.ActualState, error)
	ComposePolicy  func(*agentbox.Config) ([]byte, error)
	ResolvedImage  func(*agentbox.Config) (string, bool, error)
	// Conversation describes a declared agent box's conversation identity.
	// Nil skips it.
	Conversation func(Box) *agentsession.Status
}

func NewResolver() Resolver {
	return Resolver{
		ListSpaces: func(ctx context.Context) ([]vmhost.Instance, error) {
			return vmhost.New().List(ctx)
		},
		ListContainers: func(ctx context.Context, space string) ([]container.Container, error) {
			return container.New(space).List(ctx, agentbox.LabelAgentbox)
		},
		ProbeSession: func(ctx context.Context, space, name string, running bool) SessionInfo {
			c := container.New(space)
			info := ProbeSession(ctx, c, name, running)
			// Only a native worker beside the holders can ask them; a remote
			// probe would need a bridge per session.
			if info.State == SessionRunning && spaceenv.Native() {
				info.Build, info.Holder, info.LaunchToken = probeHolderMarker(ctx, c, name)
			}
			return info
		},
		Conversation: func(b Box) *agentsession.Status {
			// Records live on the native disk; only a worker can read them.
			if !spaceenv.Native() || b.Config == nil || !b.Config.HasAgent() {
				return nil
			}
			profile, err := agentprofile.Lookup(b.Config.Agent)
			if err != nil {
				return &agentsession.Status{Provider: b.Config.Agent, Policy: "unknown", Note: "agent profile unavailable: " + err.Error()}
			}
			return agentsession.Describe(b.Config, profile, b.Session.State == SessionRunning, b.Session.LaunchToken)
		},
		InspectActual: func(ctx context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
			return agentbox.LiveInspector{Client: container.New(cfg.Space)}.Inspect(ctx, cfg)
		},
		ComposePolicy: agentbox.ComposePolicy,
		ResolvedImage: agentbox.ResolvedImage,
	}
}

func (r Resolver) ResolveAll(ctx context.Context, depth Depth) ([]Box, error) {
	spaces, err := spaceconfig.List()
	if err != nil {
		return nil, err
	}
	var out []Box
	for _, space := range spaces {
		boxes, err := r.ResolveSpace(ctx, space, depth)
		if err != nil {
			return nil, err
		}
		out = append(out, boxes...)
	}
	sortBoxes(out)
	return out, nil
}

func (r Resolver) ResolveOne(ctx context.Context, space, name string, depth Depth) (Box, error) {
	boxes, err := r.ResolveNamed(ctx, space, []string{name}, depth)
	if err != nil {
		return Box{}, err
	}
	return boxes[0], nil
}

// ResolveNamed resolves only the named boxes of space. The space's
// declarations and containers are listed once, as for ResolveSpace; the
// per-box session probe, drift and conversation run for those identities
// alone, so each result equals that box's ResolveSpace entry. The results
// follow names, one per distinct name; a name with neither a declaration nor
// a container is reported missing.
func (r Resolver) ResolveNamed(ctx context.Context, space string, names []string, depth Depth) ([]Box, error) {
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	boxes, err := r.resolveSpace(ctx, space, depth, func(name string) bool { return want[name] })
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Box, len(boxes))
	for _, b := range boxes {
		byName[b.Name] = b
	}
	out := make([]Box, 0, len(want))
	for _, name := range names {
		if !want[name] {
			continue // a repeated name
		}
		want[name] = false
		b, ok := byName[name]
		if !ok {
			b = Box{
				Space:       space,
				Name:        name,
				State:       DisplayConfigured,
				Declaration: DeclarationMissing,
				Runtime:     RuntimeMissing,
				Session:     SessionInfo{State: SessionUnknown},
				Drift:       DriftUnknown,
			}
		}
		out = append(out, b)
	}
	return out, nil
}

func (r Resolver) ResolveSpace(ctx context.Context, space string, depth Depth) ([]Box, error) {
	return r.resolveSpace(ctx, space, depth, func(string) bool { return true })
}

// resolveSpace resolves the boxes whose names selected accepts and returns
// only those: per-box probes, drift and conversations never run for others.
func (r Resolver) resolveSpace(ctx context.Context, space string, depth Depth, selected func(string) bool) ([]Box, error) {
	if r.ListSpaces == nil || r.ListContainers == nil || r.ProbeSession == nil ||
		r.InspectActual == nil || r.ComposePolicy == nil {
		def := NewResolver()
		if r.ListSpaces == nil {
			r.ListSpaces = def.ListSpaces
		}
		if r.ListContainers == nil {
			r.ListContainers = def.ListContainers
		}
		if r.ProbeSession == nil {
			r.ProbeSession = def.ProbeSession
		}
		if r.InspectActual == nil {
			r.InspectActual = def.InspectActual
		}
		if r.ComposePolicy == nil {
			r.ComposePolicy = def.ComposePolicy
		}
	}

	all, byName, err := declaredBoxes(space)
	if err != nil {
		return nil, err
	}
	// Unselected boxes are dropped before any per-box work.
	var boxes []Box
	for _, b := range all {
		if selected(b.Name) {
			byName[b.Name] = len(boxes)
			boxes = append(boxes, b)
		} else {
			delete(byName, b.Name)
		}
	}
	running, status, err := r.spaceRunning(ctx, space)
	if err != nil || !running {
		if depth == RuntimeInventory {
			if err != nil {
				return nil, fmt.Errorf("inspect space runtime: %w", err)
			}
			return nil, fmt.Errorf("space runtime unavailable (%s)", status)
		}
		msg := status
		if err != nil {
			msg = err.Error()
		}
		for i := range boxes {
			if boxes[i].Declaration == DeclarationInvalid {
				boxes[i].State = deriveDisplay(boxes[i])
				continue
			}
			boxes[i].Runtime = RuntimeUnavailable
			boxes[i].Session.State = SessionUnknown
			boxes[i].Drift = DriftUnknown
			boxes[i].Reasons = append(boxes[i].Reasons, Reason{Code: ReasonRuntimeUnavailable, Message: msg})
			boxes[i].State = deriveDisplay(boxes[i])
		}
		sortBoxes(boxes)
		return boxes, nil
	}

	containers, err := r.ListContainers(ctx, space)
	if err != nil {
		if depth == RuntimeInventory {
			return nil, fmt.Errorf("list runtime containers: %w", err)
		}
		for i := range boxes {
			if boxes[i].Declaration != DeclarationInvalid {
				boxes[i].Runtime = RuntimeUnavailable
				boxes[i].Reasons = append(boxes[i].Reasons, Reason{Code: ReasonRuntimeProbeFailed, Message: err.Error()})
			}
			boxes[i].State = deriveDisplay(boxes[i])
		}
		sortBoxes(boxes)
		return boxes, nil
	}
	for _, ctr := range containers {
		name := ctr.Name
		if !selected(name) {
			continue
		}
		running := ContainerRunning(ctr.Status)
		if idx, ok := byName[name]; ok {
			b := &boxes[idx]
			b.Container = cloneContainer(ctr)
			b.RuntimeText = ctr.Status
			if running {
				b.Runtime = RuntimeRunning
				b.Reasons = append(b.Reasons, Reason{Code: ReasonContainerRunning})
			} else {
				b.Runtime = RuntimeStopped
				b.Reasons = append(b.Reasons, Reason{Code: ReasonContainerStopped, Message: ctr.Status})
			}
			if depth != RuntimeInventory {
				b.Session = r.ProbeSession(ctx, space, name, running)
			}
			addSessionReason(b)
			continue
		}
		b := Box{
			Space:       space,
			Name:        name,
			Declaration: DeclarationMissing,
			Runtime:     RuntimeOrphaned,
			Session:     SessionInfo{State: SessionUnknown},
			Drift:       DriftUnknown,
			Container:   cloneContainer(ctr),
			RuntimeText: ctr.Status,
			Reasons: []Reason{{
				Code:    ReasonRuntimeOrphaned,
				Message: "runtime container has no agentbox declaration",
			}},
		}
		if depth != RuntimeInventory {
			b.Session = r.ProbeSession(ctx, space, name, running)
		}
		addSessionReason(&b)
		b.State = deriveDisplay(b)
		byName[name] = len(boxes)
		boxes = append(boxes, b)
	}

	for i := range boxes {
		b := &boxes[i]
		if b.Declaration == DeclarationDeclared && b.Runtime == RuntimeUnknown {
			b.Runtime = RuntimeMissing
			b.Session.State = SessionNone
			b.Reasons = append(b.Reasons, Reason{Code: ReasonContainerMissing})
		}
		if depth == FullDiff {
			r.resolveDrift(ctx, b)
		}
		if depth != RuntimeInventory && r.Conversation != nil && b.Config != nil {
			b.Conversation = r.Conversation(*b)
		}
		b.State = deriveDisplay(*b)
	}
	sortBoxes(boxes)
	return boxes, nil
}

func declaredBoxes(space string) ([]Box, map[string]int, error) {
	names, err := agentbox.ListNames(space)
	if err != nil {
		return nil, nil, err
	}
	boxes := make([]Box, 0, len(names))
	byName := make(map[string]int, len(names))
	for _, name := range names {
		b := Box{
			Space:       space,
			Name:        name,
			Declaration: DeclarationDeclared,
			Runtime:     RuntimeUnknown,
			Session:     SessionInfo{State: SessionUnknown},
			Drift:       DriftUnknown,
		}
		cfg, err := agentbox.Load(space, name)
		if err != nil {
			b.Declaration = DeclarationInvalid
			b.ConfigError = err.Error()
			b.Reasons = append(b.Reasons, Reason{Code: ReasonDeclarationInvalid, Message: err.Error()})
		} else {
			b.Config = cfg
		}
		b.State = deriveDisplay(b)
		byName[name] = len(boxes)
		boxes = append(boxes, b)
	}
	return boxes, byName, nil
}

func (r Resolver) spaceRunning(ctx context.Context, space string) (bool, string, error) {
	instances, err := r.ListSpaces(ctx)
	if err != nil {
		return false, "", err
	}
	for _, inst := range instances {
		if inst.Name == space {
			if inst.Status == "Running" {
				return true, inst.Status, nil
			}
			return false, inst.Status, nil
		}
	}
	return false, "missing", nil
}

func (r Resolver) resolveDrift(ctx context.Context, b *Box) {
	if b.Declaration != DeclarationDeclared || b.Config == nil {
		return
	}
	if b.Runtime == RuntimeUnavailable {
		b.Drift = DriftUnknown
		return
	}
	if b.Runtime == RuntimeMissing {
		b.Drift = DriftNeedsCreate
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftNeedsCreate})
		return
	}
	actual, err := r.InspectActual(ctx, b.Config)
	if err != nil {
		b.Drift = DriftError
		b.DriftError = err.Error()
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftError, Message: err.Error()})
		return
	}
	policy, err := r.ComposePolicy(b.Config)
	if err != nil {
		b.Drift = DriftError
		b.DriftError = err.Error()
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftError, Message: err.Error()})
		return
	}
	desiredImage, _, err := r.ResolvedImage(b.Config)
	if err != nil {
		b.Drift = DriftError
		b.DriftError = err.Error()
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftError, Message: err.Error()})
		return
	}
	plan := agentbox.Diff(b.Config, actual, desiredImage, policy)
	b.DiffPlan = &plan
	switch {
	case plan.NeedsCreate:
		b.Drift = DriftNeedsCreate
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftNeedsCreate, Message: plan.CreateReason})
	case plan.NeedsRecreate:
		b.Drift = DriftNeedsRecreate
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftNeedsRecreate, Message: plan.RecreateReason})
	case plan.NeedsRestart:
		b.Drift = DriftNeedsRestart
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftNeedsRestart, Message: plan.RestartReason})
	case len(plan.PortForwardsToAdd) > 0 || len(plan.PortForwardsToRemove) > 0:
		b.Drift = DriftLiveOnly
		b.Reasons = append(b.Reasons, Reason{Code: ReasonDriftLiveOnly})
	default:
		b.Drift = DriftInSync
	}
}

func ProbeSession(ctx context.Context, c *container.Client, name string, containerRunning bool) SessionInfo {
	if session.HolderUnitActive(ctx, c, name) {
		return SessionInfo{State: SessionRunning}
	}
	if ts, ok := session.ReadVMTombstone(ctx, c, name); ok {
		return SessionInfo{State: SessionExited, ExitCode: ts.ExitCode, ExitedAt: ts.ExitedAt}
	}
	if containerRunning && c.ShellQuiet(ctx, "sudo", "test", "-f", session.MarkerPath(name)) == nil {
		return SessionInfo{State: SessionLost}
	}
	return SessionInfo{State: SessionNone}
}

// ProbeHolder identifies a running session's holder: whether it speaks this
// build and core, asked through the read-only stats handshake that admits no
// viewer, and its incarnation from its marker ("" when unreadable).
func ProbeHolder(ctx context.Context, c *container.Client, name string) (SessionBuild, string) {
	build, holder, _ := probeHolderMarker(ctx, c, name)
	return build, holder
}

// probeHolderMarker is ProbeHolder plus the launch token in the marker.
func probeHolderMarker(ctx context.Context, c *container.Client, name string) (SessionBuild, string, string) {
	holder, token := "", ""
	if m, ok := session.ReadVMMarker(ctx, c, name); ok {
		holder, token = m.Identity(), m.LaunchToken
	}
	_, err := session.ReadStats(ctx, session.SocketPath(name))
	return sessionBuild(err), holder, token
}

// sessionBuild classifies a stats handshake: only the holder's explicit
// answer decides current or other; a failure to ask stays unknown.
func sessionBuild(err error) SessionBuild {
	switch {
	case err == nil:
		return SessionBuildCurrent
	case errors.Is(err, session.ErrBuildMismatch):
		return SessionBuildOther
	default:
		return SessionBuildUnknown
	}
}

func addSessionReason(b *Box) {
	switch b.Session.State {
	case SessionRunning:
		b.Reasons = append(b.Reasons, Reason{Code: ReasonSessionRunning})
	case SessionExited:
		b.Reasons = append(b.Reasons, Reason{Code: ReasonSessionExited})
	case SessionLost:
		b.Reasons = append(b.Reasons, Reason{Code: ReasonSessionLost})
	}
}

func deriveDisplay(b Box) DisplayState {
	switch {
	case b.Declaration == DeclarationInvalid:
		return DisplayInvalid
	case b.Runtime == RuntimeOrphaned:
		return DisplayOrphaned
	case b.Runtime == RuntimeUnavailable:
		return DisplayUnavailable
	case b.Drift == DriftNeedsRecreate || b.Drift == DriftNeedsRestart ||
		b.Drift == DriftLiveOnly || b.Drift == DriftError:
		return DisplayDrifted
	case b.Session.State == SessionLost:
		return DisplayLost
	case b.Session.State == SessionExited:
		return DisplayExited
	case b.Runtime == RuntimeRunning:
		return DisplayRunning
	case b.Runtime == RuntimeStopped:
		return DisplayStopped
	default:
		return DisplayConfigured
	}
}

func ContainerRunning(status string) bool {
	s := strings.TrimSpace(strings.ToLower(status))
	return strings.HasPrefix(s, "up") || s == "running"
}

func cloneContainer(c container.Container) *container.Container {
	clone := c
	return &clone
}

func sortBoxes(boxes []Box) {
	sort.Slice(boxes, func(i, j int) bool {
		if boxes[i].Space != boxes[j].Space {
			return boxes[i].Space < boxes[j].Space
		}
		return boxes[i].Name < boxes[j].Name
	})
}

func ReasonSummary(reasons []Reason) string {
	if len(reasons) == 0 {
		return ""
	}
	for _, r := range reasons {
		if r.Message != "" {
			return r.Message
		}
	}
	return fmt.Sprint(reasons[0].Code)
}
