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

	tests := []struct {
		name      string
		actor     string
		held      []string
		target    string
		requested []string
		want      error
	}{
		{"owner adds a subuser", me, []string{"*"}, "", []string{"file.read", "control.start"}, nil},
		{"owner changes another subuser", me, []string{"*"}, other, []string{"file.read"}, nil},
		{"owner removes another subuser", me, []string{"*"}, other, nil, nil},
		{"a manager hands on what they hold", me, manager, other, []string{"file.read"}, nil},
		{"a manager hands on everything they hold", me, manager, "", manager, nil},
		{"a manager adds a subuser with no permissions", me, manager, "", nil, nil},

		{"nobody edits themselves", me, []string{"*"}, me, []string{"file.read"}, ErrSelfEdit},
		{"nobody removes themselves", me, []string{"*"}, me, nil, ErrSelfEdit},
		{"another user whose id looks alike", me, []string{"*"}, "11111111-1111-4111-8111-11111111111A", nil, nil},
		{"the same user in another case", "AAAAAAAA-1111-4111-8111-111111111111", []string{"*"}, "aaaaaaaa-1111-4111-8111-111111111111", nil, ErrSelfEdit},
		{"self-edit is refused before permissions are looked at", me, manager, me, []string{"backup.delete"}, ErrSelfEdit},

		{"more than held", me, manager, other, []string{"file.read", "backup.delete"}, ErrCannotAssign},
		{"more than held, on adding", me, manager, "", []string{"control.stop"}, ErrCannotAssign},
		{"a name that does not exist", me, manager, other, []string{"root.access"}, ErrCannotAssign},
		{"the wildcard is not handed on", me, manager, other, []string{"*"}, ErrCannotAssign},
		{"an admin permission is not handed on", me, manager, other, []string{"admin.websocket.errors"}, ErrCannotAssign},
		{"nothing held", me, nil, "", []string{"file.read"}, ErrCannotAssign},

		// Without a target (adding) an empty actor id is not a self-edit.
		{"adding with an unknown actor id", "", []string{"*"}, "", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AuthorizeSubuserChange(tt.actor, tt.held, tt.target, tt.requested)
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
	// The frontend shows these words to the user, so they are the reference's.
	for err, want := range map[error]string{
		ErrCannotAssign:   "Cannot assign permissions to a subuser that your account does not actively possess.",
		ErrSubuserIsOwner: "You cannot add the server owner as a subuser for this server.",
		ErrSubuserExists:  "A user with that email address is already assigned as a subuser for this server.",
	} {
		if err.Error() != want {
			t.Errorf("%q, want %q", err.Error(), want)
		}
	}
}
