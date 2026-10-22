// Package auth holds the credential rules shared by the CLI and the HTTP API: password hashes and API keys.
package auth

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost = 10
	// MaxPasswordBytes is the longest password bcrypt can use. Longer ones are refused when a hash is made, because
	// the rest would be silently ignored.
	MaxPasswordBytes = 72
	// MinPasswordLength is the shortest password accepted for a new password (the reference panel's account rule).
	MinPasswordLength = 8
)

// ErrPasswordTooLong is returned for a password longer than MaxPasswordBytes.
var ErrPasswordTooLong = errors.New("password is longer than 72 bytes")

// HashPassword returns a bcrypt hash ($2a$) of password.
func HashPassword(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches a stored bcrypt hash.
//
// Hashes written by the reference panel (PHP) start with $2y$. That is the same algorithm as $2a$, so the prefix is
// normalised before verifying. PHP also ignores everything after the 72nd byte of a password, so passwords set there
// that are longer than that must be compared the same way, or those users could never log in.
func VerifyPassword(hash, password string) bool {
	if rest, ok := strings.CutPrefix(hash, "$2y$"); ok {
		hash = "$2a$" + rest
	}
	pw := []byte(password)
	if len(pw) > MaxPasswordBytes {
		pw = pw[:MaxPasswordBytes]
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), pw) == nil
}
