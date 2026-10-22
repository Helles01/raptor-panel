// Package sshkeys reads the public keys users register for SFTP and works out the fingerprint they are recognised by.
package sshkeys

import (
	"bytes"
	"crypto/dsa" //nolint:staticcheck // only to recognise a DSA key and refuse it
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	MaxLength  = 16384
	MinRSABits = 2048
)

// The refusals, with the reference's texts, except that the RSA message says bits where the reference says bytes.
var (
	ErrInvalid  = errors.New("The public key provided is not valid.")          //nolint:staticcheck // the reference's text
	ErrDSA      = errors.New("DSA keys are not supported.")                    //nolint:staticcheck // the reference's text
	ErrShortRSA = errors.New("RSA keys must be at least 2048 bits in length.") //nolint:staticcheck // the reference's text
)

// Only these start an OpenSSH public key line. A line with options in front, a certificate or a hardware-backed key is not one.
var openSSHPrefixes = []string{"ssh-rsa ", "ssh-ed25519 ", "ecdsa-sha2-nistp256 ", "ecdsa-sha2-nistp384 ", "ecdsa-sha2-nistp521 "}

// Key is a public key that can be registered.
type Key struct {
	// Type is the OpenSSH name of the key type, for example ssh-ed25519.
	Type string
	// Fingerprint is the SHA-256 of the key in base64 without padding and without a "SHA256:" prefix: the form the reference
	// stores, which the interface shows after a "SHA256:" of its own.
	Fingerprint string

	public ssh.PublicKey
}

// Parse reads a public key from an OpenSSH line ("ssh-ed25519 AAAA… comment", the form the node sends and users paste) or
// from a PEM block ("BEGIN PUBLIC KEY" or "BEGIN RSA PUBLIC KEY"). Surrounding white space is ignored.
func Parse(text string) (Key, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > MaxLength || strings.ContainsRune(text, 0) {
		return Key{}, ErrInvalid
	}

	var pub ssh.PublicKey
	var err error
	switch {
	case strings.HasPrefix(text, "-----BEGIN PUBLIC KEY-----"), strings.HasPrefix(text, "-----BEGIN RSA PUBLIC KEY-----"):
		pub, err = parsePEM(text)
	case hasOpenSSHPrefix(text):
		pub, err = parseOpenSSH(text)
	default:
		return Key{}, ErrInvalid
	}
	if err != nil {
		return Key{}, err
	}

	if err := checkStrength(pub); err != nil {
		return Key{}, err
	}
	sum := sha256.Sum256(pub.Marshal())
	return Key{Type: pub.Type(), Fingerprint: base64.RawStdEncoding.EncodeToString(sum[:]), public: pub}, nil
}

func hasOpenSSHPrefix(text string) bool {
	for _, p := range openSSHPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// parseOpenSSH relies on the prefix check of Parse for there being no options in front of the key, and on ParseAuthorizedKey
// for the name in front matching the type of the key; anything on further lines is refused here.
func parseOpenSSH(text string) (ssh.PublicKey, error) {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil || len(bytes.TrimSpace(rest)) > 0 {
		return nil, ErrInvalid
	}
	return pub, nil
}

func parsePEM(text string) (ssh.PublicKey, error) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || len(block.Headers) > 0 || len(bytes.TrimSpace(rest)) > 0 {
		return nil, ErrInvalid
	}

	var key any
	var err error
	switch block.Type {
	case "PUBLIC KEY":
		key, err = x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, ErrInvalid
	}
	if _, isDSA := key.(*dsa.PublicKey); isDSA {
		return nil, ErrDSA
	}

	pub, err := ssh.NewPublicKey(key)
	if err != nil {
		return nil, ErrInvalid
	}
	return pub, nil
}

func checkStrength(pub ssh.PublicKey) error {
	crypto, ok := pub.(ssh.CryptoPublicKey)
	if !ok {
		return ErrInvalid
	}
	if k, isRSA := crypto.CryptoPublicKey().(*rsa.PublicKey); isRSA && k.N.BitLen() < MinRSABits {
		return ErrShortRSA
	}
	return nil
}

// PEM returns the key as a PEM "PUBLIC KEY" block, the form the reference stores in its database.
func (k Key) PEM() (string, error) {
	der, err := x509.MarshalPKIXPublicKey(k.public.(ssh.CryptoPublicKey).CryptoPublicKey())
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
