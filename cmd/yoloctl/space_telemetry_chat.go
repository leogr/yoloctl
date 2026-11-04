package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"golang.org/x/sys/unix"
)

const (
	// The lease holder reads the managed server's status at this cadence;
	// readers treat a snapshot older than presenceStaleAfter as unavailable.
	presenceRefreshEvery = 5 * time.Second
	presenceStaleAfter   = 3 * presenceRefreshEvery
	presenceStatusBound  = 2 * time.Second
	// A holder whose own inventory is older than this releases the lease.
	presenceInventoryStale = 3 * telemetryInventoryEvery
	maxPresenceSnapshot    = 1 << 20
)

// presenceSnapshot is the one shared observation of the managed room. It is
// display evidence only: no mutation reads it or gains authority from it.
type presenceSnapshot struct {
	RoomID          string              `json:"room_id"`
	Generation      uint64              `json:"generation"`
	ServerContainer string              `json:"server_container"`
	ObservedNS      int64               `json:"observed_ns"`
	Room            spacetelemetry.Room `json:"room"`
}

// vmMonotonic is CLOCK_MONOTONIC: one clock for every worker in the VM,
// immune to wall-clock steps.
func vmMonotonic() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0
	}
	return ts.Nano()
}

// managedRoomTelemetry maps a verified server status to the room's enrolled
// boxes. A connection counts for a box only while its declaration still has
// the enrolled identity; other connected accounts are external. Presence
// is the participants' own IRC state: no box is probed for its listener.
func managedRoomTelemetry(r *managedchat.Room, boxes []boxstate.Box, status func(*agentbox.Config) (managedchat.Applied, error)) *spacetelemetry.Room {
	room := &spacetelemetry.Room{Server: r.Server.Name, Managed: true, Ports: []int{}, Members: []string{}, Participants: []spacetelemetry.Participant{}}
	byName := map[string]boxstate.Box{}
	for _, b := range boxes {
		byName[b.Name] = b
	}
	server, ok := byName[r.Server.Name]
	switch {
	case !ok || server.Config == nil:
		room.Error = "managed server declaration is unavailable"
	case server.Runtime != boxstate.RuntimeRunning:
		room.Error = "managed server is not running"
	}
	if room.Error != "" {
		room.Participants = nil
		return room
	}
	applied, err := status(server.Config)
	if err != nil {
		room.Error, room.Participants = "managed server status unavailable: "+err.Error(), nil
		return room
	}
	counted := map[string]bool{}
	if human, err := managedchat.AuthAccount(r.Human.ID); err == nil {
		room.Local = int(applied.Accounts[human].Connections)
		counted[human] = true
	}
	for _, m := range r.Members {
		if m.Revoked {
			continue
		}
		account, err := managedchat.AuthAccount(m.ID)
		if err != nil {
			continue
		}
		box, ok := byName[m.Name]
		if !ok || box.Config == nil || box.Config.ID != m.BoxID {
			// Its connections are not this box's: a reused name or a
			// missing declaration stays external.
			continue
		}
		counted[account] = true
		p := spacetelemetry.Participant{Box: m.Name, MemberID: m.ID, Account: account, Connections: int(applied.Accounts[account].Connections)}
		room.Participants = append(room.Participants, p)
		if p.Connections > 0 {
			room.Members = append(room.Members, m.Name)
		}
	}
	for account, c := range applied.Accounts {
		if !counted[account] {
			room.External += int(c.Connections)
		}
	}
	return room
}

// nativeServerStatus is the verified server's applied status for presence,
// with the container ID its control peer was verified against. The applied
// generation and configuration must match the declaration.
func nativeServerStatus(ctx context.Context, r *managedchat.Room, server *agentbox.Config) (managedchat.Applied, string, error) {
	store, err := controller.NativeChatStore()
	if err != nil {
		return managedchat.Applied{}, "", err
	}
	proof, err := store.Expected(r)
	if err != nil {
		return managedchat.Applied{}, "", err
	}
	control, details, err := managedServerControl(ctx, r, server)
	if err != nil {
		return managedchat.Applied{}, "", err
	}
	applied, err := control.Call(ctx, "status")
	if err == nil {
		err = applied.Matches(proof)
	}
	return applied, details.ID, err
}

// chatPresence is one telemetry stream's handle on the shared snapshot.
// Every stream reads it; only the stream holding the lease refreshes it,
// in its own goroutine, so the server status read never runs on the
// inventory or sample path.
type chatPresence struct {
	root, rel string // rel is per room; set once the manifest names it
	uid, gid  int
	clock     func() int64
	manifest  func() (*managedchat.Room, error)
	status    func(context.Context, *managedchat.Room, *agentbox.Config) (managedchat.Applied, string, error)

	mu        sync.Mutex
	boxes     []boxstate.Box
	states    map[string]container.RuntimeState
	seen      bool  // an inventory has arrived
	invAt     int64 // when, on clock
	lease     int   // -1 unless this stream holds the lease
	published bool
	changed   bool
	kick      chan struct{}
}

// The cache and lease belong to the controller's own (root) identity, never
// the native sandbox UID: a workload must not be able to write telemetry.
func nativeChatPresence() *chatPresence {
	if spaceenv.Current() == nil {
		return nil
	}
	p := newChatPresence(spaceenv.RunRoot, os.Geteuid(), os.Getegid())
	p.manifest = func() (*managedchat.Room, error) {
		store, err := controller.NativeChatStore()
		if err != nil {
			return nil, err
		}
		return store.ReadManifestSnapshot()
	}
	p.status = nativeServerStatus
	return p
}

func newChatPresence(root string, uid, gid int) *chatPresence {
	return &chatPresence{root: root, uid: uid, gid: gid, clock: vmMonotonic, lease: -1, kick: make(chan struct{}, 1)}
}

func presenceRel(roomID string) (string, error) {
	dir, err := layout.ChatPresenceDir(roomID)
	if err != nil {
		return "", err
	}
	return filepath.Rel(spaceenv.RunRoot, dir)
}

// managed returns the room declaration when its runtime is enabled.
func (p *chatPresence) managed() (*managedchat.Room, error) {
	r, err := p.manifest()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("managed chat declaration unavailable")
	}
	if !r.RuntimeEnabled || !r.Prepared {
		return nil, nil
	}
	return r, nil
}

// room records the stream's inventory for the refresher and returns the
// managed room from the shared snapshot, or nil without a managed room. A
// missing, stale, future-stamped or mismatched snapshot is an explicit
// error, never an empty room.
func (p *chatPresence) room(boxes []boxstate.Box, states map[string]container.RuntimeState) *spacetelemetry.Room {
	p.mu.Lock()
	first := !p.seen
	p.boxes, p.states, p.seen, p.invAt = boxes, states, true, p.clock()
	p.mu.Unlock()
	if first {
		p.poke()
	}
	r, err := p.managed()
	if err != nil {
		return &spacetelemetry.Room{Managed: true, Ports: []int{}, Members: []string{}, Error: err.Error()}
	}
	if r == nil {
		return nil
	}
	unavailable := func(why string) *spacetelemetry.Room {
		return &spacetelemetry.Room{Server: r.Server.Name, Managed: true, Ports: []int{}, Members: []string{}, Error: why}
	}
	snap, err := p.read(r.ID)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return unavailable("waiting for managed server status")
	case err != nil:
		return unavailable("managed presence snapshot unavailable")
	}
	age := p.clock() - snap.ObservedNS
	switch {
	case snap.RoomID != r.ID || snap.Generation != r.Generation:
		return unavailable("managed presence is refreshing")
	case age < 0 || age > int64(presenceStaleAfter):
		return unavailable("managed server status is stale")
	case snap.Room.Error == "" && (states[r.Server.Name].ID == "" || states[r.Server.Name].ID != snap.ServerContainer):
		return unavailable("managed server changed; presence is refreshing")
	}
	room := snap.Room
	room.Server, room.Managed = r.Server.Name, true
	return &room
}

// read decodes the bounded snapshot strictly from a private regular file.
func (p *chatPresence) read(roomID string) (*presenceSnapshot, error) {
	rel, err := presenceRel(roomID)
	if err != nil {
		return nil, err
	}
	raw, info, err := safepath.ReadFileInfo(p.root, filepath.Join(rel, "snapshot.json"), maxPresenceSnapshot)
	if err != nil {
		return nil, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != p.uid || info.Mode().Perm() != 0o600 {
		return nil, errors.New("presence snapshot is not private")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var snap presenceSnapshot
	if err := dec.Decode(&snap); err != nil {
		return nil, errors.New("invalid presence snapshot")
	}
	// Exactly one value: trailing data, even a stray delimiter, is refused.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid presence snapshot")
	}
	return &snap, nil
}

func (p *chatPresence) poke() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// takeChanged reports whether this stream published its first snapshot
// since the last call, so the stream can send a fresh inventory early
// instead of showing the room unavailable for a whole inventory cadence.
func (p *chatPresence) takeChanged() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := p.changed
	p.changed = false
	return changed
}

// run refreshes while this stream holds the lease, retrying to acquire it
// at the refresh cadence, and releases it when ctx ends. The lease file is
// never removed: another worker may be waiting to lock it.
func (p *chatPresence) run(ctx context.Context) {
	defer p.release()
	ticker := time.NewTicker(presenceRefreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		case <-ticker.C:
		}
		p.refresh(ctx)
	}
}

func (p *chatPresence) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lease >= 0 {
		_ = unix.Close(p.lease) // closing the descriptor drops the flock
		p.lease = -1
	}
}

// acquire takes the room's lease without waiting.
func (p *chatPresence) acquire(rel string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lease >= 0 && p.rel == rel {
		return true
	}
	if p.lease >= 0 {
		_ = unix.Close(p.lease)
		p.lease = -1
	}
	if err := safepath.PrivateDirectory(p.root, rel, p.uid, p.gid); err != nil {
		return false
	}
	fd, err := unix.Open(filepath.Join(p.root, rel, "lease"), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return false
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != p.uid || st.Mode&0o777 != 0o600 {
		_ = unix.Close(fd)
		return false
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		return false
	}
	p.lease, p.rel = fd, rel
	return true
}

// refresh observes the server and publishes one snapshot. Only the lease
// holder gets here.
func (p *chatPresence) refresh(ctx context.Context) {
	p.mu.Lock()
	boxes, states, seen, invAt := p.boxes, p.states, p.seen, p.invAt
	p.mu.Unlock()
	if !seen {
		return
	}
	if p.clock()-invAt > int64(presenceInventoryStale) {
		// This stream's own loop is stalled (a suspended reader): its view
		// of the space is old, so hand the lease to a stream that is not.
		p.release()
		return
	}
	r, err := p.managed()
	if err != nil || r == nil {
		return
	}
	rel, err := presenceRel(r.ID)
	if err != nil || !p.acquire(rel) {
		return
	}
	statusCtx, cancel := context.WithTimeout(ctx, presenceStatusBound)
	verified := ""
	room := managedRoomTelemetry(r, boxes, func(server *agentbox.Config) (managedchat.Applied, error) {
		applied, id, err := p.status(statusCtx, r, server)
		verified = id
		return applied, err
	})
	cancel()
	if room.Error == "" && verified != states[r.Server.Name].ID {
		// The inventory saw another container than the one the control peer
		// proved: publish nothing rather than mix identities.
		return
	}
	p.publish(rel, &presenceSnapshot{RoomID: r.ID, Generation: r.Generation, ServerContainer: verified, ObservedNS: p.clock(), Room: *room})
}

func (p *chatPresence) publish(rel string, snap *presenceSnapshot) {
	raw, err := json.Marshal(snap)
	if err != nil || len(raw) > maxPresenceSnapshot {
		return
	}
	if safepath.WriteFile(p.root, filepath.Join(rel, "snapshot.json"), raw, 0o600) == nil {
		p.mu.Lock()
		p.changed = p.changed || !p.published
		p.published = true
		p.mu.Unlock()
	}
}
