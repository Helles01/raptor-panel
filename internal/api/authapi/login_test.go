package authapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/login"
	"github.com/Helles01/raptor-panel/internal/auth/totp"
)

func TestCSRFCookieGivesASessionAndAToken(t *testing.T) {
	r := newRig(t)

	rp := r.do(http.MethodGet, "/sanctum/csrf-cookie", nil)

	if rp.Status != http.StatusNoContent {
		t.Fatalf("status = %d", rp.Status)
	}
	if r.cookie("raptor_session") == "" || r.cookie("XSRF-TOKEN") == "" {
		t.Errorf("cookies: session %q, token %q", r.cookie("raptor_session"), r.cookie("XSRF-TOKEN"))
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}
}

func TestCSRFCookieKeepsTheSession(t *testing.T) {
	r := newRig(t)
	r.prime()
	session1, token1 := r.cookie("raptor_session"), r.cookie("XSRF-TOKEN")

	r.prime()

	if r.cookie("raptor_session") != session1 {
		t.Error("a second call must not replace the session")
	}
	if r.cookie("XSRF-TOKEN") == token1 {
		t.Error("a new token is issued each time")
	}
	if rp := r.login("ada", adaPassword); rp.Status != http.StatusOK {
		t.Errorf("the new token must work: %d %s", rp.Status, rp.Body)
	}
}

func TestLoginNeedsTheCSRFToken(t *testing.T) {
	r := newRig(t)
	r.prime()

	rp := r.do(http.MethodPost, "/auth/login", map[string]string{"user": "ada", "password": adaPassword}, "X-XSRF-TOKEN", "-")

	if rp.Status != http.StatusForbidden || r.users.lookups != 0 {
		t.Errorf("status = %d, lookups = %d; a request without the token must not even be looked at", rp.Status, r.users.lookups)
	}
}

func TestLoginWithoutAnySession(t *testing.T) {
	r := newRig(t)

	rp := r.login("ada", adaPassword)

	if rp.Status != http.StatusForbidden || rp.detail() != "CSRF token mismatch." {
		t.Errorf("got %d %q", rp.Status, rp.detail())
	}
}

func TestLoginSignsIn(t *testing.T) {
	r := newRig(t)
	r.prime()
	guest := r.cookie("raptor_session")

	rp := r.login("ada", adaPassword)

	if rp.Status != http.StatusOK {
		t.Fatalf("status = %d %s", rp.Status, rp.Body)
	}
	data := rp.json()["data"].(map[string]any)
	if data["complete"] != true || data["intended"] != "/" {
		t.Errorf("data = %v", data)
	}
	user := data["user"].(map[string]any)
	if user["uuid"] != ada().UUID || user["username"] != "ada" || user["email"] != "ada@example.com" || user["root_admin"] != false {
		t.Errorf("user = %v", user)
	}
	for _, secret := range []string{"password", "id", "totp_secret", "PasswordHash"} {
		if _, has := user[secret]; has {
			t.Errorf("the user object holds %q", secret)
		}
	}
	if strings.Contains(string(rp.Body), "$2a$") {
		t.Error("the answer holds a password hash")
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}

	if now := r.cookie("raptor_session"); now == "" || now == guest {
		t.Errorf("the session must be a new one: %q was %q", now, guest)
	}
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != 200 || who.json()["user"] != float64(1) {
		t.Errorf("whoami = %d %s", who.Status, who.Body)
	}
	if got := r.log.events(); len(got) != 1 || got[0] != "auth:success" {
		t.Errorf("events = %v", got)
	}
}

func TestLoginLogsTheRequest(t *testing.T) {
	r := newRig(t)
	r.prime()

	r.login("ada", adaPassword)

	e := r.log.entries[0]
	if e.Subject != 1 || e.Actor != 1 {
		t.Errorf("subject %d, actor %d; want the user for both", e.Subject, e.Actor)
	}
	if !strings.Contains(e.Properties, `"ip":"203.0.113.9"`) || !strings.Contains(e.Properties, `"useragent":"TestBrowser/1.0"`) {
		t.Errorf("properties = %s", e.Properties)
	}
	if strings.Contains(e.Properties, adaPassword) || strings.Contains(e.Properties, "ada@") {
		t.Errorf("properties = %s: neither the password nor the typed name belongs in the log", e.Properties)
	}
}

func TestLoginByEmailOrUserName(t *testing.T) {
	for _, name := range []string{"ada", "ADA", "ada@example.com", "Ada@Example.com", "  ada  "} {
		r := newRig(t)
		r.prime()

		if rp := r.login(name, adaPassword); rp.Status != http.StatusOK {
			t.Errorf("login as %q = %d %s", name, rp.Status, rp.Body)
		}
	}
}

func TestLoginFailureLooksTheSameForEveryCause(t *testing.T) {
	r := newRig(t)
	r.prime()

	wrong := r.login("ada", "not the password")
	unknown := r.login("nobody", adaPassword)
	unusable := r.login(strings.Repeat("a", 300), adaPassword)

	for name, rp := range map[string]reply{"wrong": wrong, "unknown": unknown, "unusable": unusable} {
		if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrFailed.Error() {
			t.Errorf("%s: %d %q", name, rp.Status, rp.detail())
		}
		if got := rp.json()["errors"].([]any)[0].(map[string]any)["code"]; got != "DisplayException" {
			t.Errorf("%s: code = %v", name, got)
		}
	}
	if string(wrong.Body) != string(unknown.Body) || string(wrong.Body) != string(unusable.Body) {
		t.Errorf("the bodies differ:\n%s\n%s\n%s", wrong.Body, unknown.Body, unusable.Body)
	}
}

func TestLoginFailureIsLoggedForAKnownUserOnly(t *testing.T) {
	r := newRig(t)
	r.prime()

	r.login("nobody", "x")
	if len(r.log.entries) != 0 {
		t.Errorf("entries = %v; a user that does not exist has no log", r.log.events())
	}
	r.login("ada", "wrong")
	if got := r.log.events(); len(got) != 1 || got[0] != "auth:fail" || r.log.entries[0].Subject != 1 || r.log.entries[0].Actor != 0 {
		t.Errorf("events = %v, entries = %+v", got, r.log.entries)
	}
	if strings.Contains(r.log.entries[0].Properties, "wrong") || strings.Contains(r.log.entries[0].Properties, "\"user\"") {
		t.Errorf("properties = %s", r.log.entries[0].Properties)
	}
}

func TestLoginUnusableNamesAreNotLookedUp(t *testing.T) {
	bodies := []any{
		`{"password":"x"}`,
		`{"user":"","password":"x"}`,
		`{"user":"   ","password":"x"}`,
		`{"user":5,"password":"x"}`,
		`{"user":["ada"],"password":"x"}`,
		`{"user":"a\u0000b","password":"x"}`,
	}
	for _, b := range bodies {
		r := newRig(t)
		r.prime()

		rp := r.do(http.MethodPost, "/auth/login", b)

		if rp.Status != http.StatusBadRequest || r.users.lookups != 0 {
			t.Errorf("%v: status %d, lookups %d", b, rp.Status, r.users.lookups)
		}
	}
}

func TestLoginWithAFailingStore(t *testing.T) {
	r := newRig(t)
	r.prime()
	r.users.failFind = errors.New("db down")

	rp := r.login("ada", adaPassword)

	if rp.Status != http.StatusInternalServerError || strings.Contains(string(rp.Body), "db down") {
		t.Errorf("got %d %s; a failing store is a 500 that says nothing", rp.Status, rp.Body)
	}
}

func TestLoginWithABrokenBody(t *testing.T) {
	r := newRig(t)
	r.prime()

	for _, b := range []string{"", "not json", `{"user":`, `[]`} {
		if rp := r.do(http.MethodPost, "/auth/login", b); rp.Status != http.StatusBadRequest && rp.Status != http.StatusUnprocessableEntity {
			t.Errorf("body %q: status %d", b, rp.Status)
		}
	}
}

func TestLoginIsBlockedAfterFiveFailuresOfOneAccountFromOneClient(t *testing.T) {
	r := newRig(t)
	r.prime()
	for i := 0; i < 5; i++ {
		if rp := r.login("ada", "wrong"); rp.Status != http.StatusBadRequest {
			t.Fatalf("attempt %d = %d", i, rp.Status)
		}
	}

	rp := r.login("ada", adaPassword)

	if rp.Status != http.StatusTooManyRequests || rp.Header.Get("Retry-After") == "" {
		t.Errorf("status = %d, Retry-After = %q; even the right password must wait", rp.Status, rp.Header.Get("Retry-After"))
	}
}

func TestOneClientDoesNotLockOutAnotherOrAnotherAccount(t *testing.T) {
	r := newRig(t)
	r.prime()
	for i := 0; i < 5; i++ {
		r.login("ada", "wrong")
	}

	if rp := r.login("grace", "wrong"); rp.Status != http.StatusBadRequest {
		t.Errorf("another account from the same client = %d, want it counted apart", rp.Status)
	}
	r.ip = "198.51.100.77"
	if rp := r.login("ada", adaPassword); rp.Status != http.StatusOK {
		t.Errorf("the same account from another client = %d %s; the attacker must not lock the owner out", rp.Status, rp.Body)
	}
}

func TestFailuresOfAnAccountAreLimitedAcrossClients(t *testing.T) {
	r := newRig(t)
	r.prime()
	for i := 0; i < 20; i++ {
		r.ip = "198.51.100." + string(rune('1'+i%9))
		r.login("ada", "wrong")
		if i%9 == 8 {
			r.ip = "198.51.101." + string(rune('1'+i%9))
		}
	}
	r.ip = "192.0.2.50"

	rp := r.login("ada", adaPassword)

	if rp.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d; an account that was guessed at from many addresses is limited as a whole", rp.Status)
	}
}

func TestLoginRequestsAreLimitedPerAddress(t *testing.T) {
	r := newRig(t)
	r.prime()
	var last reply
	for i := 0; i < 11; i++ {
		// A different name each time, so that the counters of failures do not block first.
		last = r.login(fmt.Sprintf("nobody%d", i), "x")
	}

	if last.Status != http.StatusTooManyRequests {
		t.Errorf("11th request = %d, want the 10 a minute limit of the endpoint", last.Status)
	}
}

func TestTheCheckpointIsLimitedPerAddressToo(t *testing.T) {
	r := newRig(t)
	r.deps.LoginLimiter, _ = middleware.NewLimiter(3, time.Minute)
	r.restart()
	token := r.startSecondStep() // the first request of the three

	first := r.checkpoint(token, "000000", "")
	second := r.checkpoint(token, "000000", "")
	third := r.checkpoint(token, "000000", "")

	if first.Status != http.StatusBadRequest || second.Status != http.StatusBadRequest || third.Status != http.StatusTooManyRequests {
		t.Errorf("statuses = %d, %d, %d; the checkpoint shares the limit of the sign-in endpoints", first.Status, second.Status, third.Status)
	}
}

func TestLoginOfAGuestRouteIsClosedToTheSignedIn(t *testing.T) {
	r := newRig(t)
	r.prime()
	r.login("ada", adaPassword)

	if rp := r.login("ada", adaPassword); rp.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a guest route", rp.Status)
	}
}

func TestLoginWithTwoFactorAsksForASecondStep(t *testing.T) {
	r := newRig(t)
	r.prime()

	rp := r.login("grace", adaPassword)

	if rp.Status != http.StatusOK {
		t.Fatalf("status = %d %s", rp.Status, rp.Body)
	}
	data := rp.json()["data"].(map[string]any)
	token, _ := data["confirmation_token"].(string)
	if data["complete"] != false || len(token) != 64 {
		t.Errorf("data = %v", data)
	}
	if _, has := data["user"]; has {
		t.Error("the user must not be given before the second step")
	}
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != http.StatusUnauthorized {
		t.Errorf("whoami = %d: the user is not signed in yet", who.Status)
	}
	if got := r.log.events(); len(got) != 1 || got[0] != "auth:checkpoint" {
		t.Errorf("events = %v", got)
	}
}

func TestLoginWithTwoFactorAndAWrongPasswordAsksForNothing(t *testing.T) {
	r := newRig(t)
	r.prime()

	rp := r.login("grace", "wrong")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrFailed.Error() {
		t.Errorf("got %d %q; the second step must not show that the account exists", rp.Status, rp.detail())
	}
}

func (r *rig) checkpoint(token, code, recovery string) reply {
	r.t.Helper()
	body := map[string]any{"confirmation_token": token}
	if code != "" {
		body["authentication_code"] = code
	}
	if recovery != "" {
		body["recovery_token"] = recovery
	}
	return r.do(http.MethodPost, "/auth/login/checkpoint", body)
}

// startSecondStep signs in as grace up to the second step and returns the confirmation token.
func (r *rig) startSecondStep() string {
	r.t.Helper()
	r.prime()
	rp := r.login("grace", adaPassword)
	token, _ := rp.json()["data"].(map[string]any)["confirmation_token"].(string)
	if token == "" {
		r.t.Fatalf("no confirmation token: %d %s", rp.Status, rp.Body)
	}
	return token
}

func (r *rig) code() string {
	c, err := totp.Code(totpSecret, totp.Step(r.clk.get()))
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func TestCheckpointSignsInWithACode(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()

	rp := r.checkpoint(token, r.code(), "")

	if rp.Status != http.StatusOK || rp.json()["data"].(map[string]any)["complete"] != true {
		t.Fatalf("status = %d %s", rp.Status, rp.Body)
	}
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != 200 || who.json()["user"] != float64(2) {
		t.Errorf("whoami = %d %s", who.Status, who.Body)
	}
	used := r.users.get(2).TOTPLastUsed
	if used == nil || !used.Equal(time.Unix(totp.Step(r.clk.get())*totp.Period, 0)) {
		t.Errorf("the step of the code must be stored: %v", used)
	}
	if got := strings.Join(r.log.events(), ","); got != "auth:checkpoint,auth:token,auth:success" {
		t.Errorf("events = %s", got)
	}
}

func TestACodeCannotBeUsedTwice(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()
	code := r.code()
	if rp := r.checkpoint(token, code, ""); rp.Status != http.StatusOK {
		t.Fatalf("first use = %d %s", rp.Status, rp.Body)
	}
	r.do(http.MethodPost, "/auth/logout", nil)

	token2 := r.startSecondStep()
	rp := r.checkpoint(token2, code, "")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrCheckpointFailed.Error() {
		t.Errorf("second use = %d %q", rp.Status, rp.detail())
	}
}

func TestACodeThatAnotherRequestUsedFirstDoesNotCount(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()
	r.users.stepTaken = true

	rp := r.checkpoint(token, r.code(), "")

	if rp.Status != http.StatusBadRequest {
		t.Errorf("status = %d; two requests with one code must not both be let in", rp.Status)
	}
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != http.StatusUnauthorized {
		t.Error("the loser must not be signed in")
	}
}

func TestCheckpointRefusesAWrongCode(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()

	rp := r.checkpoint(token, "000000", "")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrCheckpointFailed.Error() {
		t.Errorf("got %d %q", rp.Status, rp.detail())
	}
	if got := r.log.events(); got[len(got)-1] != "auth:fail" {
		t.Errorf("events = %v; a failed second step is logged for the user", got)
	}
}

func TestCheckpointCancelsTheLoginAfterFiveWrongCodes(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()
	for i := 0; i < 5; i++ {
		if rp := r.checkpoint(token, "000000", ""); rp.Status != http.StatusBadRequest {
			t.Fatalf("attempt %d = %d", i, rp.Status)
		}
	}

	rp := r.checkpoint(token, r.code(), "")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrTokenExpired.Error() {
		t.Errorf("got %d %q; the pending login must be gone and the password asked for again", rp.Status, rp.detail())
	}
}

func TestCheckpointWithAWrongConfirmationToken(t *testing.T) {
	r := newRig(t)
	r.startSecondStep()

	rp := r.checkpoint(strings.Repeat("x", 64), r.code(), "")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrCheckpointFailed.Error() {
		t.Errorf("got %d %q", rp.Status, rp.detail())
	}
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != http.StatusUnauthorized {
		t.Error("a wrong token must not sign in")
	}
}

func TestCheckpointWithoutAPendingLoginIsCounted(t *testing.T) {
	r := newRig(t)
	r.prime()
	for i := 0; i < 5; i++ {
		r.checkpoint(strings.Repeat("x", 64), "123456", "")
	}

	rp := r.checkpoint(strings.Repeat("x", 64), "123456", "")

	if rp.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d; guessing without a pending login is limited too", rp.Status)
	}
}

func TestCheckpointAcceptsTheNeighbouringSteps(t *testing.T) {
	for _, offset := range []time.Duration{-30 * time.Second, 30 * time.Second} {
		r := newRig(t)
		token := r.startSecondStep()
		code, err := totp.Code(totpSecret, totp.Step(r.clk.get().Add(offset)))
		if err != nil {
			t.Fatal(err)
		}

		if rp := r.checkpoint(token, code, ""); rp.Status != http.StatusOK {
			t.Errorf("a code %v off the clock = %d %s; one step of drift is allowed", offset, rp.Status, rp.Body)
		}
	}
	for _, offset := range []time.Duration{-60 * time.Second, 60 * time.Second} {
		r := newRig(t)
		token := r.startSecondStep()
		code, err := totp.Code(totpSecret, totp.Step(r.clk.get().Add(offset)))
		if err != nil {
			t.Fatal(err)
		}

		if rp := r.checkpoint(token, code, ""); rp.Status != http.StatusBadRequest {
			t.Errorf("a code %v off the clock = %d; two steps is too far", offset, rp.Status)
		}
	}
}

func TestCheckpointWithoutAPendingLogin(t *testing.T) {
	r := newRig(t)
	r.prime()

	rp := r.checkpoint(strings.Repeat("x", 64), "123456", "")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrTokenExpired.Error() {
		t.Errorf("got %d %q", rp.Status, rp.detail())
	}
}

func TestCheckpointWithoutACodeIsAFormError(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()

	rp := r.checkpoint(token, "", "")

	if rp.Status != http.StatusUnprocessableEntity || rp.detail() != login.ErrNoSecondFactor.Error() {
		t.Errorf("got %d %q", rp.Status, rp.detail())
	}
	// It is not a failure: the login is still pending.
	if rp := r.checkpoint(token, r.code(), ""); rp.Status != http.StatusOK {
		t.Errorf("the pending login must have survived: %d %s", rp.Status, rp.Body)
	}
}

func TestCheckpointRefusesMalformedValues(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()

	for _, code := range []string{"12345", "1234567", "abcdef", "12 456"} {
		if rp := r.checkpoint(token, code, ""); rp.Status != http.StatusBadRequest || rp.detail() != login.ErrCheckpointFailed.Error() {
			t.Errorf("code %q: %d %q", code, rp.Status, rp.detail())
		}
	}
	if rp := r.checkpoint(token, "", "short"); rp.Status != http.StatusBadRequest || rp.detail() != login.ErrRecoveryFailed.Error() {
		t.Errorf("a malformed recovery token: %d %q", rp.Status, rp.detail())
	}
}

func recoveryHash(t *testing.T, token string) string {
	t.Helper()
	h, err := auth.HashPassword(token)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCheckpointSignsInWithARecoveryTokenAndSpendsIt(t *testing.T) {
	r := newRig(t)
	r.recovery.tokens[2] = []RecoveryToken{{ID: 7, Hash: recoveryHash(t, "AAAAAAAAAA")}, {ID: 8, Hash: recoveryHash(t, "BBBBBBBBBB")}}
	token := r.startSecondStep()

	rp := r.checkpoint(token, "", "BBBBBBBBBB")

	if rp.Status != http.StatusOK {
		t.Fatalf("status = %d %s", rp.Status, rp.Body)
	}
	if len(r.recovery.deleted) != 1 || r.recovery.deleted[0] != 8 || len(r.recovery.tokens[2]) != 1 {
		t.Errorf("deleted = %v, left = %v; only the token that was used goes", r.recovery.deleted, r.recovery.tokens[2])
	}
	if got := strings.Join(r.log.events(), ","); got != "auth:checkpoint,auth:recovery-token,auth:success" {
		t.Errorf("events = %s", got)
	}

	r.do(http.MethodPost, "/auth/logout", nil)
	token2 := r.startSecondStep()
	if rp := r.checkpoint(token2, "", "BBBBBBBBBB"); rp.Status != http.StatusBadRequest || rp.detail() != login.ErrRecoveryFailed.Error() {
		t.Errorf("reuse = %d %q", rp.Status, rp.detail())
	}
}

func TestARecoveryTokenAnotherRequestSpentDoesNotCount(t *testing.T) {
	r := newRig(t)
	r.recovery.tokens[2] = []RecoveryToken{{ID: 7, Hash: recoveryHash(t, "AAAAAAAAAA")}}
	r.recovery.raceLost = true
	token := r.startSecondStep()

	rp := r.checkpoint(token, "", "AAAAAAAAAA")

	if rp.Status != http.StatusBadRequest {
		t.Errorf("status = %d; a token that was already deleted must not sign in", rp.Status)
	}
}

func TestARecoveryTokenWinsOverACode(t *testing.T) {
	r := newRig(t)
	r.recovery.tokens[2] = []RecoveryToken{{ID: 7, Hash: recoveryHash(t, "AAAAAAAAAA")}}
	token := r.startSecondStep()

	rp := r.checkpoint(token, r.code(), "ZZZZZZZZZZ")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrRecoveryFailed.Error() {
		t.Errorf("got %d %q; a wrong recovery token must not fall back to the code", rp.Status, rp.detail())
	}
}

func TestCheckpointFailuresAreCountedAndBlock(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()
	for i := 0; i < 5; i++ {
		r.checkpoint(token, "000000", "")
	}
	token2 := r.startSecondStep()

	rp := r.checkpoint(token2, r.code(), "")

	if rp.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d; five failures for one user from one client block the next step", rp.Status)
	}
}

func TestCheckpointForAUserWithoutASecret(t *testing.T) {
	u := grace()
	u.TOTPSecret = ""
	r := newRig(t, ada(), u)
	token := r.startSecondStep()

	rp := r.checkpoint(token, "123456", "")

	if rp.Status != http.StatusBadRequest {
		t.Errorf("status = %d; two-factor without a secret can only fail", rp.Status)
	}
}

func TestCheckpointForAUserWhoDisappeared(t *testing.T) {
	r := newRig(t)
	token := r.startSecondStep()
	delete(r.users.users, 2)

	rp := r.checkpoint(token, r.code(), "")

	if rp.Status != http.StatusBadRequest || rp.detail() != login.ErrTokenExpired.Error() {
		t.Errorf("got %d %q", rp.Status, rp.detail())
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	r := newRig(t)
	r.prime()
	r.login("ada", adaPassword)
	id := r.cookie("raptor_session")

	rp := r.do(http.MethodPost, "/auth/logout", nil)

	if rp.Status != http.StatusNoContent {
		t.Fatalf("status = %d", rp.Status)
	}
	if r.cookie("raptor_session") != "" || r.cookie("XSRF-TOKEN") != "" {
		t.Error("the cookies must be cleared")
	}
	r.client.Jar.SetCookies(mustURL(r.ts.URL), []*http.Cookie{{Name: "raptor_session", Value: id, Path: "/"}})
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != http.StatusUnauthorized {
		t.Errorf("the old cookie still works: %d", who.Status)
	}
}

func TestLogoutClearsBothCookies(t *testing.T) {
	r := newRig(t)
	r.prime()
	r.login("ada", adaPassword)

	rp := r.do(http.MethodPost, "/auth/logout", nil)

	cleared := map[string]bool{}
	for _, c := range rp.Header.Values("Set-Cookie") {
		if strings.Contains(c, "Max-Age=0") {
			cleared[strings.SplitN(c, "=", 2)[0]] = true
		}
	}
	if !cleared["raptor_session"] || !cleared["XSRF-TOKEN"] {
		t.Errorf("Set-Cookie = %v, want both cookies cleared with Max-Age=0", rp.Header.Values("Set-Cookie"))
	}
}

func TestLogoutNeedsTheCSRFToken(t *testing.T) {
	r := newRig(t)
	r.prime()
	r.login("ada", adaPassword)

	rp := r.do(http.MethodPost, "/auth/logout", nil, "X-XSRF-TOKEN", "-")

	if rp.Status != http.StatusForbidden {
		t.Errorf("status = %d; another site must not be able to sign a user out", rp.Status)
	}
}

func TestLogoutOfAGuestIsRefused(t *testing.T) {
	r := newRig(t)
	r.prime()

	if rp := r.do(http.MethodPost, "/auth/logout", nil); rp.Status != http.StatusUnauthorized {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestASessionPlantedBeforeTheLoginIsWorthless(t *testing.T) {
	r := newRig(t)
	r.prime()
	planted := r.cookie("raptor_session")

	r.login("ada", adaPassword)

	r.client.Jar.SetCookies(mustURL(r.ts.URL), []*http.Cookie{{Name: "raptor_session", Value: planted, Path: "/"}})
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != http.StatusUnauthorized {
		t.Errorf("the guest session of before the login signs in: %d", who.Status)
	}
}

func TestLoginAnswersDoNotLeakThePassword(t *testing.T) {
	r := newRig(t)
	r.prime()
	rp := r.login("ada", adaPassword)

	if strings.Contains(string(rp.Body), adaPassword) || strings.Contains(r.logs.String(), adaPassword) {
		t.Error("the password appears in an answer or a log")
	}
}

func TestVerifiedPasswordsOfTheReferenceStillSignIn(t *testing.T) {
	u := ada()
	u.PasswordHash = "$2y" + adaHash[3:]
	r := newRig(t, u)
	r.prime()

	if rp := r.login("ada", adaPassword); rp.Status != http.StatusOK {
		t.Errorf("a $2y$ hash from the reference = %d %s", rp.Status, rp.Body)
	}
}
