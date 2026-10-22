package servers

import (
	"errors"
	"strings"

	"github.com/Helles01/raptor-panel/internal/permissions"
)

// The refusals of the subuser rules. The texts that a user is shown are those of the reference panel.
var (
	// ErrSelfEdit is returned when someone tries to change or remove their own access to a server.
	ErrSelfEdit = errors.New("a user cannot change their own access to a server")
	// ErrCannotAssign is returned when someone asks to give a subuser permissions they do not hold themselves.
	ErrCannotAssign = errors.New("Cannot assign permissions to a subuser that your account does not actively possess.") //nolint:staticcheck // the reference's text
	// ErrSubuserIsOwner is returned when the owner of a server is added as its subuser.
	ErrSubuserIsOwner = errors.New("You cannot add the server owner as a subuser for this server.") //nolint:staticcheck // the reference's text
	// ErrSubuserExists is returned when a user is already a subuser of the server.
	ErrSubuserExists = errors.New("A user with that email address is already assigned as a subuser for this server.") //nolint:staticcheck // the reference's text
)

// AuthorizeSubuserChange decides whether the user behind a request may add, change or remove a subuser, once the right
// to use the endpoint (user.create, user.update, user.delete) is established.
//
// held is what the actor holds on the server: the wildcard for owners and administrators, otherwise the permission list of
// their own subuser entry. targetUUID is the user being changed, empty when a subuser is being added. requested is the
// permission list in the request, nil for a removal.
func AuthorizeSubuserChange(actorUUID string, held []string, targetUUID string, requested []string) error {
	// Nobody edits themselves: it would let a subuser widen or remove their own access.
	if targetUUID != "" && strings.EqualFold(actorUUID, targetUUID) {
		return ErrSelfEdit
	}
	if !permissions.CanAssign(held, requested) {
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
