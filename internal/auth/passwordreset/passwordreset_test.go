package passwordreset

import (
	"bytes"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

func newToken(t *testing.T) Token {
	t.Helper()
	tok, err := NewToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestNewToken(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := newToken(t)
		if !wellFormed(tok.Plain) || len(tok.Plain) != TokenLength {
			t.Fatalf("token %q has the wrong form", tok.Plain)
		}
		if tok.Hash == tok.Plain || len(tok.Hash) != 64 || strings.Contains(tok.Hash, tok.Plain) {
			t.Fatalf("the hash %q is the token or holds it", tok.Hash)
		}
		if seen[tok.Plain] || seen[tok.Hash] {
			t.Fatal("a token was made twice")
		}
		seen[tok.Plain], seen[tok.Hash] = true, true
	}

	// The same random bytes give the same token, so the token is the random bytes and nothing else.
	a, _ := NewToken(bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)))
	if a.Plain != strings.Repeat("ab", 32) {
		t.Errorf("token = %q", a.Plain)
	}
	if _, err := NewToken(bytes.NewReader(make([]byte, 31))); err == nil {
		t.Error("want an error when the random source runs out")
	}
	if _, err := NewToken(bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
}

func TestRedeem(t *testing.T) {
	tok := newToken(t)
	other := newToken(t)
	rec := func(age time.Duration) *Record { return &Record{Hash: tok.Hash, CreatedAt: ago(age)} }

	tests := []struct {
		name      string
		rec       *Record
		presented string
		ok        bool
	}{
		{"a fresh token", rec(time.Minute), tok.Plain, true},
		{"a token made just now", rec(0), tok.Plain, true},
		{"a token at the limit of its life", rec(TTL), tok.Plain, true},
		{"a token just past its life", rec(TTL + time.Second), tok.Plain, false},
		{"a token a day old", rec(24 * time.Hour), tok.Plain, false},
		{"a token from slightly in the future", rec(-time.Minute), tok.Plain, true},
		{"a token from far in the future", rec(-time.Hour), tok.Plain, false},

		{"no token stored for the address", nil, tok.Plain, false},
		{"another token", rec(time.Minute), other.Plain, false},
		{"the stored hash presented as the token", rec(time.Minute), tok.Hash, false},
		{"the token in upper case", rec(time.Minute), strings.ToUpper(tok.Plain), false},
		{"empty", rec(time.Minute), "", false},
		{"a short token", rec(time.Minute), tok.Plain[:63], false},
		{"a long token", rec(time.Minute), tok.Plain + "0", false},
		{"the token with a space", rec(time.Minute), tok.Plain[:63] + " ", false},
		{"not hexadecimal", rec(time.Minute), strings.Repeat("g", 64), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Redeem(tt.rec, tt.presented, now)
			if tt.ok && err != nil {
				t.Errorf("error = %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrInvalidToken) {
				t.Errorf("error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestRedeemGivesOneAnswerForEveryFailure(t *testing.T) {
	// Nothing in the error says which part was wrong.
	tok := newToken(t)
	errs := []error{
		Redeem(nil, tok.Plain, now),
		Redeem(&Record{Hash: tok.Hash, CreatedAt: ago(2 * time.Hour)}, tok.Plain, now),
		Redeem(&Record{Hash: tok.Hash, CreatedAt: ago(time.Minute)}, strings.Repeat("0", 64), now),
		Redeem(&Record{Hash: tok.Hash, CreatedAt: ago(time.Minute)}, "short", now),
	}
	for i, err := range errs {
		if err != ErrInvalidToken {
			t.Errorf("failure %d: %v", i, err)
		}
	}
	if ErrInvalidToken.Error() != "This password reset token is invalid." {
		t.Errorf("text = %q", ErrInvalidToken.Error())
	}
}

func TestDecide(t *testing.T) {
	at := func(d time.Duration) *time.Time { v := ago(d); return &v }
	future := now.Add(time.Hour)
	tests := []struct {
		name   string
		exists bool
		last   *time.Time
		want   Decision
	}{
		{"a user and no token yet", true, nil, Send},
		{"a user and an old token", true, at(2 * time.Hour), Send},
		{"a user and a token a minute ago", true, at(ResendAfter), Send},
		{"a user and a token just under a minute ago", true, at(ResendAfter - time.Second), SkipRecent},
		{"a user and a token just now", true, at(0), SkipRecent},
		{"a token from the future does not block for ever", true, &future, Send},
		{"an unknown user", false, nil, SkipUnknown},
		{"an unknown user with an old token", false, at(time.Hour), SkipUnknown},
		{"an unknown user with a recent token", false, at(time.Second), SkipUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.exists, tt.last, now); got != tt.want {
				t.Errorf("Decide = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLink(t *testing.T) {
	tok := strings.Repeat("ab", 32)
	tests := []struct {
		name  string
		app   string
		email string
		want  string
		ok    bool
	}{
		{"a plain address", "https://panel.example.com", "jane@example.com", "https://panel.example.com/auth/password/reset/" + tok + "?email=jane%40example.com", true},
		{"a trailing slash", "https://panel.example.com/", "jane@example.com", "https://panel.example.com/auth/password/reset/" + tok + "?email=jane%40example.com", true},
		{"a port", "http://localhost:8080", "a@b.co", "http://localhost:8080/auth/password/reset/" + tok + "?email=a%40b.co", true},
		{"a plus sign and other characters are escaped", "https://p.example.com", "a+b&c=d@example.com", "https://p.example.com/auth/password/reset/" + tok + "?email=a%2Bb%26c%3Dd%40example.com", true},
		{"an address that tries to add a parameter", "https://p.example.com", "x@example.com&token=evil", "https://p.example.com/auth/password/reset/" + tok + "?email=x%40example.com%26token%3Devil", true},

		{"no address", "", "a@b.co", "", false},
		{"no scheme", "panel.example.com", "a@b.co", "", false},
		{"another scheme", "ftp://panel.example.com", "a@b.co", "", false},
		{"javascript", "javascript:alert(1)", "a@b.co", "", false},
		{"credentials", "https://user:pw@panel.example.com", "a@b.co", "", false},
		{"no host", "https://", "a@b.co", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Link(tt.app, tok, tt.email)
			if (err == nil) != tt.ok || got != tt.want {
				t.Errorf("Link = %q, %v; want %q, ok=%v", got, err, tt.want, tt.ok)
			}
		})
	}

	for _, bad := range []string{"", "short", strings.Repeat("g", 64), tok + "0", strings.ToUpper(tok)} {
		if _, err := Link("https://p.example.com", bad, "a@b.co"); err == nil {
			t.Errorf("a token of %q was put in a link", bad)
		}
	}
}

func TestCheckNewPassword(t *testing.T) {
	tests := []struct {
		name         string
		password     string
		confirmation string
		want         []string
	}{
		{"a good password", "correct horse", "correct horse", nil},
		{"the shortest", "12345678", "12345678", nil},
		{"the longest", strings.Repeat("a", 72), strings.Repeat("a", 72), nil},
		{"characters, not bytes, for the minimum", "éééééééé", "éééééééé", nil},

		{"empty", "", "", []string{"The password field is required."}},
		{"empty with a confirmation", "", "x", []string{"The password field is required."}},
		{"not confirmed", "correct horse", "correct hors", []string{"The password confirmation does not match."}},
		{"no confirmation", "correct horse", "", []string{"The password confirmation does not match."}},
		{"too short", "1234567", "1234567", []string{"The password must be at least 8 characters."}},
		{"too short and not confirmed", "1234567", "12345678", []string{"The password confirmation does not match.", "The password must be at least 8 characters."}},
		{"too long", strings.Repeat("a", 73), strings.Repeat("a", 73), []string{"The password may not be greater than 72 bytes."}},
		{"too long in bytes though short in characters", strings.Repeat("é", 40), strings.Repeat("é", 40), []string{"The password may not be greater than 72 bytes."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckNewPassword(tt.password, tt.confirmation); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("messages = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEffects(t *testing.T) {
	without := EffectsOf(false)
	with := EffectsOf(true)

	for name, e := range map[string]Effects{"without two-factor": without, "with two-factor": with} {
		if !e.RevokeSessions || !e.DeauthorizeNodes || !e.DeleteTokens {
			t.Errorf("%s: %+v must end every session, drop the nodes' tokens and delete the reset tokens", name, e)
		}
	}
	if !without.LoginUser || without.SendToLogin {
		t.Errorf("without two-factor: %+v, want the user signed in", without)
	}
	if with.LoginUser || !with.SendToLogin {
		t.Errorf("with two-factor: %+v, want the user sent to the sign-in page and not signed in", with)
	}
}

func TestLimits(t *testing.T) {
	// The reference limits the request to 2 a minute and leaves the reset itself unlimited.
	if RequestPerIPPerMinute != 2 || ResetPerIPPerMinute < 1 || RequestPerEmailPerHour < 1 || TTL != time.Hour || ResendAfter != time.Minute {
		t.Errorf("limits: %d %d %d %v %v", RequestPerIPPerMinute, ResetPerIPPerMinute, RequestPerEmailPerHour, TTL, ResendAfter)
	}
}
