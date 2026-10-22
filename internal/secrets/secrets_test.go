package secrets

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newTestBox(t *testing.T, fill byte) *Box {
	t.Helper()
	box, err := NewBox(bytes.Repeat([]byte{fill}, keySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return box
}

func TestSealOpenRoundTrip(t *testing.T) {
	box := newTestBox(t, 1)
	ctx := Context("nodes", 7, "daemon_token")

	tests := []struct {
		name      string
		plaintext string
	}{
		{"token", "5FtsW0o1pXqzhg8oN2pRFPCcb3J1OZ1Bk8vmzoo84bTBnvwk"},
		{"empty", ""},
		{"unicode", "jelszó-ű-ő-🔑"},
		{"looks like serialized php", `s:3:"abc";`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sealed, err := box.Seal(tt.plaintext, ctx)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if !strings.HasPrefix(sealed, "rp1:") {
				t.Errorf("sealed value %q has no version prefix", sealed)
			}
			if tt.plaintext != "" && strings.Contains(sealed, tt.plaintext) {
				t.Errorf("sealed value contains the plaintext")
			}

			got, err := box.Open(sealed, ctx)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if got != tt.plaintext {
				t.Errorf("Open = %q, want %q", got, tt.plaintext)
			}
		})
	}
}

func TestSealIsRandomised(t *testing.T) {
	box := newTestBox(t, 1)
	a, _ := box.Seal("same", "ctx")
	b, _ := box.Seal("same", "ctx")
	if a == b {
		t.Error("two seals of the same value are identical, the nonce is not random")
	}
}

func TestOpenRejects(t *testing.T) {
	box := newTestBox(t, 1)
	ctx := Context("users", 3, "totp_secret")
	sealed, err := box.Seal("secret", ctx)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	flipped := []byte(sealed)
	flipped[len(flipped)-2] ^= 0x01

	tests := []struct {
		name   string
		box    *Box
		sealed string
		ctx    string
	}{
		{"wrong context row", box, sealed, Context("users", 4, "totp_secret")},
		{"wrong context column", box, sealed, Context("users", 3, "password")},
		{"wrong key", newTestBox(t, 2), sealed, ctx},
		{"tampered", box, string(flipped), ctx},
		{"no prefix", box, strings.TrimPrefix(sealed, "rp1:"), ctx},
		{"other version", box, "rp2:" + strings.TrimPrefix(sealed, "rp1:"), ctx},
		{"not base64", box, "rp1:!!!", ctx},
		{"too short", box, "rp1:AAAA", ctx},
		{"empty", box, "", ctx},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.box.Open(tt.sealed, tt.ctx); !errors.Is(err, ErrInvalid) {
				t.Errorf("Open error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestParseKey(t *testing.T) {
	generated, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := ParseKey(generated)
	if err != nil || len(key) != keySize {
		t.Fatalf("ParseKey(generated) = %d bytes, %v", len(key), err)
	}

	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"not base64", "this is not base64!"},
		{"too short", "c2hvcnQ="},
		{"too long", strings.Repeat("QUFB", 20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseKey(tt.input)
			if err == nil {
				t.Fatal("ParseKey succeeded, want an error")
			}
			if tt.input != "" && strings.Contains(err.Error(), tt.input) {
				t.Errorf("error %q echoes the key material", err)
			}
		})
	}
}

func TestNewBoxKeySize(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := NewBox(make([]byte, n)); err == nil {
			t.Errorf("NewBox accepted a %d byte key", n)
		}
	}
}

func TestContext(t *testing.T) {
	if got, want := Context("nodes", 5, "daemon_token"), "raptor_secrets|nodes|5|daemon_token"; got != want {
		t.Errorf("Context = %q, want %q", got, want)
	}
}
