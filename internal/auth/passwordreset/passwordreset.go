// Package passwordreset holds the rules of the "forgot password" flow that need no database or mail server: the token, when one is
// made and when it is good, the link in the e-mail, the new password, and what a reset sets in motion.
package passwordreset

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/auth"
)

const (
	// TokenLength is the length of a token in characters: 32 random bytes in hex.
	TokenLength = 64
	// TTL is how long a token can be used, as in the reference.
	TTL = time.Hour
	// ResendAfter is how soon after a token was made another one is made for the same user, as in the reference.
	ResendAfter = time.Minute

	maxClockSkew = 5 * time.Minute
)

// The limits of the endpoints, per client address and minute. The reference limits the request for a link to 2 and the other
// sign-in endpoints to 10, and leaves the endpoint that uses a token without a limit (C-038).
const (
	RequestPerIPPerMinute = 2
	ResetPerIPPerMinute   = 10
	// RequestPerEmailPerHour caps the e-mails one address can be sent, so that the form cannot be used to flood someone.
	RequestPerEmailPerHour = 5
)

// ErrInvalidToken is answered for a token that is unknown, wrong, expired or not for this address: one answer for all of them,
// so that nothing says which part was wrong.
var ErrInvalidToken = errors.New("This password reset token is invalid.") //nolint:staticcheck // the reference's text

// Token is a new token: Plain goes to the user in the e-mail and is never stored, Hash is what is stored.
type Token struct {
	Plain string
	Hash  string
}

// NewToken makes a token from the random source r (crypto/rand.Reader in production).
func NewToken(r io.Reader) (Token, error) {
	b := make([]byte, TokenLength/2)
	if _, err := io.ReadFull(r, b); err != nil {
		return Token{}, fmt.Errorf("generate reset token: %w", err)
	}
	plain := hex.EncodeToString(b)
	return Token{Plain: plain, Hash: hashToken(plain)}, nil
}

// The token is random and long, so a fast hash is enough; the reference stores a bcrypt hash, which makes every wrong guess
// cost a lot of time on the server, for nothing.
func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func wellFormed(token string) bool {
	if len(token) != TokenLength {
		return false
	}
	for _, c := range token {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Record is the stored token of an address.
type Record struct {
	Hash      string
	CreatedAt time.Time
}

// Redeem checks a token presented with an address. rec is the record stored for that address, nil if there is none. The token
// must have the right form, match, be at most an hour old, and not come from the future.
func Redeem(rec *Record, presented string, now time.Time) error {
	if rec == nil || !wellFormed(presented) {
		return ErrInvalidToken
	}
	if subtle.ConstantTimeCompare([]byte(rec.Hash), []byte(hashToken(presented))) != 1 {
		return ErrInvalidToken
	}
	age := now.Sub(rec.CreatedAt)
	if age > TTL || age < -maxClockSkew {
		return ErrInvalidToken
	}
	return nil
}

// Decision is what to do with a request for a reset link. Whatever it is, the answer to the person who asked is the same.
type Decision int

const (
	// Send: make a token, replace any older one of the address, and send the e-mail.
	Send Decision = iota
	// SkipUnknown: no such user. Nothing is sent; the attempt is logged.
	SkipUnknown
	// SkipRecent: a link was sent a moment ago. Nothing is sent.
	SkipRecent
)

// Decide applies the rules to a request. last is when the newest stored token of the user was made, nil if none.
func Decide(userExists bool, last *time.Time, now time.Time) Decision {
	switch {
	case !userExists:
		return SkipUnknown
	case last != nil && now.Sub(*last) < ResendAfter && now.Sub(*last) > -maxClockSkew:
		return SkipRecent
	}
	return Send
}

// Link is the address in the e-mail. It is built from the configured public address of the panel and nothing the request
// said, so a request with a made-up Host header cannot make an e-mail lead to another site. The reference builds it from the
// request when mail is sent at once, and from the configuration when it is queued (C-038).
func Link(appURL, token, email string) (string, error) {
	u, err := url.Parse(appURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", errors.New("the public address of the panel is not usable")
	}
	if !wellFormed(token) {
		return "", errors.New("the token is not well formed")
	}
	return u.Scheme + "://" + u.Host + "/auth/password/reset/" + token + "?email=" + url.QueryEscape(email), nil
}

// CheckNewPassword returns a message for each rule the new password breaks, with the reference's texts, in the order of its
// rules. It must be given twice, be at least 8 characters, and at most 72 bytes (the most a password hash can use, C-008).
func CheckNewPassword(password, confirmation string) []string {
	var msgs []string
	if password == "" {
		return []string{"The password field is required."}
	}
	if password != confirmation {
		msgs = append(msgs, "The password confirmation does not match.")
	}
	if utf8.RuneCountInString(password) < auth.MinPasswordLength {
		msgs = append(msgs, fmt.Sprintf("The password must be at least %d characters.", auth.MinPasswordLength))
	}
	if len(password) > auth.MaxPasswordBytes {
		msgs = append(msgs, fmt.Sprintf("The password may not be greater than %d bytes.", auth.MaxPasswordBytes))
	}
	return msgs
}

// Effects is what a successful reset sets in motion besides the new password.
type Effects struct {
	// RevokeSessions ends every session of the user, and DeauthorizeNodes has the nodes drop their tokens and SFTP sessions: whoever
	// knew the old password, or had a session, must not keep access (C-038; the reference leaves other sessions alone).
	RevokeSessions   bool
	DeauthorizeNodes bool
	// DeleteTokens removes every reset token of the user.
	DeleteTokens bool
	// LoginUser signs the user in at once. Not for a user with two-factor authentication, who must sign in the ordinary way
	// and enter a code.
	LoginUser bool
	// SendToLogin is what the answer says to the frontend: go to the sign-in page.
	SendToLogin bool
}

// EffectsOf says what a reset does for a user with or without two-factor authentication.
func EffectsOf(usesTOTP bool) Effects {
	return Effects{RevokeSessions: true, DeauthorizeNodes: true, DeleteTokens: true, LoginUser: !usesTOTP, SendToLogin: usesTOTP}
}
