package account

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/login"
	"github.com/Helles01/raptor-panel/internal/auth/totp"
	"github.com/Helles01/raptor-panel/internal/fielderr"
)

var now = time.Date(2026, 10, 23, 12, 0, 20, 0, time.UTC)

func TestConfirmCounters(t *testing.T) {
	got, err := ConfirmCounters(7, "203.0.113.9:51234")
	if err != nil {
		t.Fatalf("ConfirmCounters: %v", err)
	}

	want := []struct {
		key    string
		limit  int
		window time.Duration
	}{
		{"confirm|user-client|id:7|203.0.113.9", 5, time.Minute},
		{"confirm|user|id:7", 20, 10 * time.Minute},
		{"confirm|client|203.0.113.9", 40, 10 * time.Minute},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d counters, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Key != w.key || got[i].Limit != w.limit || got[i].Window != w.window {
			t.Errorf("counter %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestConfirmCountersNameAnIPv6ClientByItsNetwork(t *testing.T) {
	a, _ := ConfirmCounters(7, "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443")
	b, _ := ConfirmCounters(7, "2001:db8:1:2:1111:2222:3333:4444")

	if a[2].Key != b[2].Key || !strings.HasSuffix(a[2].Key, "2001:db8:1:2::/64") {
		t.Errorf("keys %q and %q should both name the /64 network", a[2].Key, b[2].Key)
	}
}

func TestConfirmCountersRefuseWhatCannotBeCounted(t *testing.T) {
	if _, err := ConfirmCounters(7, "not an address"); !errors.Is(err, login.ErrNoClient) {
		t.Errorf("an unreadable client: err = %v, want ErrNoClient", err)
	}
	if _, err := ConfirmCounters(0, "203.0.113.9"); !errors.Is(err, login.ErrNoClient) {
		t.Errorf("no user: err = %v, want ErrNoClient", err)
	}
}

func TestConfirmCountersAreSeparateForEveryUser(t *testing.T) {
	a, _ := ConfirmCounters(7, "203.0.113.9")
	b, _ := ConfirmCounters(8, "203.0.113.9")

	if a[0].Key == b[0].Key || a[1].Key == b[1].Key {
		t.Error("two users share a counter")
	}
	if a[2].Key != b[2].Key {
		t.Error("the client counter must be shared by the users of one address")
	}
}

func TestEmailChangeCounter(t *testing.T) {
	c := EmailChangeCounter("AAAA1111-BBBB-4CCC-8DDD-EEEEFFFF0000")

	if c.Key != "email-change|aaaa1111-bbbb-4ccc-8ddd-eeeeffff0000" || c.Limit != 3 || c.Window != 24*time.Hour {
		t.Errorf("counter = %+v", c)
	}
}

func TestPlanEmailChange(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		blocked   bool
		want      EmailPlan
		wantErr   error
		wantField string
	}{
		{"another address", "new@example.com", false, EmailPlan{Change: true, Old: "ada@example.com", New: "new@example.com"}, nil, ""},
		{"the same address", "ada@example.com", false, EmailPlan{Change: false, Old: "ada@example.com", New: "ada@example.com"}, nil, ""},
		{"the same address in another case", "ADA@Example.com", false, EmailPlan{Change: false, Old: "ada@example.com", New: "ADA@Example.com"}, nil, ""},
		{"blocked", "new@example.com", true, EmailPlan{}, ErrEmailChanges, ""},
		{"blocked even for no change", "ada@example.com", true, EmailPlan{}, ErrEmailChanges, ""},
		{"blocked before the address is checked", "nope", true, EmailPlan{}, ErrEmailChanges, ""},
		{"not an address", "nope", false, EmailPlan{}, nil, "email"},
		{"a display name", "Ada <ada@new.example.com>", false, EmailPlan{}, nil, "email"},
		{"empty", "", false, EmailPlan{}, nil, "email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanEmailChange("ada@example.com", tt.requested, tt.blocked)

			if tt.wantField != "" {
				if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.wantField {
					t.Fatalf("error = %v, want one about %q", err, tt.wantField)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPlanPasswordChange(t *testing.T) {
	plan, err := PlanPasswordChange(true, "a brand new password", "a brand new password")
	if err != nil {
		t.Fatalf("PlanPasswordChange: %v", err)
	}

	if plan.Hash == "" || strings.Contains(plan.Hash, "brand new") || !auth.VerifyPassword(plan.Hash, "a brand new password") {
		t.Errorf("Hash = %q does not verify against the new password", plan.Hash)
	}
	if !plan.RevokeOtherSessions || !plan.DeauthorizeNodes || !plan.DeleteTokens {
		t.Errorf("effects = %+v, want all three", plan)
	}
}

func TestPlanPasswordChangeChecksTheCurrentPasswordFirst(t *testing.T) {
	plan, err := PlanPasswordChange(false, "x", "y")

	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("error = %v, want ErrInvalidPassword before any rule about the new password", err)
	}
	if plan != (PasswordPlan{}) {
		t.Errorf("a refused change must plan nothing: %+v", plan)
	}
}

func TestPlanPasswordChangeRefusals(t *testing.T) {
	tests := []struct {
		name         string
		password     string
		confirmation string
		want         string
	}{
		{"empty", "", "", "The password field is required."},
		{"not confirmed", "a long enough password", "another long password", "confirmation does not match"},
		{"no confirmation", "a long enough password", "", "confirmation does not match"},
		{"too short", "short", "short", "at least 8 characters"},
		{"too long", strings.Repeat("a", 73), strings.Repeat("a", 73), "greater than 72 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := PlanPasswordChange(true, tt.password, tt.confirmation)

			var pe *PasswordError
			if !errors.As(err, &pe) {
				t.Fatalf("error = %v, want a *PasswordError", err)
			}
			if !strings.Contains(strings.Join(pe.Messages, " "), tt.want) {
				t.Errorf("messages = %v, want one containing %q", pe.Messages, tt.want)
			}
			if plan != (PasswordPlan{}) {
				t.Errorf("a refused change must plan nothing: %+v", plan)
			}
		})
	}
}

func TestPasswordErrorText(t *testing.T) {
	err := &PasswordError{Messages: []string{"one.", "two."}}

	if got := err.Error(); got != "password: one. two." {
		t.Errorf("Error() = %q", got)
	}
}

func TestPlanTwoFactorSetup(t *testing.T) {
	got, err := PlanTwoFactorSetup(false, "My Panel", "ada+x@example.com", rand.Reader)
	if err != nil {
		t.Fatalf("PlanTwoFactorSetup: %v", err)
	}

	if len(got.Secret) != totp.SecretLength {
		t.Errorf("secret %q has %d characters, want %d", got.Secret, len(got.Secret), totp.SecretLength)
	}
	want := totp.ProvisioningURI("My Panel", "ada+x@example.com", got.Secret)
	if got.URI != want || !strings.Contains(got.URI, "ada%2Bx%40example.com") || !strings.Contains(got.URI, "issuer=MyPanel") {
		t.Errorf("URI = %q, want %q", got.URI, want)
	}
}

func TestPlanTwoFactorSetupIsRefusedWhileEnabled(t *testing.T) {
	got, err := PlanTwoFactorSetup(true, "My Panel", "ada@example.com", rand.Reader)

	if !errors.Is(err, ErrTwoFactorEnabled) || got != (Setup{}) {
		t.Errorf("got %+v, %v, want ErrTwoFactorEnabled and no secret", got, err)
	}
}

func TestPlanTwoFactorSetupFailsOnRandomSourceError(t *testing.T) {
	if _, err := PlanTwoFactorSetup(false, "P", "a@example.com", bytes.NewReader([]byte{1})); err == nil {
		t.Error("a failing random source must be an error")
	}
}

const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

func codeAt(t *testing.T, at time.Time) string {
	t.Helper()
	c, err := totp.Code(secret, totp.Step(at))
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	return c
}

func TestPlanTwoFactorEnable(t *testing.T) {
	got, err := PlanTwoFactorEnable(true, codeAt(t, now), secret, now, rand.Reader)
	if err != nil {
		t.Fatalf("PlanTwoFactorEnable: %v", err)
	}

	if len(got.Tokens) != login.RecoveryTokenCount || len(got.Hashes) != login.RecoveryTokenCount {
		t.Fatalf("%d tokens and %d hashes, want %d of each", len(got.Tokens), len(got.Hashes), login.RecoveryTokenCount)
	}
	for i, tok := range got.Tokens {
		if len(tok) != login.RecoveryTokenLength || !auth.VerifyPassword(got.Hashes[i], tok) {
			t.Errorf("token %d (%q) does not match its hash", i, tok)
		}
		if strings.Contains(got.Hashes[i], tok) {
			t.Errorf("hash %d holds the token", i)
		}
	}
	if want := time.Unix(totp.Step(now)*totp.Period, 0).UTC(); !got.UsedAt.Equal(want) {
		t.Errorf("UsedAt = %v, want the start of the confirmed step %v", got.UsedAt, want)
	}
}

func TestPlanTwoFactorEnableTakesTheStepThatMatched(t *testing.T) {
	previous := now.Add(-totp.Period * time.Second)

	got, err := PlanTwoFactorEnable(true, codeAt(t, previous), secret, now, rand.Reader)
	if err != nil {
		t.Fatalf("PlanTwoFactorEnable: %v", err)
	}

	if want := time.Unix(totp.Step(previous)*totp.Period, 0).UTC(); !got.UsedAt.Equal(want) {
		t.Errorf("UsedAt = %v, want %v: the step of the code, not of the clock", got.UsedAt, want)
	}
}

func TestPlanTwoFactorEnableRefusals(t *testing.T) {
	good := codeAt(t, now)
	tests := []struct {
		name       string
		passwordOK bool
		code       string
		secret     string
		at         time.Time
		wantErr    error
		wantField  string
	}{
		{"wrong password", false, good, secret, now, ErrTwoFactorPassword, ""},
		{"no secret asked for", true, good, "", now, ErrNotSetUp, ""},
		{"wrong code", true, "000000", secret, now, ErrTwoFactorToken, ""},
		{"a code two steps old", true, codeAt(t, now.Add(-2*totp.Period*time.Second)), secret, now, ErrTwoFactorToken, ""},
		{"a code two steps ahead", true, codeAt(t, now.Add(2*totp.Period*time.Second)), secret, now, ErrTwoFactorToken, ""},
		{"letters", true, "abcdef", secret, now, ErrTwoFactorToken, ""},
		{"six characters that are many bytes", true, "éééééé", secret, now, ErrTwoFactorToken, ""},
		{"five characters", true, good[:5], secret, now, nil, "code"},
		{"seven characters", true, good + "1", secret, now, nil, "code"},
		{"empty", true, "", secret, now, nil, "code"},
		{"the shape is checked before the password", false, good[:5], secret, now, nil, "code"},
		{"the password is checked before the secret", false, good, "", now, ErrTwoFactorPassword, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanTwoFactorEnable(tt.passwordOK, tt.code, tt.secret, tt.at, rand.Reader)

			if tt.wantField != "" {
				if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.wantField {
					t.Fatalf("error = %v, want one about %q", err, tt.wantField)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if len(got.Tokens) != 0 || len(got.Hashes) != 0 || !got.UsedAt.IsZero() {
				t.Errorf("a refused request must make nothing: %+v", got)
			}
		})
	}
}

func TestPlanTwoFactorEnableFailsOnRandomSourceError(t *testing.T) {
	_, err := PlanTwoFactorEnable(true, codeAt(t, now), secret, now, bytes.NewReader([]byte{1, 2, 3}))

	if err == nil || errors.Is(err, ErrTwoFactorToken) {
		t.Errorf("error = %v, want a failure of the random source", err)
	}
}

func TestPlanTwoFactorEnableWithAnUnusableSecret(t *testing.T) {
	_, err := PlanTwoFactorEnable(true, "123456", "not base32 !", now, rand.Reader)

	if err == nil || errors.Is(err, ErrTwoFactorToken) {
		t.Errorf("error = %v, want an error that is not the user's wrong code", err)
	}
}

func TestPlanTwoFactorDisable(t *testing.T) {
	got, err := PlanTwoFactorDisable(true)
	if err != nil || !got.ClearSecret || !got.DeleteRecoveryTokens {
		t.Errorf("got %+v, %v, want the secret and the recovery tokens removed", got, err)
	}

	got, err = PlanTwoFactorDisable(false)
	if !errors.Is(err, ErrTwoFactorPassword) || got != (Disable{}) {
		t.Errorf("got %+v, %v, want ErrTwoFactorPassword", got, err)
	}
}

func TestRefusalsKeepTheReferencesText(t *testing.T) {
	for err, part := range map[error]string{
		ErrInvalidPassword:   "invalid for this account",
		ErrTwoFactorPassword: "password provided was not valid",
		ErrTwoFactorEnabled:  "already enabled",
		ErrTwoFactorToken:    "two-factor authentication token was not valid",
		ErrEmailChanges:      "changed too many times today",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("%q does not contain %q", err, part)
		}
	}
}
