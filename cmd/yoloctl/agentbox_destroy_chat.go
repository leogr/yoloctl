package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// Seams for destroy's membership cleanup regressions.
var (
	destroyChatApplyFn  = applyManagedMembership
	revokeDestroyedChat = revokeDestroyedChatMember
)

// activeChatMember is the one unrevoked membership a box name holds in the
// managed room, if any.
func activeChatMember(r *managedchat.Room, name string) (managedchat.Member, bool) {
	for _, m := range r.Members {
		if !m.Revoked && m.Name == name {
			return m, true
		}
	}
	return managedchat.Member{}, false
}

// revokeDestroyedChatMember runs after `box destroy` removed the declaration,
// under the native space lock the destroy holds. It revokes the destroyed
// box's own membership (same name AND immutable box ID) with the chat revoke
// primitives in their order: durable desired state, then runtime application.
// Role publication follows when the destroy releases its lock. This cleanup
// never fails the destroy: each outcome is a notice or a warning naming the
// explicit command that finishes it. The release's role publication is
// unchanged, so an unreadable chat store or inventory still fails the
// command there, as it does for every box lifecycle command.
func revokeDestroyedChatMember(ctx context.Context, space, name, boxID string, notice io.Writer) {
	retry := fmt.Sprintf("run yoloctl chat revoke %s --space %s", name, space)
	warn := func(format string, args ...any) {
		cliWarn(logging.Chat, "box %s's chat membership "+format, append([]any{name}, args...)...)
	}
	store, err := nativeChatStoreFn()
	if err != nil {
		warn("could not be checked (%v); if it is still listed in chat status, %s", err, retry)
		return
	}
	r, err := store.Load()
	if errors.Is(err, fs.ErrNotExist) {
		return // no managed room: nothing to revoke
	}
	if err != nil {
		warn("could not be checked (%v); if it is still listed in chat status, %s", err, retry)
		return
	}
	member, ok := activeChatMember(r, name)
	switch {
	case !ok:
		return
	case boxID == "":
		warn("(member %s) was kept: the destroyed box's identity is unknown (its declaration was unreadable); if it is stale, %s", member.ID, retry)
		return
	case member.BoxID != boxID:
		warn("(member %s) belongs to another box identity and was kept; if it is stale, %s", member.ID, retry)
		return
	case !r.RuntimeEnabled || !r.Prepared:
		warn("(member %s) is still active because managed chat is not applied; run yoloctl chat ensure, then %s", member.ID, retry)
		return
	}
	boxes, err := newBoxStateResolver().ResolveSpace(ctx, space, boxstate.RuntimeInventory)
	if err != nil {
		warn("(member %s) is still active (%v); %s", member.ID, err, retry)
		return
	}
	inventory, err := chatInventory(boxes)
	if err != nil {
		warn("(member %s) is still active (%v); %s", member.ID, err, retry)
		return
	}
	r, err = store.Revoke(member.ID, r.Generation, inventory)
	if err != nil {
		warn("(member %s) is still active (%v); %s", member.ID, err, retry)
		return
	}
	proof, err := store.Render(r)
	if err == nil {
		err = destroyChatApplyFn(ctx, r, proof, boxes)
	}
	if err != nil {
		warn("(member %s) is revoked, but the chat server has not applied it yet (%v); run yoloctl chat ensure --space %s", member.ID, err, space)
		return
	}
	cliNotice(logging.Chat, "revoked box %s's chat membership (member %s); the name can be enrolled again", name, member.ID)
}
