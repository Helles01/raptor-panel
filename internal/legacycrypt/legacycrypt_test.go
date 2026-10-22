package legacycrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The vectors below are produced by following Laravel's Encrypter::encrypt() step by step (PKCS#7, AES-CBC,
// HMAC-SHA256 over iv+value, JSON, base64). They check the format handling; a payload written by a real PHP process
// is still needed to rule out a shared misreading of the format (see docs/modules/01-config-db-secrets.md).

func pkcs7(b []byte) []byte {
	n := aes.BlockSize - len(b)%aes.BlockSize
	return append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

// wrap builds the outer payload for the given base64 iv and value, with a valid MAC.
func wrap(key []byte, ivB64, valueB64 string, escapeSlashes bool) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(ivB64 + valueB64))
	j, _ := json.Marshal(map[string]string{
		"iv": ivB64, "value": valueB64, "mac": hex.EncodeToString(mac.Sum(nil)), "tag": "",
	})
	if escapeSlashes {
		// PHP's json_encode escapes "/" as "\/".
		j = bytes.ReplaceAll(j, []byte("/"), []byte(`\/`))
	}
	return base64.StdEncoding.EncodeToString(j)
}

func encryptRaw(t *testing.T, key []byte, plaintext []byte, escapeSlashes bool) string {
	t.Helper()
	iv := bytes.Repeat([]byte{0x2a}, aes.BlockSize)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padded := pkcs7(plaintext)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return wrap(key, base64.StdEncoding.EncodeToString(iv), base64.StdEncoding.EncodeToString(ct), escapeSlashes)
}

func serialize(s string) []byte { return []byte(fmt.Sprintf(`s:%d:"%s";`, len(s), s)) }

func TestDecrypt(t *testing.T) {
	key32 := bytes.Repeat([]byte{0x11}, 32)
	key16 := bytes.Repeat([]byte{0x22}, 16)
	token := "kRxj1xqCwMBmYYLU4n1sAUmuRVY9NCtwk7J3hFrRzAMLBlDvP5Jb4HEQPqsoQSEz"

	tests := []struct {
		name    string
		key     []byte
		payload string
		want    string
	}{
		{"serialized string (encrypt)", key32, encryptRaw(t, key32, serialize(token), false), token},
		{"raw string (encryptString)", key32, encryptRaw(t, key32, []byte(token), false), token},
		{"unicode is measured in bytes", key32, encryptRaw(t, key32, serialize("jelszó-ű"), false), "jelszó-ű"},
		{"empty serialized string", key32, encryptRaw(t, key32, serialize(""), false), ""},
		{"escaped slashes in the JSON", key32, encryptRaw(t, key32, serialize(token), true), token},
		{"AES-128 key", key16, encryptRaw(t, key16, serialize(token), false), token},
		{"exactly one block of padding", key32, encryptRaw(t, key32, bytes.Repeat([]byte("a"), 16), false), strings.Repeat("a", 16)},
		{"serialized string with wrong length is kept as is", key32, encryptRaw(t, key32, []byte(`s:9:"abc";`), false), `s:9:"abc";`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := New(tt.key)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := d.Decrypt(tt.payload)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if got != tt.want {
				t.Errorf("Decrypt = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecryptRejects(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	good := encryptRaw(t, key, serialize("secret"), false)

	var p payload
	raw, _ := base64.StdEncoding.DecodeString(good)
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	reencode := func(mutate func(*payload)) string {
		q := p
		mutate(&q)
		j, _ := json.Marshal(q)
		return base64.StdEncoding.EncodeToString(j)
	}

	otherKey := bytes.Repeat([]byte{0x33}, 32)
	iv := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, aes.BlockSize))

	tests := []struct {
		name    string
		payload string
		wantErr error
	}{
		{"wrong key", encryptRaw(t, otherKey, serialize("secret"), false), ErrDecrypt},
		{"tampered value", reencode(func(q *payload) { q.Value = "A" + q.Value[1:] }), ErrDecrypt},
		{"tampered mac", reencode(func(q *payload) { q.MAC = strings.Repeat("0", 64) }), ErrDecrypt},
		{"tampered iv", reencode(func(q *payload) { q.IV = iv }), ErrDecrypt},
		{"missing mac", reencode(func(q *payload) { q.MAC = "" }), ErrDecrypt},
		{"missing value", reencode(func(q *payload) { q.Value = "" }), ErrDecrypt},
		{"AEAD tag present", reencode(func(q *payload) { q.Tag = "dGFn" }), ErrDecrypt},
		{"outer payload not base64", "%%%", ErrDecrypt},
		{"outer payload not JSON", base64.StdEncoding.EncodeToString([]byte("not json")), ErrDecrypt},
		{"empty", "", ErrDecrypt},
		{"iv of wrong length (valid mac)", wrap(key, base64.StdEncoding.EncodeToString([]byte("short")), p.Value, false), ErrDecrypt},
		{"value not a block multiple (valid mac)", wrap(key, p.IV, base64.StdEncoding.EncodeToString([]byte("short")), false), ErrDecrypt},
		{"invalid padding (valid mac)", func() string {
			block, _ := aes.NewCipher(key)
			unpadded := bytes.Repeat([]byte("x"), aes.BlockSize) // last byte 0x78, not valid PKCS#7
			ct := make([]byte, aes.BlockSize)
			cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{1}, aes.BlockSize)).CryptBlocks(ct, unpadded)
			return wrap(key, iv, base64.StdEncoding.EncodeToString(ct), false)
		}(), ErrDecrypt},
	}
	d, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.Decrypt(tt.payload)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Decrypt error = %v, want %v", err, tt.wantErr)
			}
			if got != "" {
				t.Errorf("Decrypt returned %q alongside an error", got)
			}
		})
	}
}

func TestNewKeyLength(t *testing.T) {
	for _, n := range []int{0, 8, 24, 31, 33} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Errorf("New accepted a %d byte key", n)
		}
	}
}

func TestParseAppKey(t *testing.T) {
	raw32 := strings.Repeat("k", 32)
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"base64 prefix", "base64:" + base64.StdEncoding.EncodeToString([]byte(raw32)), raw32, false},
		{"surrounding whitespace", "  base64:" + base64.StdEncoding.EncodeToString([]byte(raw32)) + "\n", raw32, false},
		{"raw key", raw32, raw32, false},
		{"prefix with invalid base64", "base64:***", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAppKey(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAppKey error = %v, wantErr %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("ParseAppKey = %q, want %q", got, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), "***") {
				t.Errorf("error echoes the key material: %v", err)
			}
		})
	}
}

func TestUnserializeString(t *testing.T) {
	tests := []struct{ in, want string }{
		{`s:3:"abc";`, "abc"},
		{`s:0:"";`, ""},
		{`s:2:"abc";`, `s:2:"abc";`},
		{`s:4:"abc";`, `s:4:"abc";`},
		{`s:x:"abc";`, `s:x:"abc";`},
		{`s:-1:"abc";`, `s:-1:"abc";`},
		{`s:3:"abc"`, `s:3:"abc"`},
		{`i:5;`, `i:5;`},
		{`plain`, `plain`},
		{``, ``},
	}
	for _, tt := range tests {
		if got := unserializeString(tt.in); got != tt.want {
			t.Errorf("unserializeString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
