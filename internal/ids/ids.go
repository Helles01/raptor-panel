// Package ids generates the random identifiers the panel uses: alphanumeric tokens and UUIDs.
package ids

import (
	"fmt"
	"io"
)

const alphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// RandomAlnum returns n random characters from A-Z a-z 0-9, the alphabet of the reference panel's Str::random().
// Pass crypto/rand.Reader as r in production. Bytes that would skew the distribution are skipped.
func RandomAlnum(r io.Reader, n int) (string, error) {
	if n < 0 {
		return "", fmt.Errorf("length must not be negative")
	}

	out := make([]byte, 0, n)
	buf := make([]byte, n+n/2+8)
	for len(out) < n {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		for _, b := range buf {
			// 248 is the largest multiple of 62 below 256; larger bytes would make some characters more likely.
			if b >= 248 {
				continue
			}
			out = append(out, alphanumeric[b%62])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// UUIDv4 returns a random (version 4) UUID in its canonical lower case form.
func UUIDv4(r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
