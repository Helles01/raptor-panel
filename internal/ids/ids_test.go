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

func TestRandomBelow(t *testing.T) {
	// For n=5 the largest draw would favour 0, so it is thrown away: 0xFFFFFFFF, then 7, gives 7 mod 5.
	got, err := RandomBelow(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 7}), 5)
	if err != nil || got != 2 {
		t.Errorf("RandomBelow = %d, %v; want 2", got, err)
	}

	for _, n := range []int{1, 2, 62, 1000, 130000, 1 << 31} {
		for i := 0; i < 200; i++ {
			v, err := RandomBelow(rand.Reader, n)
			if err != nil || v < 0 || v >= n {
				t.Fatalf("RandomBelow(%d) = %d, %v", n, v, err)
			}
		}
	}

	counts := make([]int, 62)
	for i := 0; i < 62000; i++ {
		v, err := RandomBelow(rand.Reader, 62)
		if err != nil {
			t.Fatal(err)
		}
		counts[v]++
	}
	for v, c := range counts {
		if c < 800 || c > 1200 {
			t.Errorf("value %d came up %d times in 62000 draws", v, c)
		}
	}

	for _, bad := range []int{0, -1, 1<<31 + 1} {
		if _, err := RandomBelow(rand.Reader, bad); err == nil {
			t.Errorf("RandomBelow(%d) must be refused", bad)
		}
	}
	if _, err := RandomBelow(bytes.NewReader([]byte{1, 2, 3}), 10); err == nil {
		t.Error("want an error when the random source runs out")
	}
}

func TestRealtime(t *testing.T) {
	tests := []struct {
		name, prefix, uuid, want string
		wantErr                  bool
	}{
		{"all zero bits", "serv", "00000000-0000-0000-0000-000000000000", "serv_aaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		// 15 bytes of ones make 24 characters of 7, the last byte 11111111 is the five bits 11111 and the three bits 111 padded with zeros.
		{"all one bits", "user", "ffffffff-ffff-ffff-ffff-ffffffffffff", "user_77777777777777777777777774", false},
		// 0x01 is the bits 00000 001(00): "a" and then "e" (index 4).
		{"first byte one", "serv", "01000000-0000-0000-0000-000000000000", "serv_aeaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"upper case hex", "serv", "FFFFFFFF-ffff-FFFF-ffff-ffffffffffff", "serv_77777777777777777777777774", false},
		{"too short", "serv", "0000000-0000-0000-0000-000000000000", "", true},
		{"too long", "serv", "000000000-0000-0000-0000-000000000000", "", true},
		{"no dashes", "serv", "00000000000000000000000000000000000x", "", true},
		{"dash in the wrong place", "serv", "000000000-000-0000-0000-000000000000", "", true},
		{"not hex", "serv", "0000000g-0000-0000-0000-000000000000", "", true},
		{"empty", "serv", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Realtime(tt.prefix, tt.uuid)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("Realtime(%q, %q) = %q, %v; want %q (error %v)", tt.prefix, tt.uuid, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestParseRealtime(t *testing.T) {
	tests := []struct {
		name, prefix, in, want string
		ok                     bool
	}{
		{"all zero bits", "serv", "serv_aaaaaaaaaaaaaaaaaaaaaaaaaa", "00000000-0000-0000-0000-000000000000", true},
		{"all one bits", "user", "user_77777777777777777777777774", "ffffffff-ffff-ffff-ffff-ffffffffffff", true},
		{"first byte one", "serv", "serv_aeaaaaaaaaaaaaaaaaaaaaaaaa", "01000000-0000-0000-0000-000000000000", true},
		{"another prefix", "user", "serv_aaaaaaaaaaaaaaaaaaaaaaaaaa", "", false},
		{"no prefix", "serv", "aaaaaaaaaaaaaaaaaaaaaaaaaa", "", false},
		{"only the prefix", "serv", "serv_", "", false},
		{"too short", "serv", "serv_aaaaaaaaaaaaaaaaaaaaaaaaa", "", false},
		{"too long", "serv", "serv_aaaaaaaaaaaaaaaaaaaaaaaaaaa", "", false},
		// The last character carries four stray bits: the same 16 bytes written another way are not an identifier.
		{"stray bits", "serv", "serv_aaaaaaaaaaaaaaaaaaaaaaaaab", "", false},
		{"a character outside the alphabet", "serv", "serv_aaaaaaaaaaaaaaaaaaaaaaaaa1", "", false},
		{"upper case", "serv", "serv_AAAAAAAAAAAAAAAAAAAAAAAAAA", "", false},
		{"padding", "serv", "serv_aaaaaaaaaaaaaaaaaaaaaaaaaa======", "", false},
		{"empty", "serv", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseRealtime(tt.prefix, tt.in)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ParseRealtime(%q, %q) = %q, %v; want %q, %v", tt.prefix, tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestRealtimeRoundTrip(t *testing.T) {
	for i := 0; i < 50; i++ {
		uuid, err := UUIDv4(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		id, err := Realtime("serv", uuid)
		if err != nil {
			t.Fatal(err)
		}
		if back, ok := ParseRealtime("serv", id); !ok || back != uuid {
			t.Errorf("%s -> %s -> %s", uuid, id, back)
		}
	}
}
