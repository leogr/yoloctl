package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

func managedTelemetryFixture() (*managedchat.Room, []boxstate.Box, map[string]container.RuntimeState) {
	r := &managedchat.Room{ID: managedchat.NewID(), Generation: 4, Server: managedchat.Box{Name: "chat", ID: managedchat.NewID()}, Human: managedchat.Member{ID: managedchat.NewID(), Name: "leo"},
		Prepared: true, RuntimeEnabled: true}
	member := func(name string) managedchat.Member {
		return managedchat.Member{ID: managedchat.NewID(), Name: name, BoxID: managedchat.NewID()}
	}
	a, b, gone, reused := member("a"), member("b"), member("gone"), member("reused")
	gone.Revoked = true
	r.Members = []managedchat.Member{a, b, gone, reused}
	box := func(name, id string, running bool) boxstate.Box {
		rt := boxstate.RuntimeStopped
		if running {
			rt = boxstate.RuntimeRunning
		}
		return boxstate.Box{Name: name, Runtime: rt, Config: &agentbox.Config{Name: name, ID: id}}
	}
	boxes := []boxstate.Box{box("chat", r.Server.ID, true), box("a", a.BoxID, true), box("b", b.BoxID, false), box("reused", managedchat.NewID(), true)}
	states := map[string]container.RuntimeState{"chat": {ID: "c-chat", Running: true}, "a": {ID: "c-a", Running: true}, "reused": {ID: "c-r", Running: true}}
	return r, boxes, states
}

func accountOf(t *testing.T, id string) string {
	t.Helper()
	account, err := managedchat.AuthAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

// Members come from the verified server's own connection counts, mapped to
// boxes only while their declaration keeps the enrolled identity.
func TestManagedRoomTelemetryMapsVerifiedConnections(t *testing.T) {
	r, boxes, _ := managedTelemetryFixture()
	a, b, gone, reused := r.Members[0], r.Members[1], r.Members[2], r.Members[3]
	applied := managedchat.Applied{Accounts: map[string]managedchat.Connections{
		accountOf(t, r.Human.ID): {Connections: 2},
		accountOf(t, a.ID):       {Connections: 1},
		accountOf(t, gone.ID):    {Connections: 1},
		accountOf(t, reused.ID):  {Connections: 1},
		"mstranger":              {Connections: 1},
	}}
	room := managedRoomTelemetry(r, boxes, func(cfg *agentbox.Config) (managedchat.Applied, error) {
		if cfg.Name != "chat" {
			t.Fatalf("status asked of %q", cfg.Name)
		}
		return applied, nil
	})
	want := &spacetelemetry.Room{Server: "chat", Managed: true, Ports: []int{}, Members: []string{"a"}, Local: 2, External: 3,
		Participants: []spacetelemetry.Participant{
			{Box: "a", MemberID: a.ID, Account: accountOf(t, a.ID), Connections: 1},
			{Box: "b", MemberID: b.ID, Account: accountOf(t, b.ID)},
		}}
	if !reflect.DeepEqual(room, want) {
		t.Fatalf("room = %+v\nwant %+v", room, want)
	}
}

// An unverifiable or stopped server is an explicit error, never an empty
// room that would read as nobody connected.
func TestManagedRoomTelemetryKeepsServerUncertaintyExplicit(t *testing.T) {
	r, boxes, _ := managedTelemetryFixture()
	room := managedRoomTelemetry(r, boxes, func(*agentbox.Config) (managedchat.Applied, error) {
		return managedchat.Applied{}, errors.New("peer is not the managed server")
	})
	if room.Error != "managed server status unavailable: peer is not the managed server" || room.Participants != nil {
		t.Fatalf("status failure = %+v", room)
	}
	boxes[0].Runtime = boxstate.RuntimeStopped
	room = managedRoomTelemetry(r, boxes, func(*agentbox.Config) (managedchat.Applied, error) {
		t.Fatal("stopped server was asked for status")
		return managedchat.Applied{}, nil
	})
	if room.Error != "managed server is not running" || !room.Managed || room.Server != "chat" {
		t.Fatalf("stopped server = %+v", room)
	}
}

// presenceWorld is one VM as several telemetry workers see it: a shared
// tmpfs root, one monotonic clock and counted server calls.
type presenceWorld struct {
	t        *testing.T
	root     string
	clock    atomic.Int64
	statuses atomic.Int32
	r        *managedchat.Room
	boxes    []boxstate.Box
	states   map[string]container.RuntimeState
	applied  managedchat.Applied
	// verified is the server container the control peer proves.
	verified func(string) string
}

func newPresenceWorld(t *testing.T) *presenceWorld {
	w := &presenceWorld{t: t, root: t.TempDir()}
	w.r, w.boxes, w.states = managedTelemetryFixture()
	w.clock.Store(int64(time.Hour))
	w.verified = func(server string) string { return w.states[server].ID }
	w.applied = managedchat.Applied{Accounts: map[string]managedchat.Connections{accountOf(t, w.r.Members[0].ID): {Connections: 1}}}
	return w
}

// worker is one telemetry stream's presence handle.
func (w *presenceWorld) worker() *chatPresence {
	p := newChatPresence(w.root, os.Geteuid(), os.Getegid())
	p.clock = w.clock.Load
	p.manifest = func() (*managedchat.Room, error) { return w.r, nil }
	p.status = func(_ context.Context, r *managedchat.Room, _ *agentbox.Config) (managedchat.Applied, string, error) {
		w.statuses.Add(1)
		return w.applied, w.verified(r.Server.Name), nil
	}
	return p
}

func (w *presenceWorld) room(p *chatPresence) *spacetelemetry.Room {
	return p.room(w.boxes, w.states)
}

// Several open views share one refresher: only the lease holder reads the
// server; the others read its snapshot. When the holder's stream ends,
// another worker takes over the kept lease file.
func TestChatPresenceSharesOneRefresherAcrossWorkers(t *testing.T) {
	w := newPresenceWorld(t)
	a, b := w.worker(), w.worker()
	if room := w.room(a); room == nil || room.Error != "waiting for managed server status" || room.Server != "chat" {
		t.Fatalf("before any snapshot = %+v", room)
	}
	w.room(b)
	a.refresh(t.Context())
	if w.statuses.Load() != 1 || !a.takeChanged() || a.takeChanged() {
		t.Fatalf("holder refresh: statuses=%d", w.statuses.Load())
	}
	b.refresh(t.Context())
	if w.statuses.Load() != 1 {
		t.Fatal("a second worker refreshed while the lease was held")
	}
	room := w.room(b)
	if room.Error != "" || !reflect.DeepEqual(room.Members, []string{"a"}) {
		t.Fatalf("reader room = %+v", room)
	}
	w.clock.Add(int64(presenceRefreshEvery))
	a.refresh(t.Context())
	if w.statuses.Load() != 2 {
		t.Fatalf("second refresh: statuses=%d", w.statuses.Load())
	}
	a.release()
	lease := filepath.Join(w.root, "chat-presence", w.r.ID, "lease")
	if _, err := os.Lstat(lease); err != nil {
		t.Fatalf("lease file removed on release: %v", err)
	}
	b.refresh(t.Context())
	if w.statuses.Load() != 3 {
		t.Fatal("no takeover after the holder released its lease")
	}
	b.release()
}

// Stale, future-stamped, mismatched or corrupted evidence is unavailable,
// never an old room or an empty one.
func TestChatPresenceRejectsStaleOrMismatchedSnapshots(t *testing.T) {
	w := newPresenceWorld(t)
	p := w.worker()
	defer p.release()
	w.room(p)
	p.refresh(t.Context())
	if room := w.room(p); room.Error != "" {
		t.Fatalf("fresh room = %+v", room)
	}
	w.clock.Add(12 * int64(time.Second))
	if room := w.room(p); room.Error != "" {
		t.Fatalf("a current snapshot was refused: %+v", room)
	}
	w.clock.Add(4 * int64(time.Second))
	if room := w.room(p); room.Error != "managed server status is stale" || room.Participants != nil {
		t.Fatalf("stale snapshot = %+v", room)
	}
	w.clock.Add(-time.Hour.Nanoseconds())
	if room := w.room(p); room.Error != "managed server status is stale" {
		t.Fatalf("future-stamped snapshot = %+v", room)
	}
	w.clock.Add(time.Hour.Nanoseconds() - 16*int64(time.Second))
	w.r.Generation++
	if room := w.room(p); room.Error != "managed presence is refreshing" {
		t.Fatalf("other generation = %+v", room)
	}
	w.r.Generation--
	w.states["chat"] = container.RuntimeState{ID: "c-new", Running: true}
	if room := w.room(p); !strings.Contains(room.Error, "managed server changed") {
		t.Fatalf("replaced server = %+v", room)
	}
	w.states["chat"] = container.RuntimeState{ID: "c-chat", Running: true}
	snapshot := filepath.Join(w.root, "chat-presence", w.r.ID, "snapshot.json")
	if err := os.WriteFile(snapshot, []byte(`{"room_id":"x","extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if room := w.room(p); room.Error != "managed presence snapshot unavailable" {
		t.Fatalf("corrupt snapshot = %+v", room)
	}
	p.refresh(t.Context())
	valid, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, trailing := range []string{"]", "}", " {}", "\n{\"room_id\":\"x\"}"} {
		if err := os.WriteFile(snapshot, append(append([]byte(nil), valid...), trailing...), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := p.read(w.r.ID); err == nil {
			t.Fatalf("trailing %q after the snapshot was accepted", trailing)
		}
	}
	if err := os.WriteFile(snapshot, append(valid, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.read(w.r.ID); err != nil {
		t.Fatalf("a trailing newline was refused: %v", err)
	}
	if err := os.Chmod(snapshot, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.read(w.r.ID); err == nil {
		t.Fatal("a non-private snapshot was accepted")
	}
}

// The stream runs the refresher beside sampling, sends the managed room as
// soon as the first snapshot exists as its only room, and releases the
// lease when the reader leaves.
func TestStreamTelemetryCarriesManagedPresence(t *testing.T) {
	w := newPresenceWorld(t)
	src := fakeTelemetrySource(t)
	member := managedchat.Member{ID: managedchat.NewID(), Name: "api"}
	w.r.Server = managedchat.Box{Name: "worker"}
	w.r.Members = []managedchat.Member{member}
	w.applied = managedchat.Applied{Accounts: map[string]managedchat.Connections{accountOf(t, member.ID): {Connections: 1}}}
	w.verified = func(string) string { return "bbbb" } // the fake source's worker container
	src.presence = w.worker()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rec := &frameRecorder{limit: 400, cancel: cancel}
	if err := streamTelemetry(ctx, rec, src, time.Millisecond); err != nil {
		t.Fatalf("stream ended with %v", err)
	}
	dec := spacetelemetry.NewDecoder(&rec.buf)
	var rooms [][]spacetelemetry.Room
	for {
		f, err := dec.Decode()
		if err != nil {
			break
		}
		if f.Kind == spacetelemetry.KindInventory {
			rooms = append(rooms, f.Inventory.Rooms)
		}
	}
	if len(rooms) < 2 || len(rooms[0]) != 1 || rooms[0][0].Error != "waiting for managed server status" {
		t.Fatalf("inventories = %+v", rooms)
	}
	last := rooms[len(rooms)-1]
	if len(last) != 1 || !last[0].Managed || last[0].Error != "" || !reflect.DeepEqual(last[0].Members, []string{"api"}) {
		t.Fatalf("managed room after the first snapshot = %+v", last)
	}
	other := w.worker()
	defer other.release()
	rel, err := presenceRel(w.r.ID)
	if err != nil || !other.acquire(rel) {
		t.Fatalf("the ended stream kept its lease: %v", err)
	}
}

// The cache and lease belong to the controller's identity. A snapshot or
// lease owned by anyone else, such as a native sandbox UID, is refused, and
// a server container that differs from the peer-verified one publishes
// nothing.
func TestChatPresenceRefusesForeignOwnershipAndUnverifiedServer(t *testing.T) {
	w := newPresenceWorld(t)
	p := w.worker()
	defer p.release()
	w.room(p)
	p.refresh(t.Context())
	foreign := w.worker()
	foreign.uid = os.Geteuid() + 1000
	if _, err := foreign.read(w.r.ID); err == nil || !strings.Contains(err.Error(), "not private") {
		t.Fatalf("a snapshot owned by another identity was accepted: %v", err)
	}
	p.release()
	rel, err := presenceRel(w.r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if foreign.acquire(rel) {
		t.Fatal("a lease path owned by another identity was used")
	}
	if !p.acquire(rel) {
		t.Fatal("the owner could not reacquire its lease")
	}
	if err := os.Chmod(filepath.Join(w.root, rel, "lease"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.release()
	if p.acquire(rel) {
		t.Fatal("a non-private lease file was used")
	}
	if err := os.Chmod(filepath.Join(w.root, rel, "lease"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := w.statuses.Load()
	w.verified = func(string) string { return "c-other" }
	w.clock.Add(int64(time.Second))
	p.refresh(t.Context())
	snap, err := p.read(w.r.ID)
	if w.statuses.Load() != before+1 || err != nil || snap.ObservedNS == w.clock.Load() {
		t.Fatalf("a snapshot was published for an unverified server container: %+v %v", snap, err)
	}
}

// A holder whose own stream stopped delivering inventories gives the lease
// up instead of starving every other view.
func TestChatPresenceStalledHolderReleasesTheLease(t *testing.T) {
	w := newPresenceWorld(t)
	a, b := w.worker(), w.worker()
	defer a.release()
	defer b.release()
	w.room(a)
	w.room(b)
	a.refresh(t.Context())
	statuses := w.statuses.Load()
	b.refresh(t.Context())
	if w.statuses.Load() != statuses {
		t.Fatal("the lease was not held")
	}
	w.clock.Add(int64(presenceInventoryStale) + 1)
	w.room(b) // b keeps receiving inventories; a does not
	a.refresh(t.Context())
	if w.statuses.Load() != statuses {
		t.Fatal("a stalled holder refreshed from its old inventory")
	}
	b.refresh(t.Context())
	if w.statuses.Load() != statuses+1 {
		t.Fatal("no takeover from a stalled holder")
	}
}
