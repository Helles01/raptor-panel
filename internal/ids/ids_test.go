package ids

import (
	"bytes"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
)

func TestRandomAlnum(t *testing.T) {
	charset := regexp.MustCompile(`^[A-Za-z0-9]*$`)

	for _, n := range []int{0, 1, 16, 32, 64, 1000} {
		got, err := RandomAlnum(rand.Reader, n)
		if err != nil {
			t.Fatalf("RandomAlnum(%d): %v", n, err)
		}
		if len(got) != n || !charset.MatchString(got) {
			t.Errorf("RandomAlnum(%d) = %q", n, got)
		}
	}

	a, _ := RandomAlnum(rand.Reader, 32)
	b, _ := RandomAlnum(rand.Reader, 32)
	if a == b {
		t.Error("two random strings are equal")
	}
}

func TestRandomAlnumMapsBytesWithoutBias(t *testing.T) {
	// Bytes 0..61 map to the alphabet in order, 62..123 and so on wrap around, 248..255 are skipped.
	input := append([]byte{255, 248, 0, 1, 25, 26, 51, 52, 61, 62}, bytes.Repeat([]byte{0}, 64)...)

	got, err := RandomAlnum(bytes.NewReader(input), 9)
	if err != nil {
		t.Fatal(err)
	}
	// 255 and 248 are skipped; 0,1,25,26,51,52,61 give A,B,Z,a,z,0,9; byte 62 wraps to A, and so does the padding.
	if want := "ABZaz09AA"; got != want {
		t.Errorf("RandomAlnum = %q, want %q", got, want)
	}
}

func TestRandomAlnumErrors(t *testing.T) {
	if _, err := RandomAlnum(rand.Reader, -1); err == nil {
		t.Error("negative length accepted")
	}
	failing := iotest.ErrReader(errors.New("entropy source failed"))
	if _, err := RandomAlnum(failing, 8); err == nil || !strings.Contains(err.Error(), "entropy source failed") {
		t.Errorf("error = %v, want the reader's error", err)
	}
	if _, err := RandomAlnum(bytes.NewReader([]byte{1, 2}), 8); err == nil {
		t.Error("a short reader must be an error, not a short string")
	}
}

func TestUUIDv4(t *testing.T) {
	format := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := UUIDv4(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if !format.MatchString(id) || len(id) != 36 {
			t.Fatalf("UUIDv4 = %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID %q", id)
		}
		seen[id] = true
	}

	// Fixed input gives the RFC 4122 example layout with version and variant bits set.
	got, err := UUIDv4(bytes.NewReader(bytes.Repeat([]byte{0xff}, 16)))
	if err != nil || got != "ffffffff-ffff-4fff-bfff-ffffffffffff" {
		t.Errorf("UUIDv4(all ones) = %q, %v", got, err)
	}

	if _, err := UUIDv4(bytes.NewReader([]byte{1})); err == nil {
		t.Error("a short reader must be an error")
	}
}
