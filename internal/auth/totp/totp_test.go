package totp

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// RFC 6238 appendix B: SHA-1, secret "12345678901234567890", 8 digit codes.
func TestRFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))

	tests := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, tt := range tests {
		got, err := codeWithDigits(secret, tt.unix/Period, 8)
		if err != nil || got != tt.want {
			t.Errorf("code at %d = %q, %v; want %q", tt.unix, got, err, tt.want)
		}
		// The 6 digit code is the last 6 digits of the 8 digit one.
		six, _ := Code(secret, tt.unix/Period)
		if six != tt.want[2:] {
			t.Errorf("6 digit code at %d = %q, want %q", tt.unix, six, tt.want[2:])
		}
	}
}

func TestCodeAcceptsSecretFormats(t *testing.T) {
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890")) // has padding
	want, _ := Code(secret, 1)

	for _, variant := range []string{
		strings.ToLower(secret),
		strings.TrimRight(secret, "="),
		strings.Join(strings.SplitAfter(strings.TrimRight(secret, "="), "AA"), " "),
	} {
		if got, err := Code(variant, 1); err != nil || got != want {
			t.Errorf("secret %q: %q, %v; want %q", variant, got, err, want)
		}
	}
}

func TestCodeRejectsBadSecrets(t *testing.T) {
	for _, secret := range []string{"", "not base32!", "1111111111111111", "----"} {
		if _, err := Code(secret, 1); err == nil {
			t.Errorf("Code accepted the secret %q", secret)
		}
	}
}

func TestVerify(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	now := time.Unix(1_700_000_010, 0)
	cur := Step(now)
	codeAt := func(step int64) string {
		c, err := Code(secret, step)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	tests := []struct {
		name     string
		code     string
		window   int
		lastStep int64
		wantStep int64
		wantOK   bool
	}{
		{"current step", codeAt(cur), 1, -1, cur, true},
		{"one step early", codeAt(cur - 1), 1, -1, cur - 1, true},
		{"one step late", codeAt(cur + 1), 1, -1, cur + 1, true},
		{"two steps early with window 1", codeAt(cur - 2), 1, -1, 0, false},
		{"two steps early with window 2", codeAt(cur - 2), 2, -1, cur - 2, true},
		{"wrong code", "000000", 1, -1, 0, false},
		{"already used step", codeAt(cur), 1, cur, 0, false},
		{"older than the last used step", codeAt(cur - 1), 1, cur, 0, false},
		{"newer than the last used step", codeAt(cur + 1), 1, cur, cur + 1, true},
		{"code too short", codeAt(cur)[:5], 1, -1, 0, false},
		{"code too long", codeAt(cur) + "0", 1, -1, 0, false},
		{"empty code", "", 1, -1, 0, false},
		{"code with spaces", " " + codeAt(cur)[:5], 1, -1, 0, false},
		{"non digits", "abcdef", 1, -1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step, ok, err := Verify(secret, tt.code, now, tt.window, tt.lastStep)

			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if ok != tt.wantOK || step != tt.wantStep {
				t.Errorf("Verify = step %d, ok %v; want step %d, ok %v", step, ok, tt.wantStep, tt.wantOK)
			}
		})
	}
}

// A code used once must not log in again, even though it is still inside the time window.
func TestReplayIsRefused(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	now := time.Unix(1_700_000_010, 0)
	code, _ := Code(secret, Step(now))

	step, ok, err := Verify(secret, code, now, DefaultWindow, -1)
	if err != nil || !ok {
		t.Fatalf("first use: %v %v", ok, err)
	}

	_, again, _ := Verify(secret, code, now, DefaultWindow, step)
	_, later, _ := Verify(secret, code, now.Add(20*time.Second), DefaultWindow, step)
	if again || later {
		t.Errorf("the same code was accepted a second time (same moment %v, 20 s later %v)", again, later)
	}
}

func TestVerifyReportsABadSecret(t *testing.T) {
	if _, ok, err := Verify("not base32!", "123456", time.Now(), 1, -1); ok || err == nil {
		t.Errorf("a bad secret must be an error, got ok=%v err=%v", ok, err)
	}
}

func TestGenerateSecret(t *testing.T) {
	format := regexp.MustCompile(`^[A-Z2-7]{32}$`)

	a, err := GenerateSecret(rand.Reader)
	if err != nil || !format.MatchString(a) {
		t.Fatalf("GenerateSecret = %q, %v", a, err)
	}
	b, _ := GenerateSecret(rand.Reader)
	if a == b {
		t.Error("two secrets are equal")
	}
	// The secret must be usable.
	if _, err := Code(a, 1); err != nil {
		t.Errorf("the generated secret is not usable: %v", err)
	}

	// Bytes at or above 224 are skipped, the rest map in order: 0 -> A, 31 -> 7, 32 -> A again.
	input := append([]byte{255, 224, 0, 1, 31, 32}, bytes.Repeat([]byte{0}, 100)...)
	got, _ := GenerateSecret(bytes.NewReader(input))
	if !strings.HasPrefix(got, "AB7A") {
		t.Errorf("mapping = %q, want it to start with AB7A", got)
	}

	if _, err := GenerateSecret(iotest.ErrReader(errors.New("no entropy"))); err == nil {
		t.Error("a failing random source must be an error")
	}
}

func TestProvisioningURI(t *testing.T) {
	got := ProvisioningURI("My Panel", "ada+test@example.com", "ABCDEF234567")

	want := "otpauth://totp/MyPanel:ada%2Btest%40example.com?secret=ABCDEF234567&issuer=MyPanel"
	if got != want {
		t.Errorf("URI = %q, want %q", got, want)
	}
	if u := ProvisioningURI("Panel", "a b:c@example.com", "AB"); !strings.Contains(u, "a%20b%3Ac%40example.com") {
		t.Errorf("the account must be escaped, a colon included: %s", u)
	}
}

func TestStep(t *testing.T) {
	if Step(time.Unix(59, 0)) != 1 || Step(time.Unix(60, 0)) != 2 || Step(time.Unix(0, 0)) != 0 {
		t.Error("Step does not divide by the 30 second period")
	}
}
