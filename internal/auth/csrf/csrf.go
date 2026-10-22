// Package csrf makes and checks anti-CSRF tokens that are bound to one session.
//
// A token is "<random>.<MAC>", where the MAC covers the random part and the session identifier. The page sets it in the
// XSRF-TOKEN cookie (which the frontend's HTTP client copies into the X-XSRF-TOKEN header) and the server checks that
// the header matches the session. A plain double-submit cookie is not enough: a sibling site or an attacker who can set
// a cookie could set both halves. A token that is bound to the secret session identifier cannot be made up by them.
package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const randomBytes = 16

// Protector makes and checks tokens with one key.
type Protector struct {
	key []byte
}

// New returns a Protector. The key must be at least 32 bytes; derive it from the secret key of the panel, not from
// anything a client can see.
func New(key []byte) (*Protector, error) {
	if len(key) < 32 {
		return nil, errors.New("csrf: the key must be at least 32 bytes")
	}
	return &Protector{key: append([]byte(nil), key...)}, nil
}

func (p *Protector) mac(sessionID string, random []byte) []byte {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte("raptor-csrf\x00"))
	m.Write([]byte(sessionID))
	m.Write([]byte{0})
	m.Write(random)
	return m.Sum(nil)
}

// Issue returns a token for a session.
func (p *Protector) Issue(sessionID string) (string, error) {
	if sessionID == "" {
		return "", errors.New("csrf: a token needs a session")
	}
	random := make([]byte, randomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate csrf token: %w", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(random) + "." + enc.EncodeToString(p.mac(sessionID, random)), nil
}

// Valid reports whether token was issued for this session.
func (p *Protector) Valid(sessionID, token string) bool {
	if sessionID == "" {
		return false
	}
	randomPart, macPart, found := strings.Cut(token, ".")
	if !found {
		return false
	}
	enc := base64.RawURLEncoding
	random, err := enc.DecodeString(randomPart)
	// The decoder skips line breaks, so only the canonical spelling of a token is accepted.
	if err != nil || len(random) != randomBytes || enc.EncodeToString(random) != randomPart {
		return false
	}
	got, err := enc.DecodeString(macPart)
	if err != nil || enc.EncodeToString(got) != macPart {
		return false
	}
	return hmac.Equal(got, p.mac(sessionID, random))
}
