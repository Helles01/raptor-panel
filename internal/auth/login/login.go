// Package login holds the rules of signing in that need no database: which account a login names, the counters that limit
// guessing, what each factor decides, and the recovery tokens.
package login

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/auth/lockout"
	"github.com/Helles01/raptor-panel/internal/ids"
)

// The answers to a failed step. Each is a 400 with this text; the texts are the reference's.
var (
	ErrFailed           = errors.New("No account matching those credentials could be found.")                                 //nolint:staticcheck // the reference's text
	ErrCheckpointFailed = errors.New("The two-factor authentication token was invalid.")                                      //nolint:staticcheck // the reference's text
	ErrRecoveryFailed   = errors.New("The recovery token provided is not valid.")                                             //nolint:staticcheck // the reference's text
	ErrTokenExpired     = errors.New("The authentication token provided has expired, please refresh the page and try again.") //nolint:staticcheck // the reference's text
	// ErrNoSecondFactor is a 422: neither a code nor a recovery token was sent.
	ErrNoSecondFactor = errors.New("The authentication code field is required.") //nolint:staticcheck // the reference's text
)

const (
	maxIdentifierLength = 191

	// RecoveryTokenCount and RecoveryTokenLength are the recovery tokens made when two-factor authentication is switched on.
	RecoveryTokenCount  = 10
	RecoveryTokenLength = 10
)

var (
	codePattern     = regexp.MustCompile(`^[0-9]{6}$`)
	recoveryPattern = regexp.MustCompile(`^[A-Za-z0-9]{10}$`)
)

// Field says what a login name is: an e-mail address if it has an @, otherwise a user name.
func Field(input string) string {
	if strings.Contains(input, "@") {
		return "email"
	}
	return "username"
}

// Identifier cleans the login name the way the framework does (white space at both ends is removed) and checks that it can be
// one: not empty, not long, no control characters. The second result is false for anything else, which counts as a failed login
// without a look-up.
func Identifier(input string) (string, bool) {
	id := strings.Trim(input, " \t\n\r\x00\x0b")
	if id == "" || utf8.RuneCountInString(id) > maxIdentifierLength || !utf8.ValidString(id) || strings.ContainsFunc(id, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return "", false
	}
	return id, true
}

// ErrNoClient is returned when the address of the client cannot be read. A request like that must be refused: counting it
// together with all others would let one client use up everybody's allowance.
var ErrNoClient = errors.New("the address of the client cannot be read")

// Counters are the counters a login or second-factor attempt is checked against, and counted in when it fails. The reference
// counts per client address only (its key has an empty user part), so a client behind a shared address is held back by others
// and one account can be tried from many addresses; here an attempt counts per account and client, per account, and per client.
// account is the login name at the first step, and the user's id, as "id:7", at the second.
func Counters(account, clientAddr string) ([]lockout.Counter, error) {
	client := lockout.Client(clientAddr, "")
	if client == "" || account == "" {
		return nil, ErrNoClient
	}
	account = strings.ToLower(account)
	return []lockout.Counter{
		{Key: "login|account-client|" + account + "|" + client, Limit: 5, Window: time.Minute},
		{Key: "login|account|" + account, Limit: 20, Window: 10 * time.Minute},
		{Key: "login|client|" + client, Limit: 40, Window: 10 * time.Minute},
	}, nil
}

// First is what the first factor, the password, decides.
type First int

const (
	// Rejected: no such user or a wrong password. The same answer for both.
	Rejected First = iota
	// Done: the user is signed in.
	Done
	// NeedsSecondFactor: the password was right and the user has two-factor authentication.
	NeedsSecondFactor
)

// FirstFactor decides after the user was looked up and the password checked. A password is checked only for a user that was
// found, so burn says when the caller must spend the time of a check anyway (auth.VerifyUnknownUser), or the time taken shows
// which user names exist. The second factor is asked for only after the password, so that asking for it does not show that
// an account exists.
func FirstFactor(userFound, passwordOK, usesTOTP bool) (result First, burn bool) {
	switch {
	case !userFound:
		return Rejected, true
	case !passwordOK:
		return Rejected, false
	case usesTOTP:
		return NeedsSecondFactor, false
	}
	return Done, false
}

// Method is how a user proves the second factor.
type Method int

const (
	// Code is a code from the authenticator app.
	Code Method = iota
	// Recovery is one of the recovery tokens.
	Recovery
)

// SecondFactor reads what was sent for the second factor. A recovery token wins if both were sent, and then a wrong one does not
// fall back to the code. A value that has the wrong form is the same failure as a wrong one.
func SecondFactor(code, recovery string) (Method, string, error) {
	code = strings.TrimSpace(code)
	recovery = strings.TrimSpace(recovery)

	switch {
	case recovery != "":
		if !recoveryPattern.MatchString(recovery) {
			return Recovery, "", ErrRecoveryFailed
		}
		return Recovery, recovery, nil
	case code != "":
		if !codePattern.MatchString(code) {
			return Code, "", ErrCheckpointFailed
		}
		return Code, code, nil
	}
	return Code, "", ErrNoSecondFactor
}

// SecondFactorFailure is the error for a second factor that did not check out.
func SecondFactorFailure(m Method) error {
	if m == Recovery {
		return ErrRecoveryFailed
	}
	return ErrCheckpointFailed
}

// ConfirmationFailure is the error for a second-factor request whose pending login does not fit: expired or gone, or the token
// is wrong.
func ConfirmationFailure(pending bool) error {
	if !pending {
		return ErrTokenExpired
	}
	return ErrCheckpointFailed
}

// NewRecoveryTokens makes the recovery tokens of a user: ten of ten letters and digits, about 59 bits each. They are shown once
// and stored as password hashes, each good once.
func NewRecoveryTokens(r io.Reader) ([]string, error) {
	tokens := make([]string, RecoveryTokenCount)
	for i := range tokens {
		t, err := ids.RandomAlnum(r, RecoveryTokenLength)
		if err != nil {
			return nil, fmt.Errorf("generate recovery tokens: %w", err)
		}
		tokens[i] = t
	}
	return tokens, nil
}
