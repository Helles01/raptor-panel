// Package legacycrypt reads values encrypted by the reference panel (Laravel's Crypt facade, AES-CBC with an HMAC).
//
// It exists only for the one-way secret migration command. Request handlers must never use it, and it can only
// decrypt, never encrypt.
package legacycrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrDecrypt is returned for every payload that cannot be decrypted. It deliberately says nothing about why, so it
// cannot be used as an oracle.
var ErrDecrypt = errors.New("legacycrypt: payload cannot be decrypted")

// ParseAppKey parses an APP_KEY value: either "base64:<key>" or the raw key.
func ParseAppKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if encoded, ok := strings.CutPrefix(s, "base64:"); ok {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("APP_KEY has the base64: prefix but is not valid base64")
		}
		return key, nil
	}
	return []byte(s), nil
}

// Decrypter decrypts Laravel payloads with one key.
type Decrypter struct {
	key []byte
}

// New returns a Decrypter. A 32 byte key selects AES-256-CBC (the reference panel's cipher), a 16 byte key AES-128-CBC.
func New(key []byte) (*Decrypter, error) {
	if len(key) != 16 && len(key) != 32 {
		return nil, errors.New("APP_KEY must decode to 16 or 32 bytes")
	}
	return &Decrypter{key: append([]byte(nil), key...)}, nil
}

type payload struct {
	IV    string `json:"iv"`
	Value string `json:"value"`
	MAC   string `json:"mac"`
	Tag   string `json:"tag"`
}

// Decrypt decrypts a payload as written by Laravel's Encrypter. If the plaintext is a PHP serialized string
// (`s:<bytes>:"<value>";`, what encrypt() produces) the value is unwrapped; otherwise (encryptString()) it is returned
// as is.
func (d *Decrypter) Decrypt(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrDecrypt
	}

	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", ErrDecrypt
	}
	if p.IV == "" || p.Value == "" || p.MAC == "" {
		return "", ErrDecrypt
	}
	if p.Tag != "" {
		return "", fmt.Errorf("%w: AEAD payloads are not supported", ErrDecrypt)
	}

	// The MAC is checked before anything else is parsed. It covers the base64 strings exactly as they were written.
	mac := hmac.New(sha256.New, d.key)
	mac.Write([]byte(p.IV + p.Value))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(p.MAC)) {
		return "", ErrDecrypt
	}

	iv, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil || len(iv) != aes.BlockSize {
		return "", ErrDecrypt
	}
	ciphertext, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", ErrDecrypt
	}

	block, err := aes.NewCipher(d.key)
	if err != nil {
		return "", ErrDecrypt
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	plaintext, ok := unpad(plaintext)
	if !ok {
		return "", ErrDecrypt
	}
	return unserializeString(string(plaintext)), nil
}

// unpad removes PKCS#7 padding.
func unpad(b []byte) ([]byte, bool) {
	if len(b) == 0 {
		return nil, false
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, false
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, false
		}
	}
	return b[:len(b)-n], true
}

// unserializeString unwraps a PHP serialized string, `s:<byte length>:"<bytes>";`. Anything else is returned unchanged.
func unserializeString(s string) string {
	rest, ok := strings.CutPrefix(s, "s:")
	if !ok {
		return s
	}
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return s
	}
	n, err := strconv.Atoi(rest[:colon])
	if err != nil || n < 0 {
		return s
	}
	rest = rest[colon+1:]
	if len(rest) != n+3 || rest[0] != '"' || rest[len(rest)-2:] != `";` {
		return s
	}
	return rest[1 : 1+n]
}
