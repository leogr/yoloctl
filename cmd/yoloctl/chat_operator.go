package main

import (
	"context"
	"errors"
	"os"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

type controllerPrincipalKey struct{}

// Only the worker installs the listener's trusted principal. Neither a typed
// payload nor frontend routing metadata can select an operator identity.
func withControllerPrincipal(ctx context.Context, p controller.Principal) context.Context {
	return context.WithValue(ctx, controllerPrincipalKey{}, p)
}

func currentControllerPrincipal(ctx context.Context) controller.Principal {
	if ctx == nil {
		return controller.Principal{}
	}
	p, _ := ctx.Value(controllerPrincipalKey{}).(controller.Principal)
	return p
}

// Keep the frontend's discoverable surface explicit. The server operation
// registry and live principal checks remain the authorization boundary.
func operatorChatMethod(method string) bool {
	switch method {
	case "chat.ensure", "chat.status", "chat.enroll", "chat.revoke", "chat.connect", "chat.disconnect",
		"chat.channel.list", "chat.channel.catalog", "chat.channel.create", "chat.channel.add", "chat.channel.remove", "chat.channel.delete":
		return true
	default:
		return false
	}
}

// Called under the native space lock, before enrollment can publish a box
// identity, change membership or recreate a participant. The listener rechecks
// the grant throughout the call; this check also covers time spent waiting for
// the native lock.
func checkOperatorChatRuntime(ctx context.Context, method string, input chatRuntimeRequest, store managedchat.Store) error {
	p := currentControllerPrincipal(ctx)
	if p.Admin() {
		return nil
	}
	if err := checkOperatorPrincipal(ctx, p); err != nil {
		return err
	}
	r, err := store.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return validateOperatorChatRuntime(p, method, input, r)
}

func validateOperatorChatRuntime(p controller.Principal, method string, input chatRuntimeRequest, r *managedchat.Room) error {
	if p.Admin() {
		return nil
	}
	if p.Incarnation == "" || !operatorChatMethod(method) {
		return errors.New("this managed chat operation requires the human host endpoint")
	}
	if method == "chat.ensure" && r == nil && input.Human == "" {
		return errors.New("first room creation from an operator needs --human NAME; the box username is not the human identity")
	}
	if !input.Recreate {
		return nil
	}
	self := method == "chat.enroll" && input.Box == p.Box
	if method == "chat.revoke" {
		// The argument is a box name or a member ID; either may be our own.
		// A member-ID-shaped argument is always an ID (RevokeTarget), so
		// only another argument can be our name, even with no room yet.
		self = layout.ValidateIdentity(input.MemberID) != nil && input.MemberID == p.Box
		if r != nil {
			if member, err := r.RevokeTarget(input.MemberID); err == nil && member.Name == p.Box {
				self = true
			}
		}
	}
	if self {
		return errors.New("cannot recreate the calling operator during chat administration; omit --recreate to save membership and have another operator explicitly recreate this box after the reply")
	}
	return nil
}
