package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/internal/sshtransport"
	"golang.org/x/crypto/ssh"
)

type chatRuntimeRequest struct {
	ServerBox    string `json:"server_box,omitempty"`
	Human        string `json:"human,omitempty"`
	DefaultHuman string `json:"default_human,omitempty"`
	Box          string `json:"box,omitempty"`
	MemberID     string `json:"member_id,omitempty"`
	Generation   uint64 `json:"generation,omitempty"`
	Recreate     bool   `json:"recreate,omitempty"`
	Nickname     string `json:"nickname,omitempty"`
}

func runControllerChatRuntime(ctx context.Context, req controller.Request, out, notice io.Writer) (_ bool, result error) {
	if req.Method != "chat.ensure" && req.Method != "chat.status" && req.Method != "chat.enroll" && req.Method != "chat.revoke" && req.Method != "chat.nickname" && req.Method != "chat.attach-target" {
		return false, nil
	}
	if len(req.Args) != 0 || len(req.Flags) != 0 || req.Terminal || req.View || req.Session != nil {
		return true, errors.New("managed chat runtime accepts only a typed payload")
	}
	var input chatRuntimeRequest
	if len(req.Payload) > 0 {
		if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &input, 4096); err != nil {
			return true, errors.New("invalid managed chat request")
		}
	}
	if req.Method == "chat.status" && input != (chatRuntimeRequest{}) {
		return true, errors.New("chat status takes no mutation fields")
	}
	if req.Method == "chat.attach-target" && (input != (chatRuntimeRequest{MemberID: input.MemberID}) || layout.ValidateIdentity(input.MemberID) != nil) {
		return true, errors.New("chat attach target requires only an immutable member ID")
	}
	if req.Method != "chat.nickname" && input.Nickname != "" {
		return true, errors.New("nickname changes require chat nickname")
	}
	if req.Method == "chat.nickname" && (input.ServerBox != "" || input.Human != "" || input.DefaultHuman != "" || input.Box != "" || input.MemberID != "" || input.Recreate || input.Nickname == "") {
		return true, errors.New("chat nickname accepts only nickname and desired generation")
	}
	store, err := nativeChatStoreFn()
	if err != nil {
		return true, err
	}
	if req.Method == "chat.status" {
		status, err := observeManagedChatRead(ctx, req.Space, store)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(status)
	}
	if req.Method == "chat.attach-target" {
		target, err := observeChatAttachTarget(ctx, req.Space, store, input.MemberID)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(target)
	}
	release, err := spacelock.Acquire(ctx, req.Space, true)
	if err != nil {
		return true, err
	}
	defer release()
	// A failed role or membership publication after a save withholds the
	// completion checkpoint, at every site below.
	publicationFailed := false
	defer completeBeforeUnlock(ctx, &result, &publicationFailed)
	publishRoles := func(boxes []boxstate.Box, notice io.Writer) error {
		err := synchronizeChatRolesFn(ctx, req.Space, store, boxes, notice)
		publicationFailed = publicationFailed || err != nil
		return err
	}
	if err := checkOperatorChatRuntime(ctx, req.Method, input, store); err != nil {
		return true, err
	}
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, req.Space, boxstate.RuntimeInventory)
	if err != nil {
		return true, err
	}
	inventory, err := chatInventory(boxes)
	if err != nil {
		return true, err
	}
	intent, err := chatRuntimeIntent(ctx, req.Space, req.Method, input, store)
	if err != nil {
		return true, err
	}
	if req.Method == "chat.ensure" && ctx.Value(warmKey{}) != nil {
		if input.Box != "" || input.MemberID != "" || input.Recreate || input.Generation != 0 {
			return true, errors.New("invalid chat ensure fields")
		}
		if err := warmChatServer(ctx, req.Space, store, input, boxes, inventory); err != nil {
			return true, err
		}
	}
	if input.Recreate && (req.Method == "chat.enroll" || req.Method == "chat.revoke") && ctx.Value(warmKey{}) != nil {
		r, err := store.Load()
		if err != nil {
			return true, err
		}
		if !r.RuntimeEnabled || !r.Prepared {
			return true, errors.New("run chat ensure before changing applied membership")
		}
		generation := input.Generation
		if generation == 0 {
			generation = r.Generation
		}
		if input.ServerBox != "" || input.Human != "" || input.DefaultHuman != "" {
			return true, errors.New("membership operation cannot replace room ownership")
		}
		var cfg *agentbox.Config
		if req.Method == "chat.enroll" {
			if input.Box == "" || input.MemberID != "" {
				return true, errors.New("chat enroll needs one box")
			}
			cfg, err = resolveChatEnrollment(req.Space, input.Box, r, generation, inventory)
		} else {
			if input.MemberID == "" || input.Box != "" {
				return true, errors.New("chat revoke requires one box name or member ID")
			}
			var member managedchat.Member
			member, err = r.RevokeTarget(input.MemberID)
			if err == nil {
				cfg, err = agentbox.Load(req.Space, member.Name)
			}
		}
		if err != nil {
			return true, err
		}
		if err := warmBoxBeforeEffects(defaultCreationContext(ctx, cfg), container.New(req.Space), cfg, true); err != nil {
			return true, err
		}
	}
	if err := controller.BeginScopedEffects(ctx, addDesiredImageIntent(ctx, intent)); err != nil {
		return true, err
	}
	if req.Method == "chat.ensure" {
		if input.Box != "" || input.MemberID != "" || input.Recreate || input.Generation != 0 {
			return true, errors.New("invalid chat ensure fields")
		}
		if _, err := ensureManagedRoom(store, input, inventory); err != nil {
			return true, err
		}
		if err := publishRoles(boxes, io.Discard); err != nil {
			return true, err
		}
		r, err := store.Load()
		if err != nil {
			return true, err
		}
		if err := ensureManagedServer(ctx, req.Space, store, r, boxes, notice); err != nil {
			return true, err
		}
	} else {
		if input.ServerBox != "" || input.Human != "" || input.DefaultHuman != "" {
			return true, errors.New("membership operation cannot replace room ownership")
		}
		r, err := store.Load()
		if err != nil {
			return true, err
		}
		if !r.RuntimeEnabled || !r.Prepared {
			return true, errors.New("run chat ensure before changing applied membership")
		}
		generation := input.Generation
		if generation == 0 {
			generation = r.Generation
		}
		var changed managedchat.Member
		switch req.Method {
		case "chat.nickname":
			r, err = store.SetHumanNickname(input.Nickname, generation, inventory)
			if err != nil {
				return true, err
			}
		case "chat.enroll":
			if input.Box == "" || input.MemberID != "" {
				return true, errors.New("chat enroll needs one box")
			}
			cfg, err := prepareChatEnrollment(req.Space, input.Box, r, generation, inventory)
			if err != nil {
				return true, err
			}
			inventory.Declarations[cfg.Name] = cfg.ID
			r, err = store.Enroll(managedchat.Box{Name: cfg.Name, ID: cfg.ID}, generation, inventory)
			if err != nil {
				return true, err
			}
			for _, m := range r.Members {
				if !m.Revoked && m.BoxID == cfg.ID && m.Name == cfg.Name {
					changed = m
				}
			}
		default:
			if input.MemberID == "" || input.Box != "" {
				return true, errors.New("chat revoke requires one box name or member ID")
			}
			// The native space lock is held, so this names the member Revoke
			// resolves again inside its mutation.
			changed, err = r.RevokeTarget(input.MemberID)
			if err != nil {
				return true, err
			}
			r, err = store.Revoke(input.MemberID, generation, inventory)
			if err != nil {
				return true, err
			}
		}
		// Desired state is durable first. A failure leaves explicit pending
		// application; retained recovery tokens never imply live enforcement.
		proof, err := store.Render(r)
		if err != nil {
			publicationFailed = true
			return true, err
		}
		if err := applyManagedMembership(ctx, r, proof, boxes); err != nil {
			publicationFailed = true
			return true, fmt.Errorf("desired membership saved; runtime application pending: %w", err)
		}
		if input.Recreate {
			if err := recreateChatMember(ctx, req.Space, changed, notice); err != nil {
				return true, err
			}
		}
	}
	boxes, err = newBoxStateResolver().ResolveSpace(ctx, req.Space, boxstate.RuntimeInventory)
	if err != nil {
		return true, err
	}
	if err := publishRoles(boxes, notice); err != nil {
		return true, err
	}
	status, err := observeManagedChat(ctx, store, boxes)
	if err != nil {
		return true, err
	}
	return true, json.NewEncoder(out).Encode(status)
}

// Resolve the same server/member names that the mutation will use while the
// native lock is held. Payload omissions and immutable IDs are not scopes.
func chatRuntimeIntent(ctx context.Context, space, method string, input chatRuntimeRequest, store managedchat.Store) (controller.EffectIntent, error) {
	current, err := store.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return globalIntent, nil
	}
	server := input.ServerBox
	if current != nil {
		server = current.Server.Name
	}
	if server == "" {
		server = managedchat.DefaultBox
	}
	var f footprint
	f.write(chatDomain, boxResource(server))
	switch method {
	case "chat.ensure":
		// A newly declared managed server is explicitly agentless; it never
		// selects the space's default account.
		boxLifecycleFootprint(ctx, space, server, boxRequest{account: account.None}, &f)
	case "chat.enroll":
		if validateAgentboxName(input.Box) != nil {
			return globalIntent, nil
		}
		f.write(boxResource(input.Box)) // enrollment persists the box identity
		if input.Recreate {
			boxLifecycleFootprint(ctx, space, input.Box, boxRequest{}, &f)
		}
	case "chat.revoke":
		if current == nil {
			return globalIntent, nil
		}
		member, err := current.RevokeTarget(input.MemberID)
		if err != nil {
			return controller.EffectIntent{}, err
		}
		if input.Recreate {
			boxLifecycleFootprint(ctx, space, member.Name, boxRequest{}, &f)
		}
	}
	return f.intent(""), nil
}

func ensureManagedRoom(store managedchat.Store, input chatRuntimeRequest, inventory managedchat.Inventory) (*managedchat.Room, error) {
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	r, err := store.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	server, human := input.ServerBox, input.Human
	if r != nil {
		if server == "" {
			server = r.Server.Name
		}
		if human == "" {
			human = r.Human.Name
		}
		if server != r.Server.Name || human != r.Human.Name {
			return nil, errors.New("managed chat already has a different room owner configuration")
		}
		if id, ok := inventory.Declarations[server]; ok && id != r.Server.ID {
			return nil, errors.New("managed chat server name belongs to an unrelated declaration")
		}
	} else if human == "" {
		human = input.DefaultHuman
	}
	if human == "" {
		return nil, errors.New("first room creation needs --human NAME")
	}
	if r == nil || !r.Prepared {
		r, err = store.Ensure(server, human, inventory)
		if err != nil {
			return nil, err
		}
	}
	if err := store.Verify(r); err != nil {
		return nil, err
	}
	if !r.RuntimeEnabled {
		if inventory.Runtime[r.Server.Name] {
			return nil, errors.New("cannot adopt a pre-existing server container")
		}
		r, err = store.EnableRuntime(r.Generation, inventory)
	}
	return r, err
}

func managedServerArgv() []string {
	// SQLite/WAL/artifact files must also be private for native backup. This
	// fixed, non-secret wrapper runs inside the ordinary confined session.
	return agentbox.ChatServerCommand()
}

func validateManagedServer(cfg *agentbox.Config, r *managedchat.Room) error {
	return agentbox.ValidateChatServer(cfg, r.Server)
}

func ensureManagedServer(ctx context.Context, space string, store managedchat.Store, r *managedchat.Room, boxes []boxstate.Box, notice io.Writer) error {
	var current *boxstate.Box
	for i := range boxes {
		if boxes[i].Name == r.Server.Name {
			current = &boxes[i]
			break
		}
	}
	var cfg *agentbox.Config
	if current != nil && current.Declaration == boxstate.DeclarationDeclared {
		cfg = current.Config
	}
	if cfg == nil && current != nil && current.Container != nil {
		return errors.New("managed chat cannot adopt an undeclared server container")
	}
	if cfg != nil {
		if err := validateManagedServer(cfg, r); err != nil {
			return err
		}
	}
	if cfg != nil && current.Container != nil {
		details, err := container.New(space).Inspect(ctx, cfg.Name)
		if err != nil {
			return err
		}
		if err := agentbox.VerifyChatRuntime(cfg, details); err != nil {
			return err
		}
	}
	proof, err := store.Render(r)
	if err != nil {
		return err
	}
	if cfg == nil {
		unlock, err := agentbox.LockConfig(space, r.Server.Name)
		if err != nil {
			return err
		}
		defer unlock()
		cfg = &agentbox.Config{Name: r.Server.Name, ID: r.Server.ID, Space: space, Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir(r.Server.Name), Features: []string{"yolo.chat.skill"}, SessionCommand: managedServerArgv()}
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	c := newChatRuntimeClient(space, notice)
	var prepared recordedNativeStart
	defer func() { closeNativeStart(prepared) }()
	if err := preflightAgentboxRuntimeQualified(ctx, c, cfg, true, func(check context.Context, declared *agentbox.Config) (bool, error) {
		return qualifyNativeRuntimePreparation(check, c, declared, &prepared)
	}); err != nil {
		return err
	}
	actual, err := (agentbox.LiveInspector{Client: c}).Inspect(ctx, cfg)
	if err != nil {
		return err
	}
	policy, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		return err
	}
	image, _, err := agentbox.ResolvedImage(cfg)
	if err != nil {
		return err
	}
	plan := agentbox.Diff(cfg, actual, image, policy)
	if err := managedServerPlanError(plan, cfg.Name, space); err != nil {
		return err
	}
	if plan.NeedsCreate {
		if err := newManagedLiveApplier(c, notice).Create(ctx, cfg, policy); err != nil {
			return err
		}
	}
	details, err := c.Inspect(ctx, cfg.Name)
	if err != nil {
		return err
	}
	if !details.Running {
		if err := startExistingAgentboxContainerPrepared(ctx, c, cfg, prepared); err != nil {
			return err
		}
	}
	var capabilities bytes.Buffer
	if err := runManagedChatExec(ctx, cfg, []string{"/usr/local/bin/aircd", "features"}, nil, &boundedChatWriter{Out: &capabilities, Remaining: managedchat.ControlLimit}, notice); err != nil {
		return fmt.Errorf("managed server image lacks required AIRC interfaces: %w", err)
	}
	var features managedchat.Features
	if err := json.Unmarshal(capabilities.Bytes(), &features); err != nil {
		return errors.New("invalid AIRC managed feature probe")
	}
	if err := features.Require("unix-listeners", "unix-artifacts", "control-reload", "acl-revoke", "multiline", "server-presence"); err != nil {
		return err
	}
	if r.Schema == 2 {
		if err := features.Require("channel-add-reload"); err != nil {
			return err
		}
	}
	if err := runManagedChatExec(ctx, cfg, []string{"/usr/local/bin/aircd", "check", "--config", managedchat.ServerConfigMount + "/server.toml"}, nil, io.Discard, notice); err != nil {
		return err
	}
	updated := false
	if _, err := ensureAgentSession(ctx, c, space, cfg.Name, notice, true, &updated, sessionStartOptions{}); err != nil {
		return err
	}
	control, _, err := managedServerControl(ctx, r, cfg)
	if err != nil {
		return err
	}
	status, err := waitManagedServer(ctx, control)
	if err != nil {
		return err
	}
	if err := status.Matches(proof); err == nil {
		return nil
	}
	if _, err := control.Call(ctx, "reload"); err != nil {
		return err
	}
	status, err = control.Call(ctx, "status")
	if err != nil {
		return err
	}
	return status.Matches(proof)
}

func prepareChatEnrollment(space, name string, r *managedchat.Room, generation uint64, inventory managedchat.Inventory) (*agentbox.Config, error) {
	unlock, err := agentbox.LockConfig(space, name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, err := resolveChatEnrollment(space, name, r, generation, inventory)
	if err != nil {
		return nil, err
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func resolveChatEnrollment(space, name string, r *managedchat.Room, generation uint64, inventory managedchat.Inventory) (*agentbox.Config, error) {
	if generation != r.Generation || name == r.Server.Name || name == r.Human.Name {
		return nil, errors.New("chat enrollment generation or identity precondition changed")
	}
	id, ok := inventory.Declarations[name]
	if !ok {
		return nil, errors.New("chat enrollment needs an existing declaration")
	}
	for _, m := range r.Members {
		if !m.Revoked && m.Name == name && m.BoxID != id {
			return nil, errors.New("revoke the previous immutable member before enrolling a reused name")
		}
	}
	cfg, err := agentbox.Load(space, name)
	if err != nil {
		return nil, err
	}
	if cfg.ID != id {
		return nil, errors.New("enrollment declaration changed after inventory")
	}
	if cfg.ID == "" {
		cfg.ID = agentbox.NewIdentity()
	}
	if !slices.Contains(cfg.Features, "yolo.chat.skill") {
		cfg.Features = append(cfg.Features, "yolo.chat.skill")
	}
	return cfg, nil
}

// recreateChatMember runs under the caller's native space lock and takes the
// member's box lock, so a replacement settles its conversation record.
func recreateChatMember(ctx context.Context, space string, m managedchat.Member, notice io.Writer) error {
	if m.ID == "" {
		return errors.New("missing immutable member for recreation")
	}
	lock, err := lockSessionBoxFn(space, m.Name)
	if err != nil {
		return err
	}
	defer lock.Release()
	cfg, err := agentbox.Load(space, m.Name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if cfg.ID != m.BoxID {
		return errors.New("member box name was reused; refusing to recreate its replacement")
	}
	_, err = reconcileManaged(agentbox.WithChatRecreate(ctx), lock, newChatRuntimeClient(space, notice), cfg, notice)
	return err
}

// managedServerPlanError refuses drift of an existing server, including its
// temporary-directory deployment drift: ensure never restarts or recreates it
// and never reports it repaired. The refusal names the exact commands for
// the server box, as the stale-image refusal does for other boxes.
func managedServerPlanError(plan agentbox.DiffPlan, name, space string) error {
	if !plan.NeedsRecreate && !plan.NeedsRestart {
		return nil
	}
	reason := plan.RecreateReason
	if reason == "" {
		reason = plan.RestartReason
	}
	return fmt.Errorf("the managed chat server box %q runtime changed (%s); the server was not recreated. Inspect with yoloctl box diff %s --space %s, then explicitly use yoloctl box recreate %s --space %s", name, reason, name, space, name, space)
}

func managedServerControl(ctx context.Context, r *managedchat.Room, cfg *agentbox.Config) (managedchat.Control, *container.Details, error) {
	var control managedchat.Control
	if err := validateManagedServer(cfg, r); err != nil {
		return control, nil, err
	}
	details, err := container.New(cfg.Space).Inspect(ctx, cfg.Name)
	if err != nil {
		return control, nil, err
	}
	if !details.Running {
		return control, details, errors.New("managed server container is stopped")
	}
	if err := agentbox.VerifyChatRuntime(cfg, details); err != nil {
		return control, details, err
	}
	dir, _ := layout.ChatControlDir(r.ID)
	native := spaceenv.Current()
	control = managedchat.Control{Socket: dir + "/control.sock", VerifyPeer: func(conn *net.UnixConn) error { return verifyChatControlPeer(conn, details.ID, native.UID, native.GID) }}
	return control, details, nil
}

func applyManagedMembership(ctx context.Context, r *managedchat.Room, want managedchat.Rendered, boxes []boxstate.Box) error {
	for _, box := range boxes {
		if box.Name == r.Server.Name {
			control, _, err := managedServerControl(ctx, r, box.Config)
			if err != nil {
				return err
			}
			if _, err := control.Call(ctx, "reload"); err != nil {
				return err
			}
			status, err := control.Call(ctx, "status")
			if err != nil {
				return err
			}
			return status.Matches(want)
		}
	}
	return errors.New("managed server is absent; run chat ensure to apply the pending generation")
}

func waitManagedServer(ctx context.Context, control managedchat.Control) (managedchat.Applied, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		status, err := control.Call(ctx, "status")
		if err == nil {
			return status, nil
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return status, err
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type boundedChatWriter struct {
	Out       io.Writer
	Remaining int
}

func (w *boundedChatWriter) Write(p []byte) (int, error) {
	if len(p) > w.Remaining {
		return 0, errors.New("managed chat response exceeds its bound")
	}
	n, err := w.Out.Write(p)
	w.Remaining -= n
	return n, err
}

func runManagedChatExec(ctx context.Context, cfg *agentbox.Config, argv []string, in *os.File, out, notice io.Writer) error {
	if !spaceenv.Native() {
		return errors.New("managed chat requires the native controller")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sandbox.SocketHostPath(cfg.Name))
	if err != nil {
		return err
	}
	if in == nil {
		in, err = os.Open(os.DevNull)
		if err != nil {
			_ = conn.Close()
			return err
		}
		defer func() { _ = in.Close() }()
	}
	return sshtransport.Run(ctx, conn, &ssh.ClientConfig{User: "sandbox", HostKeyCallback: ssh.InsecureIgnoreHostKey()}, remoteCommandString(argv), false, in, out, notice)
}

// newChatRuntimeClient keeps lifecycle output separate from the structured
// controller response. The worker's stdout is reserved for one Status value.
func newChatRuntimeClient(space string, notice io.Writer) *container.Client {
	return container.NewWithOutput(space, notice, notice)
}

// Resolve the server's image before the room/publication gate. This does not
// reserve a room identity, write a token, save a box or adopt a container.
func warmChatServer(ctx context.Context, space string, store managedchat.Store, input chatRuntimeRequest, boxes []boxstate.Box, inventory managedchat.Inventory) error {
	if err := inventory.Validate(); err != nil {
		return err
	}
	room, err := store.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	server, human := input.ServerBox, input.Human
	if room != nil {
		if server == "" {
			server = room.Server.Name
		}
		if human == "" {
			human = room.Human.Name
		}
		if server != room.Server.Name || human != room.Human.Name {
			return errors.New("managed chat already has a different room owner configuration")
		}
	} else {
		if server == "" {
			server = managedchat.DefaultBox
		}
		if human == "" {
			human = input.DefaultHuman
		}
		if human == "" {
			return errors.New("first room creation needs --human NAME")
		}
		if _, exists := inventory.Declarations[server]; exists || inventory.Runtime[server] {
			return errors.New("managed chat server name conflicts with an existing box")
		}
	}
	for _, box := range boxes {
		if box.Name == server && box.Config != nil {
			if room != nil {
				if err := validateManagedServer(box.Config, room); err != nil {
					return err
				}
			}
			return warmBoxBeforeEffects(defaultCreationContext(ctx, box.Config), container.New(space), box.Config, true)
		}
	}
	cfg := &agentbox.Config{Name: server, Space: space, Agent: "none", Account: "none", Image: defaultAgentboxImage, Workdir: agentbox.NewEphemeralWorkdir(server), Features: []string{"yolo.chat.skill"}, SessionCommand: managedServerArgv()}
	if room != nil {
		cfg.ID = room.Server.ID
	}
	return warmBoxBeforeEffects(defaultCreationContext(ctx, cfg), container.New(space), cfg, false)
}
