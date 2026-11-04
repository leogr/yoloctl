package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// spaceTelemetryMethod is the internal read stream behind `space view`. It is
// not a public command: the host opens it once and keeps it for the life of
// the view, so a dashboard costs one controller connection rather than one per
// refresh.
const spaceTelemetryMethod = "space.telemetry"

const (
	defaultTelemetryInterval = spacetelemetry.DefaultInterval
	minTelemetryInterval     = 250 * time.Millisecond
	maxTelemetryInterval     = 10 * time.Second
	// Declarations and session probes change rarely and cost several guest
	// processes; resource counters are cheap file reads.
	telemetryInventoryEvery = 5 * time.Second
	// Native-state leaves are listed by a guest helper per account.
	telemetryAccountsEvery = 30 * time.Second
)

func parseTelemetryInterval(values []string) (time.Duration, error) {
	if len(values) == 0 || values[0] == "" {
		return defaultTelemetryInterval, nil
	}
	d, err := time.ParseDuration(values[len(values)-1])
	if err != nil {
		return 0, fmt.Errorf("invalid refresh interval: %w", err)
	}
	if d < minTelemetryInterval || d > maxTelemetryInterval {
		return 0, fmt.Errorf("refresh interval must be between %s and %s", minTelemetryInterval, maxTelemetryInterval)
	}
	return d, nil
}

// telemetrySource gathers the declaration-first inventory. Its functions are
// replaceable so the stream can be tested without a VM.
type telemetrySource struct {
	space    string
	settings func() (*spaceconfig.Config, error)
	boxes    func(context.Context) ([]boxstate.Box, error)
	runtime  func(context.Context, []string) (map[string]container.RuntimeState, error)
	accounts func(context.Context) ([]spacetelemetry.Account, error)
	// presence reads the managed room from the shared snapshot and, while
	// this stream holds its lease, refreshes it off the sampling path.
	presence  *chatPresence
	collector spacetelemetry.Collector
	cpus      int
}

func nativeTelemetrySource(space string) telemetrySource {
	diskMount := ""
	if current := spaceenv.Current(); current != nil {
		diskMount = current.DiskMount
	}
	return telemetrySource{
		space:    space,
		settings: func() (*spaceconfig.Config, error) { return spaceconfig.Load(space) },
		boxes: func(ctx context.Context) ([]boxstate.Box, error) {
			return newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeSummary)
		},
		runtime: func(ctx context.Context, names []string) (map[string]container.RuntimeState, error) {
			return container.New(space).RuntimeStates(ctx, names)
		},
		accounts: func(ctx context.Context) ([]spacetelemetry.Account, error) {
			names, err := account.List(space)
			if err != nil {
				return nil, err
			}
			return telemetryAccounts(names, func(name string) []accountStateLeaf {
				return accountStateLeaves(ctx, space, name)
			})
		},
		presence: nativeChatPresence(),
		collector: spacetelemetry.Collector{
			ProcRoot:    "/proc",
			CgroupRoot:  "/sys/fs/cgroup",
			PoolRel:     workloadPoolRel(currentServiceManager()),
			DiskMount:   diskMount,
			SystemMount: "/",
			Statfs:      spacetelemetry.StatfsUsage,
			PTYStats:    telemetryPTYStats,
		},
		cpus: runtime.NumCPU(),
	}
}

func telemetryAccounts(names []string, leaves func(string) []accountStateLeaf) ([]spacetelemetry.Account, error) {
	out := make([]spacetelemetry.Account, 0, len(names))
	for _, name := range names {
		state := leaves(name)
		if state == nil {
			// accountStateLeaves uses nil for unavailable state and a non-nil
			// empty slice for a successful read with no materialized leaves.
			return nil, fmt.Errorf("account %q native state is unavailable", name)
		}
		a := spacetelemetry.Account{Name: name, Agents: []string{}}
		for _, leaf := range state {
			a.Agents = append(a.Agents, leaf.Agent)
		}
		out = append(out, a)
	}
	return out, nil
}

// telemetryPTYStats reads a box's holder counters through its root-only
// attach socket, beside the holder in the VM.
func telemetryPTYStats(ctx context.Context, box string) (spacetelemetry.PTYCounters, error) {
	s, err := session.ReadStats(ctx, session.SocketPath(box))
	if err != nil {
		return spacetelemetry.PTYCounters{}, err
	}
	return spacetelemetry.PTYCounters{Session: s.Session, OutputBytes: s.OutputBytes, InputPackets: s.InputPackets, InputBytes: s.InputBytes}, nil
}

// liveBox is the per-box runtime identity the sampler needs between
// inventories.
type liveBox struct {
	name    string
	state   container.RuntimeState
	session bool // a running session whose holder reports PTY counters
	// sessionUnknown means the session state could not be read, so its
	// output is unread rather than absent.
	sessionUnknown bool
}

func (src telemetrySource) inventory(ctx context.Context, accounts []spacetelemetry.Account, accountsErr error) (*spacetelemetry.Inventory, []liveBox) {
	inv := &spacetelemetry.Inventory{
		Space:    src.space,
		Grants:   []spacetelemetry.Grant{},
		Boxes:    []spacetelemetry.Box{},
		Accounts: accounts,
		Rooms:    []spacetelemetry.Room{},
		CPUs:     src.cpus,
	}
	if inv.Accounts == nil {
		inv.Accounts = []spacetelemetry.Account{}
	}
	if accountsErr != nil {
		inv.AccountsError = accountsErr.Error()
	}
	if cfg, err := src.settings(); err == nil {
		inv.DefaultAccount, inv.DefaultWorkdir = cfg.DefaultAccount, cfg.DefaultWorkdir
		for _, m := range cfg.Mounts {
			inv.Grants = append(inv.Grants, spacetelemetry.Grant{Path: m.Source, Writable: m.Writable})
		}
	}
	boxes, err := src.boxes(ctx)
	if err != nil {
		inv.BoxesError = err.Error()
		return inv, nil
	}
	var running []string
	for _, b := range boxes {
		if b.Runtime == boxstate.RuntimeRunning {
			running = append(running, b.Name)
		}
	}
	states, err := src.runtime(ctx, running)
	if err != nil {
		// Counters need the init PID; declarations remain accurate without it.
		states = nil
	}
	var live []liveBox
	for _, b := range boxes {
		tb := telemetryBox(b)
		st, ok := states[b.Name]
		if ok && st.Running {
			tb.Container = st.ID
			if !st.StartedAt.IsZero() {
				started := st.StartedAt
				tb.StartedAt = &started
			}
			live = append(live, liveBox{name: b.Name, state: st,
				session: b.Session.State == boxstate.SessionRunning, sessionUnknown: b.Session.State == boxstate.SessionUnknown})
			// The same batched inspect carries the launch-mode evidence; an
			// undecodable entry leaves the mode unknown, never confined.
			ev := agentbox.ClassifyRuntime(st.Details)
			tb.RuntimeMode, tb.RuntimeModeReason, tb.RuntimePrivileged = string(ev.Mode), ev.Reason, ev.AnyPrivileged
		}
		inv.Boxes = append(inv.Boxes, tb)
	}
	if src.presence != nil {
		if room := src.presence.room(boxes, states); room != nil {
			inv.Rooms = append(inv.Rooms, *room)
		}
	}
	return inv, live
}

func telemetryBox(b boxstate.Box) spacetelemetry.Box {
	tb := spacetelemetry.Box{
		Name:        b.Name,
		State:       string(b.State),
		Runtime:     string(b.Runtime),
		Session:     string(b.Session.State),
		ConfigError: b.ConfigError,
		Image:       boxStateImageField(b),
	}
	if b.Session.State == boxstate.SessionExited {
		code, at := b.Session.ExitCode, b.Session.ExitedAt
		tb.ExitCode = &code
		if !at.IsZero() {
			tb.ExitedAt = &at
		}
	}
	for _, r := range b.Reasons {
		if r.Message != "" {
			tb.Reasons = append(tb.Reasons, r.Message)
		}
	}
	cfg := b.Config
	if cfg == nil {
		tb.Agent = boxStateAgentField(b)
		return tb
	}
	tb.Agent, tb.Account = cfg.Agent, cfg.Account
	tb.WorkdirMode = cfg.Workdir.Mode
	if cfg.Workdir.IsHost() {
		tb.Workdir = cfg.Workdir.MountSource()
	}
	// Lenient on read paths: a missing agent profile keeps the box's own
	// features rather than hiding the box.
	tb.Features, _ = agentbox.EffectiveFeaturesStrict(cfg)
	if eff, err := agentbox.EffectiveCapabilities(cfg); err == nil {
		for _, c := range eff.Capabilities() {
			tb.Capabilities = append(tb.Capabilities, c.Name)
		}
	} else {
		tb.Capabilities = slices.Clone(cfg.Capabilities)
	}
	for _, p := range cfg.Ports {
		tb.Ports = append(tb.Ports, spacetelemetry.Port{HostIP: p.ResolvedHostIP(), Published: p.Published, Target: p.Target, Protocol: p.ResolvedProtocol()})
	}
	tb.CPUs, tb.Memory = cfg.Resources.CPUs, cfg.Resources.Memory
	tb.Unconfined = cfg.UnconfinedNetwork
	tb.Privileged = cfg.Privileged
	tb.SessionCommand = slices.Clone(cfg.SessionCommand)
	return tb
}

// streamTelemetry writes an inventory immediately and then at its cadence,
// with a sample at every interval. It ends when the reader goes away.
func streamTelemetry(ctx context.Context, out io.Writer, src telemetrySource, interval time.Duration) error {
	enc := spacetelemetry.NewEncoder(out)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if src.presence != nil {
		presenceCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); src.presence.run(presenceCtx) }()
		// The lease is released before the stream returns.
		defer func() { stop(); <-done }()
	}
	var (
		live          []liveBox
		accounts      []spacetelemetry.Account
		accountsErr   error
		nextInventory time.Time
		nextAccounts  time.Time
	)
	for {
		now := time.Now()
		if !now.Before(nextAccounts) {
			accounts, accountsErr = src.accounts(ctx)
			nextAccounts = now.Add(telemetryAccountsEvery)
		}
		if !now.Before(nextInventory) {
			var inv *spacetelemetry.Inventory
			inv, live = src.inventory(ctx, accounts, accountsErr)
			if err := enc.Encode(spacetelemetry.Frame{Kind: spacetelemetry.KindInventory, At: time.Now(), Inventory: inv}); err != nil {
				return err
			}
			nextInventory = now.Add(telemetryInventoryEvery)
		}
		if src.presence != nil && src.presence.takeChanged() {
			nextInventory = time.Now() // the first snapshot replaces "waiting"
		}
		targets := make([]spacetelemetry.Target, 0, len(live))
		for _, lb := range live {
			targets = append(targets, spacetelemetry.Target{Name: lb.name, PID: lb.state.PID, ID: lb.state.ID, Session: lb.session, SessionUnknown: lb.sessionUnknown})
		}
		sample := src.collector.Sample(ctx, targets)
		if err := enc.Encode(spacetelemetry.Frame{Kind: spacetelemetry.KindSample, At: time.Now(), Sample: sample}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
