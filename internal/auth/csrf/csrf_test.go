package csrf

import (
	"bytes"
	"strings"
	"testing"
)

func newProtector(t *testing.T, fill byte) *Protector {
	t.Helper()
	p, err := New(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIssueAndValid(t *testing.T) {
	p := newProtector(t, 1)

	token, err := p.Issue("session-a")
	if err != nil {
		t.Fatal(err)
	}

	if !p.Valid("session-a", token) {
		t.Errorf("a token was not valid for the session it was issued for: %s", token)
	}
	if strings.ContainsAny(token, "+/= ") {
		t.Errorf("the token %q is not safe for a cookie and a header", token)
	}
}

func TestTokensAreBoundToTheirSession(t *testing.T) {
	p := newProtector(t, 1)
	token, _ := p.Issue("session-a")

	if p.Valid("session-b", token) {
		t.Error("a token of one session was accepted for another")
	}
	if p.Valid("", token) {
		t.Error("a token was accepted without a session")
	}
	if newProtector(t, 2).Valid("session-a", token) {
		t.Error("a token was accepted under another key")
	}
}

func TestTokensAreRandom(t *testing.T) {
	p := newProtector(t, 1)
	a, _ := p.Issue("s")
	b, _ := p.Issue("s")
	if a == b {
		t.Error("two tokens for one session are equal")
	}
	if !p.Valid("s", a) || !p.Valid("s", b) {
		t.Error("both must be valid")
	}
}

func TestMalformedTokensAreRefused(t *testing.T) {
	p := newProtector(t, 1)
	good, _ := p.Issue("s")
	random, mac, _ := strings.Cut(good, ".")

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no separator", random + mac},
		{"only the random part", random + "."},
		{"only the mac", "." + mac},
		{"swapped parts", mac + "." + random},
		{"extra part", good + ".x"},
		{"truncated mac", random + "." + mac[:len(mac)-2]},
		{"flipped mac", random + "." + flip(mac)},
		{"flipped random", flip(random) + "." + mac},
		{"not base64", "!!!.???"},
		{"padded base64", random + "==." + mac},
		{"whitespace", " " + good},
		{"trailing newline", good + "\n"},
		{"short random part", random[:10] + "." + mac},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if p.Valid("s", tt.token) {
				t.Errorf("token %q was accepted", tt.token)
			}
		})
	}
}

func flip(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

func TestNewAndIssueValidate(t *testing.T) {
	if _, err := New(make([]byte, 31)); err == nil {
		t.Error("a short key was accepted")
	}
	if _, err := newProtector(t, 1).Issue(""); err == nil {
		t.Error("a token was issued without a session")
	}
}
