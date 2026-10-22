// Package legacycrypttest writes values in the reference panel's encryption format, for tests only. Production code
// must never import it: Raptor can read the old format (package legacycrypt) but never writes it.
package legacycrypttest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Encrypt follows Laravel's Encrypter::encrypt(): the value is PHP serialized as a string, padded (PKCS#7), encrypted
// with AES-CBC under key (16 or 32 bytes), and wrapped in a JSON payload with an HMAC-SHA256 over iv+value. The iv is
// derived from the plaintext so tests are repeatable.
func Encrypt(key []byte, plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	iv := sum[:aes.BlockSize]

	block, err := aes.NewCipher(key)
	if err != nil {
		panic(fmt.Sprintf("legacycrypttest: %v", err))
	}

	data := []byte(fmt.Sprintf(`s:%d:"%s";`, len(plaintext), plaintext))
	pad := aes.BlockSize - len(data)%aes.BlockSize
	for i := 0; i < pad; i++ {
		data = append(data, byte(pad))
	}
	ciphertext := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, data)

	ivB64 := base64.StdEncoding.EncodeToString(iv)
	valueB64 := base64.StdEncoding.EncodeToString(ciphertext)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(ivB64 + valueB64))

	payload, _ := json.Marshal(map[string]string{
		"iv": ivB64, "value": valueB64, "mac": hex.EncodeToString(mac.Sum(nil)), "tag": "",
	})
	return base64.StdEncoding.EncodeToString(payload)
}
