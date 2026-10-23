// Package account holds the rules of what a signed-in user does to their own account: changing the e-mail address or the
// password, and switching two-factor authentication on and off. Nothing here touches storage; the handlers store what the
// plans say and carry out the effects.
package account

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/lockout"
	"github.com/Helles01/raptor-panel/internal/auth/login"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/auth/totp"
	"github.com/Helles01/raptor-panel/internal/fielderr"
	"github.com/Helles01/raptor-panel/internal/users"
)

// The refusals, with the reference's texts. All of them are answered with 400, except ErrEmailChanges (429).
var (
	ErrInvalidPassword   = errors.New("The password provided was invalid for this account.")                               //nolint:staticcheck // the reference's text
	ErrTwoFactorPassword = errors.New("The password provided was not valid.")                                              //nolint:staticcheck // the reference's text
	ErrTwoFactorEnabled  = errors.New("Two-factor authentication is already enabled on this account.")                     //nolint:staticcheck // the reference's text
	ErrTwoFactorToken    = errors.New("The provided two-factor authentication token was not valid.")                       //nolint:staticcheck // the reference's text
	ErrEmailChanges      = errors.New("Your email address has been changed too many times today. Please try again later.") //nolint:staticcheck // the reference's text
	// ErrNotSetUp is for a code sent before the secret was asked for.
	ErrNotSetUp = errors.New("Two-factor authentication has not been set up: ask for a secret first.")
)

const (
	// EmailChangeLimit is how many times an address may be changed in EmailChangeWindow. The reference does the same so that
	// nobody can find out which addresses are in use by changing their own again and again.
	EmailChangeLimit  = 3
	EmailChangeWindow = 24 * time.Hour

	codeLength = 6
)

// ConfirmCounters are the counters that limit wrong passwords in the requests that ask for the current password again. The
// reference does not limit them, so a stolen session or API key could be used to guess the password; here they count per user
// and client, per user, and per client, as login attempts do.
func ConfirmCounters(userID uint64, clientAddr string) ([]lockout.Counter, error) {
	client := lockout.Client(clientAddr, "")
	if client == "" || userID == 0 {
		return nil, login.ErrNoClient
	}
	id := fmt.Sprintf("id:%d", userID)
	return []lockout.Counter{
		{Key: "confirm|user-client|" + id + "|" + client, Limit: 5, Window: time.Minute},
		{Key: "confirm|user|" + id, Limit: 20, Window: 10 * time.Minute},
		{Key: "confirm|client|" + client, Limit: 40, Window: 10 * time.Minute},
	}, nil
}

// EmailChangeCounter counts the address changes of one user. It is hit only when the address really changes.
func EmailChangeCounter(userUUID string) lockout.Counter {
	return lockout.Counter{Key: "email-change|" + strings.ToLower(userUUID), Limit: EmailChangeLimit, Window: EmailChangeWindow}
}

// EmailPlan says what to do with a request to change the address.
type EmailPlan struct {
	// Change is false when the address stays the same (apart from its case): nothing is stored, counted or logged.
	Change bool
	Old    string
	New    string
}

// PlanEmailChange checks a new address. blocked says that the user has used up EmailChangeCounter; like the reference this is
// checked first, also for a request that would change nothing. The current password must have been confirmed before.
func PlanEmailChange(current, requested string, blocked bool) (EmailPlan, error) {
	if blocked {
		return EmailPlan{}, ErrEmailChanges
	}
	if err := users.ValidateEmail(requested); err != nil {
		return EmailPlan{}, err
	}
	return EmailPlan{Change: !strings.EqualFold(current, requested), Old: current, New: requested}, nil
}

// PasswordError is a new password that breaks rules. The API answers it with a 422 entry on "password" for each message.
type PasswordError struct{ Messages []string }

func (e *PasswordError) Error() string { return "password: " + strings.Join(e.Messages, " ") }

// PasswordPlan is a password change that is allowed.
type PasswordPlan struct {
	Hash string
	// RevokeOtherSessions ends the user's sessions but the one that made the request. DeauthorizeNodes has the nodes drop the user's
	// tokens and SFTP sessions. DeleteTokens removes the reset tokens that were asked for before. The reference only logs the
	// other devices out (C-049).
	RevokeOtherSessions bool
	DeauthorizeNodes    bool
	DeleteTokens        bool
}

// PlanPasswordChange checks a change of password. currentOK says whether the current password was right: that is checked
// before the new one, as in the reference, and a wrong one is ErrInvalidPassword.
func PlanPasswordChange(currentOK bool, password, confirmation string) (PasswordPlan, error) {
	if !currentOK {
		return PasswordPlan{}, ErrInvalidPassword
	}
	if msgs := passwordreset.CheckNewPassword(password, confirmation); len(msgs) > 0 {
		return PasswordPlan{}, &PasswordError{Messages: msgs}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return PasswordPlan{}, fmt.Errorf("hash password: %w", err)
	}
	fx := passwordreset.EffectsOf(false)
	return PasswordPlan{Hash: hash, RevokeOtherSessions: fx.RevokeSessions, DeauthorizeNodes: fx.DeauthorizeNodes, DeleteTokens: fx.DeleteTokens}, nil
}

// Setup is the secret a user is shown to put into an authenticator app.
type Setup struct {
	Secret string
	// URI is the otpauth:// address of the QR code.
	URI string
}

// PlanTwoFactorSetup makes a new secret. It is refused while two-factor authentication is on, so that the secret in use cannot
// be replaced by asking for another. issuer is the name of the panel. The secret is stored (sealed) as soon as it is made, and
// counts only once a code for it has been confirmed.
func PlanTwoFactorSetup(enabled bool, issuer, email string, r io.Reader) (Setup, error) {
	if enabled {
		return Setup{}, ErrTwoFactorEnabled
	}
	secret, err := totp.GenerateSecret(r)
	if err != nil {
		return Setup{}, fmt.Errorf("generate secret: %w", err)
	}
	return Setup{Secret: secret, URI: totp.ProvisioningURI(issuer, email, secret)}, nil
}

// Enable is what to store when two-factor authentication is switched on.
type Enable struct {
	// Tokens are the recovery tokens, to be shown once; Hashes are what is stored, in the same order.
	Tokens []string
	Hashes []string
	// UsedAt is when to say a code was last used (the start of the step that was confirmed), so that the code that switched it on
	// cannot be used again to sign in. The old recovery tokens are deleted.
	UsedAt time.Time
}

// PlanTwoFactorEnable checks the request to switch two-factor authentication on. passwordOK says that the password sent is the
// user's; code is the code from the app; secret is the stored secret (empty when none was asked for). The order is the
// reference's: the shape of the code, the password, then the code.
func PlanTwoFactorEnable(passwordOK bool, code, secret string, now time.Time, r io.Reader) (Enable, error) {
	if utf8.RuneCountInString(code) != codeLength {
		return Enable{}, fielderr.New("code", "must be %d characters", codeLength)
	}
	if !passwordOK {
		return Enable{}, ErrTwoFactorPassword
	}
	if secret == "" {
		return Enable{}, ErrNotSetUp
	}
	step, ok, err := totp.Verify(secret, code, now, totp.DefaultWindow, -1)
	if err != nil {
		return Enable{}, fmt.Errorf("verify code: %w", err)
	}
	if !ok {
		return Enable{}, ErrTwoFactorToken
	}

	tokens, err := login.NewRecoveryTokens(r)
	if err != nil {
		return Enable{}, err
	}
	hashes := make([]string, len(tokens))
	for i, t := range tokens {
		if hashes[i], err = auth.HashPassword(t); err != nil {
			return Enable{}, fmt.Errorf("hash recovery token: %w", err)
		}
	}
	return Enable{Tokens: tokens, Hashes: hashes, UsedAt: time.Unix(step*totp.Period, 0).UTC()}, nil
}

// Disable is what to do when two-factor authentication is switched off.
type Disable struct {
	// ClearSecret and DeleteRecoveryTokens remove what could sign in without the password's help. The reference leaves both in
	// place (C-049).
	ClearSecret          bool
	DeleteRecoveryTokens bool
}

// PlanTwoFactorDisable checks the request to switch two-factor authentication off.
func PlanTwoFactorDisable(passwordOK bool) (Disable, error) {
	if !passwordOK {
		return Disable{}, ErrTwoFactorPassword
	}
	return Disable{ClearSecret: true, DeleteRecoveryTokens: true}, nil
}
