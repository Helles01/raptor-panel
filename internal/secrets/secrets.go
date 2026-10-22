// Package secrets seals and opens Raptor's own secret format: AES-256-GCM with a version prefix, where every value is
// bound to the place it is stored in (table, row and column) so that ciphertexts cannot be swapped between rows.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	prefix  = "rp1:"
	keySize = 32
)

// ErrInvalid is returned for any value that cannot be opened. It deliberately says nothing about why.
var ErrInvalid = errors.New("secrets: value cannot be opened")

// GenerateKey returns a new random key in the form accepted by ParseKey (base64 of 32 bytes).
func GenerateKey() (string, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate secret key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// ParseKey decodes a key given as base64 of exactly 32 bytes. The input is never echoed in errors.
func ParseKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(key) != keySize {
		return nil, errors.New("secret key must be the base64 encoding of exactly 32 bytes")
	}
	return key, nil
}

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// NewBox returns a Box for a 32 byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, errors.New("secret key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Context builds the value a secret is bound to. Use the same context when sealing and opening.
func Context(table string, rowID uint64, column string) string {
	return "raptor_secrets|" + table + "|" + strconv.FormatUint(rowID, 10) + "|" + column
}

// Seal encrypts plaintext and binds it to context.
func (b *Box) Seal(plaintext, context string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(context))
	return prefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value produced by Seal with the same context.
func (b *Box) Open(sealed, context string) (string, error) {
	encoded, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return "", ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", ErrInvalid
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(context))
	if err != nil {
		return "", ErrInvalid
	}
	return string(plaintext), nil
}
