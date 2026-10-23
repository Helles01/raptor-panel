package users

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Helles01/raptor-panel/internal/auth"
)

// ErrHasServers refuses to delete an account that still owns servers. The text is the reference's.
var ErrHasServers = errors.New("Cannot delete a user with active servers attached to their account. Please delete their servers before continuing.") //nolint:staticcheck // the reference's text

// CheckCanDelete refuses a user who owns servers, so that no server is left without an owner.
func CheckCanDelete(ownedServers int64) error {
	if ownedServers > 0 {
		return ErrHasServers
	}
	return nil
}

// Account is what is stored about a user, as far as an update needs to know.
type Account struct {
	ExternalID *string
	Email      string
	Username   string
	FirstName  string
	LastName   string
	Language   string
	Admin      bool
}

// Change is the body of PATCH /api/application/users/{id}. Like the reference's update request it needs the email,
// username and both names every time; the rest is optional. A nil field is left as it is, and a password that is
// blank is not changed.
type Change struct {
	ExternalID *string // a pointer to "" clears it
	Email      string
	Username   string
	FirstName  string
	LastName   string
	Password   string
	Language   *string
	Admin      *bool
}

// Update is the row to store after a Change. PasswordHash is empty when the password stays.
type Update struct {
	Account
	PasswordHash string
}

// Effects are what the caller must do besides storing an Update.
type Effects struct {
	// PasswordChanged: end the user's sessions, make the nodes forget the user's tokens and delete the pending reset
	// tokens, as after any password change (passwordreset.EffectsOf).
	PasswordChanged bool
	// EmailChanged: delete the reset tokens that were sent to the old address.
	EmailChanged bool
	// AdminRevoked: delete the user's application API keys, so that they do not work again if the user is made an
	// admin later.
	AdminRevoked bool
}

// PlanUpdate checks a Change against the stored account and returns what to store and what else to do. languages is
// the list of available languages. The problem found first is the error, a *fielderr.Error.
func PlanUpdate(cur Account, ch Change, languages []string) (Update, Effects, error) {
	if err := validateProfile(ch.Email, ch.Username, ch.FirstName, ch.LastName); err != nil {
		return Update{}, Effects{}, err
	}

	next := Account{
		ExternalID: cur.ExternalID,
		Email:      ch.Email,
		Username:   strings.ToLower(ch.Username),
		FirstName:  ch.FirstName,
		LastName:   ch.LastName,
		Language:   cur.Language,
		Admin:      cur.Admin,
	}
	if ch.ExternalID != nil {
		next.ExternalID = nil
		if *ch.ExternalID != "" {
			if err := ValidateExternalID(*ch.ExternalID); err != nil {
				return Update{}, Effects{}, err
			}
			next.ExternalID = ch.ExternalID
		}
	}
	if ch.Language != nil {
		if err := validateLanguage(*ch.Language, languages); err != nil {
			return Update{}, Effects{}, err
		}
		next.Language = *ch.Language
	}
	if ch.Admin != nil {
		next.Admin = *ch.Admin
	}

	up := Update{Account: next}
	if ch.Password != "" {
		if err := ValidatePassword(ch.Password); err != nil {
			return Update{}, Effects{}, err
		}
		hash, err := auth.HashPassword(ch.Password)
		if err != nil {
			return Update{}, Effects{}, fmt.Errorf("hash password: %w", err)
		}
		up.PasswordHash = hash
	}

	return up, Effects{
		PasswordChanged: ch.Password != "",
		EmailChanged:    !strings.EqualFold(cur.Email, next.Email),
		AdminRevoked:    cur.Admin && !next.Admin,
	}, nil
}
