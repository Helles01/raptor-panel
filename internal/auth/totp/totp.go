// Package totp implements time-based one-time passwords (RFC 6238, HMAC-SHA1, 6 digits, 30 second steps), which is what
// authenticator apps and the reference panel use, together with the protection against using one code twice.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

const (
	// Digits is the length of a code.
	Digits = 6
	// Period is the length of one time step in seconds.
	Period = 30
	// SecretLength is the length of a new secret in base32 characters (160 bits), as in the reference panel.
	SecretLength = 32
	// DefaultWindow is how many steps before and after the current one are accepted, to absorb clock drift.
	DefaultWindow = 1

	base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

var secretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random secret of SecretLength base32 characters. r is crypto/rand.Reader in production.
func GenerateSecret(r io.Reader) (string, error) {
	// Every byte below 224 maps evenly onto the 32 characters (224 = 7 * 32); larger ones are skipped to avoid bias.
	out := make([]byte, 0, SecretLength)
	buf := make([]byte, SecretLength*2)
	for len(out) < SecretLength {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		for _, b := range buf {
			if b >= 224 {
				continue
			}
			out = append(out, base32Alphabet[b%32])
			if len(out) == SecretLength {
				break
			}
		}
	}
	return string(out), nil
}

// ProvisioningURI returns the otpauth:// URI that authenticator apps read from a QR code.
func ProvisioningURI(issuer, account, secret string) string {
	issuer = strings.Join(strings.Fields(issuer), "")
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s",
		rawURLEncode(issuer), rawURLEncode(account), rawURLEncode(secret), rawURLEncode(issuer))
}

// rawURLEncode escapes everything except letters, digits and "-._~" (RFC 3986, what PHP's rawurlencode does).
func rawURLEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Step returns the number of the time step t falls into.
func Step(t time.Time) int64 { return t.Unix() / Period }

// Code returns the code of one time step.
func Code(secret string, step int64) (string, error) {
	return codeWithDigits(secret, step, Digits)
}

func codeWithDigits(secret string, step int64, digits int) (string, error) {
	key, err := secretEncoding.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil || len(key) == 0 {
		return "", errors.New("the TOTP secret is not valid base32")
	}

	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 section 5.3).
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	modulus := uint32(1)
	for i := 0; i < digits; i++ {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus), nil
}

// Verify checks a code against the steps within window of now and returns the step it matched.
//
// lastStep is the step of the previous successful verification of this account (use Step of the stored time), or a
// negative number if there was none. Only a step newer than lastStep is accepted, so a code that was already used, or
// an older one, is refused. This is the replay protection that the reference panel gained in v1.12.0 (CVE-2025-69197).
func Verify(secret, code string, now time.Time, window int, lastStep int64) (step int64, ok bool, err error) {
	if len(code) != Digits {
		return 0, false, nil
	}
	if _, err := Code(secret, 0); err != nil {
		return 0, false, err
	}

	current := Step(now)
	matched := int64(-1)
	// Every candidate is compared in constant time and the loop never stops early, so timing does not reveal which
	// step (or how close a guess) was right.
	for s := current - int64(window); s <= current+int64(window); s++ {
		want, _ := Code(secret, s)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && s > lastStep && s > matched {
			matched = s
		}
	}
	if matched < 0 {
		return 0, false, nil
	}
	return matched, true, nil
}
