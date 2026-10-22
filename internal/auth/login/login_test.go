package login

import (
	"bytes"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth/lockout"
)

func TestField(t *testing.T) {
	for input, want := range map[string]string{
		"jane": "username", "jane.doe": "username", "jane@example.com": "email", "@": "email", "a@b": "email", "": "username", "ja ne": "username",
	} {
		if got := Field(input); got != want {
			t.Errorf("Field(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIdentifier(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"a user name", "jane", "jane", true},
		{"an e-mail address", "jane@example.com", "jane@example.com", true},
		{"padded", "  jane\t", "jane", true},
		{"case is kept", "Jane", "Jane", true},
		{"the longest", strings.Repeat("a", 191), strings.Repeat("a", 191), true},
		{"multi-byte characters count once", strings.Repeat("é", 191), strings.Repeat("é", 191), true},

		{"empty", "", "", false},
		{"spaces only", "   ", "", false},
		{"too long", strings.Repeat("a", 192), "", false},
		{"a newline inside", "ja\nne", "", false},
		{"a NUL inside", "ja\x00ne", "", false},
		{"invalid UTF-8", "ja\xffne", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Identifier(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Errorf("Identifier(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCounters(t *testing.T) {
	got, err := Counters("Jane", "203.0.113.9:51234")
	if err != nil {
		t.Fatal(err)
	}
	want := []lockout.Counter{
		{Key: "login|account-client|jane|203.0.113.9", Limit: 5, Window: time.Minute},
		{Key: "login|account|jane", Limit: 20, Window: 10 * time.Minute},
		{Key: "login|client|203.0.113.9", Limit: 40, Window: 10 * time.Minute},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Counters = %+v\nwant %+v", got, want)
	}
}

func keys(cs []lockout.Counter) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Key
	}
	return out
}

func TestCountersSeparateWhatTheyShould(t *testing.T) {
	a, _ := Counters("jane", "203.0.113.9")
	sameAll, _ := Counters("JANE", "203.0.113.9:4000")
	otherAccount, _ := Counters("john", "203.0.113.9")
	otherClient, _ := Counters("jane", "203.0.113.10")
	idAccount, _ := Counters("id:7", "203.0.113.9")

	if !reflect.DeepEqual(keys(a), keys(sameAll)) {
		t.Error("the same account from the same address must share its counters, whatever the case and the port")
	}
	if a[0].Key == otherAccount[0].Key || a[1].Key == otherAccount[1].Key || a[2].Key != otherAccount[2].Key {
		t.Errorf("another account: %v vs %v", keys(a), keys(otherAccount))
	}
	if a[0].Key == otherClient[0].Key || a[1].Key != otherClient[1].Key || a[2].Key == otherClient[2].Key {
		t.Errorf("another client: %v vs %v", keys(a), keys(otherClient))
	}
	if idAccount[0].Key == a[0].Key || idAccount[1].Key == a[1].Key {
		t.Error("a user id and a login name must not share counters")
	}

	// Two clients of the same IPv6 network share the counters of the network, one client of another network does not.
	n1, _ := Counters("jane", "[2001:db8:1:2::1]:1")
	n2, _ := Counters("jane", "[2001:db8:1:2:ffff::9]:2")
	n3, _ := Counters("jane", "[2001:db8:1:3::1]:1")
	if n1[2].Key != n2[2].Key || n1[2].Key == n3[2].Key {
		t.Errorf("IPv6 clients: %v %v %v", n1[2].Key, n2[2].Key, n3[2].Key)
	}
}

func TestCountersRefuseWhatCannotBeTold(t *testing.T) {
	for _, tt := range []struct{ account, addr string }{
		{"jane", ""}, {"jane", "not an address"}, {"jane", "client.example.com"}, {"jane", ":22"}, {"", "203.0.113.9"}, {"", ""},
	} {
		if c, err := Counters(tt.account, tt.addr); !errors.Is(err, ErrNoClient) || c != nil {
			t.Errorf("Counters(%q, %q) = %v, %v; want ErrNoClient", tt.account, tt.addr, c, err)
		}
	}
}

func TestFirstFactor(t *testing.T) {
	tests := []struct {
		name                        string
		found, passwordOK, usesTOTP bool
		want                        First
		burn                        bool
	}{
		{"no such user", false, false, false, Rejected, true},
		{"no such user, whatever else is said", false, true, true, Rejected, true},
		{"a wrong password", true, false, false, Rejected, false},
		{"a wrong password for a user with two-factor authentication", true, false, true, Rejected, false},
		{"a right password", true, true, false, Done, false},
		{"a right password and two-factor authentication", true, true, true, NeedsSecondFactor, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, burn := FirstFactor(tt.found, tt.passwordOK, tt.usesTOTP)
			if got != tt.want || burn != tt.burn {
				t.Errorf("FirstFactor = %d, burn %v; want %d, %v", got, burn, tt.want, tt.burn)
			}
		})
	}
}

func TestSecondFactor(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		recovery string
		method   Method
		value    string
		err      error
	}{
		{"a code", "123456", "", Code, "123456", nil},
		{"a code with leading zeros", "000123", "", Code, "000123", nil},
		{"a padded code", " 123456 ", "", Code, "123456", nil},
		{"a recovery token", "", "aB3dE5fG7h", Recovery, "aB3dE5fG7h", nil},
		{"a padded recovery token", "", " aB3dE5fG7h\n", Recovery, "aB3dE5fG7h", nil},
		{"both: the recovery token wins", "123456", "aB3dE5fG7h", Recovery, "aB3dE5fG7h", nil},

		{"neither", "", "", Code, "", ErrNoSecondFactor},
		{"only spaces", "  ", "\t", Code, "", ErrNoSecondFactor},
		{"a code that is too short", "12345", "", Code, "", ErrCheckpointFailed},
		{"a code that is too long", "1234567", "", Code, "", ErrCheckpointFailed},
		{"a code with a space", "123 456", "", Code, "", ErrCheckpointFailed},
		{"a code with letters", "12345a", "", Code, "", ErrCheckpointFailed},
		{"a code that looks like a number", "1e5000", "", Code, "", ErrCheckpointFailed},
		{"a negative code", "-12345", "", Code, "", ErrCheckpointFailed},
		{"a recovery token that is too short", "", "aB3dE5fG7", Recovery, "", ErrRecoveryFailed},
		{"a recovery token that is too long", "", "aB3dE5fG7hi", Recovery, "", ErrRecoveryFailed},
		{"a recovery token with a dash", "", "aB3dE5fG7-", Recovery, "", ErrRecoveryFailed},
		{"a bad recovery token and a good code: no fall back", "123456", "short", Recovery, "", ErrRecoveryFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, v, err := SecondFactor(tt.code, tt.recovery)
			if m != tt.method || v != tt.value || !errors.Is(err, tt.err) {
				t.Errorf("SecondFactor = %d, %q, %v; want %d, %q, %v", m, v, err, tt.method, tt.value, tt.err)
			}
		})
	}
}

func TestFailureMessages(t *testing.T) {
	if SecondFactorFailure(Recovery) != ErrRecoveryFailed || SecondFactorFailure(Code) != ErrCheckpointFailed {
		t.Error("SecondFactorFailure picked the wrong error")
	}
	if ConfirmationFailure(false) != ErrTokenExpired || ConfirmationFailure(true) != ErrCheckpointFailed {
		t.Error("ConfirmationFailure picked the wrong error")
	}

	// The frontend shows these words to the user.
	for err, want := range map[error]string{
		ErrFailed:           "No account matching those credentials could be found.",
		ErrCheckpointFailed: "The two-factor authentication token was invalid.",
		ErrRecoveryFailed:   "The recovery token provided is not valid.",
		ErrTokenExpired:     "The authentication token provided has expired, please refresh the page and try again.",
	} {
		if err.Error() != want {
			t.Errorf("%q, want %q", err.Error(), want)
		}
	}
}

func TestNewRecoveryTokens(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tokens, err := NewRecoveryTokens(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(tokens) != RecoveryTokenCount || RecoveryTokenCount != 10 {
			t.Fatalf("%d tokens", len(tokens))
		}
		for _, tok := range tokens {
			if !recoveryPattern.MatchString(tok) {
				t.Fatalf("token %q has the wrong form", tok)
			}
			// What is made is what SecondFactor accepts.
			if _, v, err := SecondFactor("", tok); err != nil || v != tok {
				t.Fatalf("a made token is refused: %q, %v", tok, err)
			}
			if seen[tok] {
				t.Fatalf("token %q was made twice", tok)
			}
			seen[tok] = true
		}
	}

	if _, err := NewRecoveryTokens(bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
	if _, err := NewRecoveryTokens(bytes.NewReader(bytes.Repeat([]byte{1}, 60))); err == nil {
		t.Error("want an error when the random bytes run out half way")
	}
}
