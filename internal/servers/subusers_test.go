package servers

import (
	"errors"
	"testing"
)

func TestAuthorizeSubuserChange(t *testing.T) {
	const (
		me    = "11111111-1111-4111-8111-111111111111"
		other = "22222222-2222-4222-8222-222222222222"
	)
	manager := []string{"user.create", "user.update", "user.delete", "file.read", "websocket.connect"}
	junior := []string{"file.read", "websocket.connect"}
	senior := append(append([]string(nil), manager...), "backup.delete")

	tests := []struct {
		name string
		c    SubuserChange
		want error
	}{
		{"owner adds a subuser", SubuserChange{ActorUUID: me, Held: []string{"*"}, Requested: []string{"file.read", "control.start"}}, nil},
		{"owner changes another subuser", SubuserChange{ActorUUID: me, Held: []string{"*"}, TargetUUID: other, TargetHeld: senior, Requested: []string{"file.read"}}, nil},
		{"owner removes another subuser", SubuserChange{ActorUUID: me, Held: []string{"*"}, TargetUUID: other, TargetHeld: senior}, nil},
		{"a manager hands on what they hold", SubuserChange{ActorUUID: me, Held: manager, Requested: []string{"file.read"}}, nil},
		{"a manager hands on everything they hold", SubuserChange{ActorUUID: me, Held: manager, Requested: manager}, nil},
		{"a manager adds a subuser with no permissions", SubuserChange{ActorUUID: me, Held: manager}, nil},

		// Whom a subuser may manage: only someone who holds no more than they do.
		{"a manager changes a junior", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: junior, Requested: []string{"file.read"}}, nil},
		{"a manager removes a junior", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: junior}, nil},
		{"a manager changes a peer with the same permissions", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: manager, Requested: junior}, nil},
		{"a manager changes someone who holds only the console permission", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: []string{"websocket.connect"}, Requested: junior}, nil},
		{"a manager changes someone with no stored permissions", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, Requested: junior}, nil},
		{"a manager may not change a senior", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: senior, Requested: junior}, ErrTargetOutranks},
		{"a manager may not remove a senior", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: senior}, ErrTargetOutranks},
		{"a stored permission that no longer exists keeps a subuser out", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: []string{"file.read", "legacy.thing"}}, ErrTargetOutranks},
		{"but not the owner", SubuserChange{ActorUUID: me, Held: []string{"*"}, TargetUUID: other, TargetHeld: []string{"file.read", "legacy.thing"}}, nil},

		{"nobody edits themselves", SubuserChange{ActorUUID: me, Held: []string{"*"}, TargetUUID: me, Requested: []string{"file.read"}}, ErrSelfEdit},
		{"nobody removes themselves", SubuserChange{ActorUUID: me, Held: []string{"*"}, TargetUUID: me}, ErrSelfEdit},
		{"another user whose id looks alike", SubuserChange{ActorUUID: me, Held: []string{"*"}, TargetUUID: "11111111-1111-4111-8111-11111111111A"}, nil},
		{"the same user in another case", SubuserChange{ActorUUID: "AAAAAAAA-1111-4111-8111-111111111111", Held: []string{"*"}, TargetUUID: "aaaaaaaa-1111-4111-8111-111111111111"}, ErrSelfEdit},
		{"self-edit is refused first", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: me, TargetHeld: senior, Requested: []string{"backup.delete"}}, ErrSelfEdit},

		{"more than held", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: junior, Requested: []string{"file.read", "backup.delete"}}, ErrCannotAssign},
		{"more than held, on adding", SubuserChange{ActorUUID: me, Held: manager, Requested: []string{"control.stop"}}, ErrCannotAssign},
		{"a name that does not exist", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: junior, Requested: []string{"root.access"}}, ErrCannotAssign},
		{"the wildcard is not handed on", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: junior, Requested: []string{"*"}}, ErrCannotAssign},
		{"an admin permission is not handed on", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: junior, Requested: []string{"admin.websocket.errors"}}, ErrCannotAssign},
		{"nothing held", SubuserChange{ActorUUID: me, Requested: []string{"file.read"}}, ErrCannotAssign},
		{"the target is checked before the request", SubuserChange{ActorUUID: me, Held: manager, TargetUUID: other, TargetHeld: senior, Requested: []string{"backup.delete"}}, ErrTargetOutranks},

		// Without a target (adding) an empty actor id is not a self-edit.
		{"adding with an unknown actor id", SubuserChange{Held: []string{"*"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AuthorizeSubuserChange(tt.c)
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCheckNewSubuser(t *testing.T) {
	tests := []struct {
		name           string
		owner, user    uint64
		alreadySubuser bool
		want           error
	}{
		{"a new subuser", 1, 2, false, nil},
		{"the owner", 1, 1, false, ErrSubuserIsOwner},
		{"already a subuser", 1, 2, true, ErrSubuserExists},
		{"the owner is named first", 1, 1, true, ErrSubuserIsOwner},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckNewSubuser(tt.owner, tt.user, tt.alreadySubuser); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSubuserErrorTexts(t *testing.T) {
	// The frontend shows these words to the user. All but ErrTargetOutranks are the reference's.
	for err, want := range map[error]string{
		ErrCannotAssign:   "Cannot assign permissions to a subuser that your account does not actively possess.",
		ErrTargetOutranks: "Cannot change a subuser who has permissions that your account does not actively possess.",
		ErrSubuserIsOwner: "You cannot add the server owner as a subuser for this server.",
		ErrSubuserExists:  "A user with that email address is already assigned as a subuser for this server.",
	} {
		if err.Error() != want {
			t.Errorf("%q, want %q", err.Error(), want)
		}
	}
}
