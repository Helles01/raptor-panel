package servers

import (
	"errors"
	"strings"

	"github.com/Helles01/raptor-panel/internal/permissions"
)

// The refusals of the subuser rules. The texts that a user is shown are those of the reference panel, except
// ErrTargetOutranks, which is Raptor's own (C-025).
var (
	// ErrSelfEdit is returned when someone tries to change or remove their own access to a server.
	ErrSelfEdit = errors.New("a user cannot change their own access to a server")
	// ErrCannotAssign is returned when someone asks to give a subuser permissions they do not hold themselves.
	ErrCannotAssign = errors.New("Cannot assign permissions to a subuser that your account does not actively possess.") //nolint:staticcheck // the reference's text
	// ErrTargetOutranks is returned when someone tries to change or remove a subuser who holds permissions they do not hold.
	ErrTargetOutranks = errors.New("Cannot change a subuser who has permissions that your account does not actively possess.") //nolint:staticcheck // same style as the reference's text
	// ErrSubuserIsOwner is returned when the owner of a server is added as its subuser.
	ErrSubuserIsOwner = errors.New("You cannot add the server owner as a subuser for this server.") //nolint:staticcheck // the reference's text
	// ErrSubuserExists is returned when a user is already a subuser of the server.
	ErrSubuserExists = errors.New("A user with that email address is already assigned as a subuser for this server.") //nolint:staticcheck // the reference's text
)

// SubuserChange describes a request to add, change or remove a subuser, once the right to use the endpoint (user.create,
// user.update, user.delete) is established.
type SubuserChange struct {
	// ActorUUID is the user who makes the request.
	ActorUUID string
	// Held is what the actor holds on the server: the wildcard for owners and administrators, otherwise the permission
	// list of their own subuser entry.
	Held []string
	// TargetUUID is the subuser being changed or removed, empty when a subuser is being added.
	TargetUUID string
	// TargetHeld is the stored permission list of that subuser, nil when a subuser is being added.
	TargetHeld []string
	// Requested is the permission list in the request, nil for a removal.
	Requested []string
}

// AuthorizeSubuserChange decides whether the actor may make the change. Three rules, in this order:
//  1. Nobody changes or removes themselves, which would let a subuser widen or remove their own access.
//  2. Nobody changes or removes a subuser who holds more than they do (C-025). The reference only has rule 3.
//  3. A request may only hand on permissions the actor holds.
//
// Owners and administrators hold the wildcard and are bound only by rule 1.
func AuthorizeSubuserChange(c SubuserChange) error {
	if c.TargetUUID != "" && strings.EqualFold(c.ActorUUID, c.TargetUUID) {
		return ErrSelfEdit
	}
	// A stored permission the actor does not hold, even one that no longer exists, keeps them out: failing closed, and the
	// owner can always make the change. When adding there is no target and TargetHeld is empty, so this passes.
	if !permissions.CanAssign(c.Held, c.TargetHeld) {
		return ErrTargetOutranks
	}
	if !permissions.CanAssign(c.Held, c.Requested) {
		return ErrCannotAssign
	}
	return nil
}

// CheckNewSubuser applies the checks before a user is added as a subuser: they must not own the server and must not
// already be one of its subusers.
func CheckNewSubuser(ownerID, userID uint64, alreadySubuser bool) error {
	switch {
	case ownerID == userID:
		return ErrSubuserIsOwner
	case alreadySubuser:
		return ErrSubuserExists
	}
	return nil
}
